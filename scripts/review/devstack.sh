#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

# Start an isolated, disposable Carbon Panel for manual review.
#
# Never review against the shared development instance: another agent or
# another person can delete a server out from under a running journey, and the
# resulting report is about their action, not your change. This stack has its
# own port, its own data directory and its own installation id, so it never
# touches another panel's containers, worlds or database.
#
#   scripts/review/devstack.sh up      # build, start, seed, print the URL
#   scripts/review/devstack.sh down    # stop and remove the container
#   scripts/review/devstack.sh clean   # also delete its data (worlds, backups)
#
# `up` seeds what a review needs to be useful on the first tap: the admin
# account, a local Docker node and one stopped vanilla server. The chosen port
# is written to "<data dir>/port" so other scripts do not guess.

set -euo pipefail

NAME="${REVIEW_STACK_NAME:-carbon-panel-review}"
PORT="${REVIEW_PORT:-8099}"
GAME_PORT="${REVIEW_GAME_PORT:-25585}"
IMAGE="${REVIEW_IMAGE:-carbon-panel:review}"
USER_NAME="${PANEL_REVIEW_USER:-reviewer}"
USER_PASS="${PANEL_REVIEW_PASSWORD:-Review-Panel-2026}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
[[ "$PORT" =~ ^[0-9]+$ ]] && ((10#$PORT >= 1 && 10#$PORT <= 65515)) || {
	echo "review port must be in 1..65515" >&2; exit 2;
}
[[ "$GAME_PORT" =~ ^[0-9]+$ ]] && ((10#$GAME_PORT >= 1 && 10#$GAME_PORT <= 65515)) || {
	echo "review game port must be in 1..65515" >&2; exit 2;
}

# A session may only manage its own direct child of this fixed user cache.
# Existing unmarked directories are never adopted automatically.
[[ "$NAME" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$ ]] || { echo "invalid review session name" >&2; exit 2; }
HOME_REAL="$(realpath -m "$HOME")"
ROOT="$(realpath -m "$HOME/.cache/carbon-panel-review/sessions")"
case "$ROOT/" in "$HOME_REAL"/*) ;; *) echo "review root is outside the user home" >&2; exit 2 ;; esac
DATA_RAW="${REVIEW_DATA_DIR:-$ROOT/$NAME}"
[ ! -L "$DATA_RAW" ] || { echo "review data must not be a symlink" >&2; exit 2; }
DATA="$(realpath -m "$DATA_RAW")"
[ "$DATA" = "$ROOT/$NAME" ] || { echo "review data must be the named session beneath $ROOT" >&2; exit 2; }
[[ "$DATA" != *$'\n'* && "$DATA" != *$'\r'* ]] || { echo "invalid review data path" >&2; exit 2; }
MARKER="$DATA/.carbon-review-owner"

assert_owner() {
	[ -f "$MARKER" ] && [ ! -L "$MARKER" ] &&
		[ "$(cat "$MARKER")" = "$(printf 'carbon-review-v1\n%s\n%s' "$NAME" "$DATA")" ] || {
		echo "refusing operation: review data has no matching ownership marker" >&2
		return 1
	}
}

init_owner() {
	if [ -e "$DATA" ] && [ ! -f "$MARKER" ]; then
		echo "refusing to adopt existing unmarked review data: $DATA" >&2
		return 1
	fi
	mkdir -p "$DATA"
	if [ ! -e "$MARKER" ]; then
		(umask 077; set -o noclobber; printf 'carbon-review-v1\n%s\n%s\n' "$NAME" "$DATA" > "$MARKER")
	fi
	assert_owner
}

# free_port picks the first port from base upward that nothing is listening on,
# so a review stack does not fight the cloud stack for a fixed number.
free_port() {
	local base="$1" candidate bucket
	for bucket in $(seq 0 20); do
		candidate=$((base + bucket))
		if ! (exec 3<>"/dev/tcp/127.0.0.1/${candidate}") 2>/dev/null; then
			echo "${candidate}"
			return 0
		fi
	done
	echo "no free port in ${base}..$((base + 20))" >&2
	return 1
}

wait_for_health() {
	local deadline=$((SECONDS + 180))
	while ((SECONDS < deadline)); do
		if curl -fsS --max-time 5 -o /dev/null "http://127.0.0.1:${PORT}/healthz"; then
			return 0
		fi
		sleep 2
	done
	echo "panel did not become healthy on port ${PORT}" >&2
	docker logs --tail 40 "${NAME}" >&2 || true
	return 1
}

# rpc posts one Connect RPC and prints the response body.
rpc() {
	local path="$1" body="$2" token="${3:-}"
	if [ -n "${token}" ]; then
		curl -fsS -X POST "http://127.0.0.1:${PORT}${path}" \
			-H 'Content-Type: application/json' -H "Authorization: Bearer ${token}" -d "${body}"
	else
		curl -fsS -X POST "http://127.0.0.1:${PORT}${path}" \
			-H 'Content-Type: application/json' -d "${body}"
	fi
}

list_count() {
	node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const v=JSON.parse(s)[process.argv[1]]??[];if(!Array.isArray(v))throw Error("invalid list");console.log(v.length)}catch(e){console.error("invalid review fixture response");process.exitCode=1}})' "$1"
}

# seed makes a fresh stack usable: the first admin account, a local Docker node
# and a stopped starter server. Every step is safe to repeat.
seed() {
	local auth_payload
	auth_payload="$(REVIEW_SEED_USER="$USER_NAME" REVIEW_SEED_PASS="$USER_PASS" node -e 'console.log(JSON.stringify({username:process.env.REVIEW_SEED_USER,password:process.env.REVIEW_SEED_PASS}))')"
	rpc /carbonpanel.v1.AuthService/Register \
		"${auth_payload}" >/dev/null 2>&1 || true

	local token
	token=$(rpc /carbonpanel.v1.AuthService/Login \
		"${auth_payload}" |
		node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const t=JSON.parse(s).token;if(typeof t!=="string"||!t)throw Error();console.log(t)}catch(e){process.exitCode=1}})')
	if [ -z "${token}" ]; then
		echo "could not sign in as ${USER_NAME}; create the account by hand" >&2
		return 1
	fi

	# Without a node the panel has nowhere to place a server and every create
	# fails with "no reachable Docker node has capacity".
	local count
	count="$(rpc /carbonpanel.v1.NodeService/ListNodes '{}' "${token}" | list_count nodes)"
	if [ "$count" -eq 0 ]; then
		rpc /carbonpanel.v1.NodeService/CreateNode \
			'{"name":"review-local","host":"unix:///var/run/docker.sock","enabled":true,"maxMemoryMb":16384,"maxServers":20}' \
			"${token}" >/dev/null
	fi
	count="$(rpc /carbonpanel.v1.NodeService/ListNodes '{}' "${token}" | list_count nodes)"
	[ "$count" -gt 0 ] || { echo "review node was not created" >&2; return 1; }

	count="$(rpc /carbonpanel.v1.ServerService/ListServers '{}' "${token}" | list_count servers)"
	if [ "$count" -eq 0 ]; then
		GAME_PORT="$(free_port "$GAME_PORT")"
		rpc /carbonpanel.v1.ServerService/CreateServer \
			'{"name":"review-vanilla","mcVersion":"1.21.1","modLoader":"MOD_LOADER_VANILLA","memory":2048,"maxPlayers":10,"port":'"$GAME_PORT"',"requestId":"review-bootstrap-server"}' \
			"${token}" >/dev/null
	fi
	# A missing or failed starter is a setup failure, not a successful capture.
	local servers server_id server_status container_id record deadline timeout="${REVIEW_SEED_TIMEOUT_SECONDS:-180}"
	[[ "$timeout" =~ ^[0-9]+$ ]] && ((10#$timeout >= 1 && 10#$timeout <= 600)) || { echo "review seed timeout must be in 1..600 seconds" >&2; return 1; }
	deadline=$((SECONDS + 10#$timeout))
	# CreateServer returns before image/container creation finishes. Do not send
	# StartServer while that asynchronous lifecycle operation still owns the row.
	while :; do
		servers="$(rpc /carbonpanel.v1.ServerService/ListServers '{}' "${token}")"
		record="$(printf '%s' "$servers" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const v=JSON.parse(s).servers;if(!Array.isArray(v)||!v[0]?.id)throw Error();console.log([v[0].id,v[0].status??"",v[0].containerId??""].join("|"))}catch(e){process.exitCode=1}})')"
		IFS='|' read -r server_id server_status container_id <<< "$record"
		case "$server_status" in
			SERVER_STATUS_ERROR|SERVER_STATUS_UNHEALTHY|7|8) echo "review starter creation failed: $server_status" >&2; return 1 ;;
			SERVER_STATUS_CREATING|1) ;;
			SERVER_STATUS_STOPPED|SERVER_STATUS_RUNNING|SERVER_STATUS_STARTING|5|3|2) [ -z "$container_id" ] || break ;;
			*) echo "unexpected review starter status: $server_status" >&2; return 1 ;;
		esac
		((SECONDS < deadline)) || { echo "review starter did not finish creation within $timeout seconds" >&2; return 1; }
		sleep 1
	done
	case "$server_status" in
		SERVER_STATUS_RUNNING|SERVER_STATUS_STARTING|3|2) ;;
		*) rpc /carbonpanel.v1.ServerService/StartServer '{"id":"'${server_id}'","requestId":"review-bootstrap-start"}' "${token}" >/dev/null ;;
	esac

	seed_showcase_pack "${token}"
}

# seed_showcase_pack creates a small fabric pack with real mods so studio and
# library captures show populated state, then adds a few empty packs so the
# library grid reads as a real row of cards rather than one lone pack. Runs
# only on a fresh pack; reruns resolve the existing pack and skip seeding (no
# duplicates). Best-effort: any failure only warns, never fails the boot.
seed_showcase_pack() {
	local token="$1" base="http://127.0.0.1:${PORT}" pack_id mod ver
	pack_id=$(curl -fsS --max-time 20 -X POST "${base}/api/v1/packwiz/packs" \
		-H "Authorization: Bearer ${token}" -H 'Content-Type: application/json' \
		-d '{"name":"Showcase Pack","author":"Carbon Review","version":"1.0.0","mc_version":"1.21.1","mod_loader":"fabric","loader_version":"latest"}' 2>/dev/null |
		node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{console.log(JSON.parse(s).id||"")}catch(e){}})') || true
	if [ -z "${pack_id}" ]; then
		echo "showcase pack already seeded or unavailable; skipping mod seeding" >&2
		return 0
	fi
	for mod in 9s6osm5g mOgUt4GM P7dR8mSH; do
		ver=$(curl -fsS --max-time 30 "${base}/api/v1/servers/none/mods/${mod}/versions?platform=modrinth&loader=fabric&mc_version=1.21.1" \
			-H "Authorization: Bearer ${token}" 2>/dev/null |
			MOD="${mod}" node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const v=(JSON.parse(s).versions||[])[0];if(!v||!v.file_name||!v.download_url)return;console.log(JSON.stringify({slug:String(v.name||process.env.MOD).toLowerCase().replace(/[^a-z0-9]+/g,"-"),name:v.name||v.version_number,file_name:v.file_name,side:"both",platform:"modrinth",project_id:process.env.MOD,version_id:v.id,download_url:v.download_url,file_size:v.file_size||0,pinned:false}))}catch(e){}})') || true
		if [ -n "${ver}" ]; then
			curl -fsS --max-time 30 -X POST "${base}/api/v1/packwiz/packs/${pack_id}/mods" \
				-H "Authorization: Bearer ${token}" -H 'Content-Type: application/json' \
				-d "${ver}" >/dev/null 2>&1 ||
				echo "could not add mod ${mod} to showcase pack" >&2
		fi
	done

	create_extra_pack() {
		curl -fsS --max-time 20 -X POST "${base}/api/v1/packwiz/packs" \
			-H "Authorization: Bearer ${token}" -H 'Content-Type: application/json' \
			-d "{\"name\":\"$1\",\"author\":\"Carbon Review\",\"version\":\"$2\",\"mc_version\":\"$3\",\"mod_loader\":\"$4\",\"loader_version\":\"latest\"}" \
			>/dev/null 2>&1 || echo "could not add showcase pack $1" >&2
	}
	create_extra_pack 'Performance Plus' '2.3.1' '1.20.1' 'fabric'
	create_extra_pack 'Create: Workshop' '1.4.0' '1.20.1' 'forge'
	create_extra_pack 'Vanilla Tweaks' '3.0.2' '1.21.1' 'fabric'
	create_extra_pack 'NeoForge Basics' '1.1.0' '1.21.1' 'neoforge'
	create_extra_pack 'Adventure Kit' '1.2.4' '1.20.1' 'quilt'
}

down() {
	docker info >/dev/null 2>&1 || { echo "Docker is unavailable; refusing teardown" >&2; return 1; }
	local names
	names="$(docker ps -a --format '{{.Names}}')" || { echo "cannot list containers; refusing teardown" >&2; return 1; }
	if printf '%s\n' "$names" | grep -Fxq "$NAME"; then
		assert_owner
		local record id owner
		record="$(docker inspect --format '{{.Id}} {{ index .Config.Labels "carbonpanel.review.owner" }}' "$NAME")"
		id="${record%% *}"; owner="${record#* }"
		[[ "$id" =~ ^[0-9a-f]{64}$ ]] && [ "$owner" = "$DATA" ] || { echo "refusing to stop an unowned container: $NAME" >&2; return 1; }
		docker rm -f "$id" >/dev/null
	fi
}

case "${1:-up}" in
up)
	init_owner
	down
	PORT="$(free_port "${PORT}")"
	mkdir -p "${DATA}/data" "${DATA}/backups" "${DATA}/tmp"
	echo "${PORT}" >"${DATA}/port"
	docker build -q -f "${REPO}/docker/Dockerfile.carbon-panel" \
		-t "${IMAGE}" --build-arg APP_VERSION=review "${REPO}" >/dev/null
	docker run -d --name "${NAME}" \
		--label "carbonpanel.review.owner=${DATA}" \
		--restart unless-stopped \
		-p "127.0.0.1:${PORT}:8080" \
		-v "${DATA}/data:/app/data" \
		-v "${DATA}/backups:/app/backups" \
		-v "${DATA}/tmp:/app/tmp" \
		-v /var/run/docker.sock:/var/run/docker.sock \
		"${IMAGE}" >/dev/null
	wait_for_health
	seed
	echo "review panel: http://127.0.0.1:${PORT}  (data: ${DATA})"
	echo "account: ${USER_NAME} (password from PANEL_REVIEW_PASSWORD or the local default)"
	;;
down)
	down
	echo "stopped ${NAME}"
	;;
clean)
	assert_owner
	down
	# Re-resolve immediately before deletion; never follow a substituted link.
	[ ! -L "$DATA" ] && [ "$(realpath -e "$DATA")" = "$ROOT/$NAME" ] && assert_owner || exit 1
	rm -rf -- "$DATA"
	echo "removed ${NAME} and ${DATA}"
	;;
*)
	sed -n '5,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac

