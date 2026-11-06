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
# is written to "<data dir>.port" so other scripts do not guess.

set -euo pipefail

NAME="${REVIEW_STACK_NAME:-carbon-panel-review}"
PORT="${REVIEW_PORT:-8099}"
DATA="${REVIEW_DATA_DIR:-${HOME}/.cache/carbon-panel-review}"
IMAGE="${REVIEW_IMAGE:-carbon-panel:review}"
USER_NAME="${PANEL_REVIEW_USER:-reviewer}"
USER_PASS="${PANEL_REVIEW_PASSWORD:-Review-Panel-2026}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

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
		if curl -fsS -o /dev/null "http://127.0.0.1:${PORT}/healthz"; then
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

# seed makes a fresh stack usable: the first admin account, a local Docker node
# and a stopped starter server. Every step is safe to repeat.
seed() {
	rpc /carbonpanel.v1.AuthService/Register \
		"{\"username\":\"${USER_NAME}\",\"password\":\"${USER_PASS}\"}" >/dev/null 2>&1 || true

	local token
	token=$(rpc /carbonpanel.v1.AuthService/Login \
		"{\"username\":\"${USER_NAME}\",\"password\":\"${USER_PASS}\"}" |
		sed 's/.*"token":"\([^"]*\)".*/\1/')
	if [ -z "${token}" ]; then
		echo "could not sign in as ${USER_NAME}; create the account by hand" >&2
		return 0
	fi

	# Without a node the panel has nowhere to place a server and every create
	# fails with "no reachable Docker node has capacity".
	if ! rpc /carbonpanel.v1.NodeService/ListNodes '{}' "${token}" | grep -q '"nodes"'; then
		rpc /carbonpanel.v1.NodeService/CreateNode \
			'{"name":"review-local","host":"unix:///var/run/docker.sock","enabled":true,"maxMemoryMb":16384,"maxServers":20}' \
			"${token}" >/dev/null 2>&1 || true
	fi

	if ! rpc /carbonpanel.v1.ServerService/ListServers '{}' "${token}" | grep -q '"servers"'; then
		rpc /carbonpanel.v1.ServerService/CreateServer \
			'{"name":"review-vanilla","mcVersion":"1.21.1","modLoader":"MOD_LOADER_VANILLA","memory":2048,"maxPlayers":10,"port":25565,"requestId":"review-bootstrap-server"}' \
			"${token}" >/dev/null 2>&1 ||
			echo "could not create the starter server; create one in the UI" >&2
	fi
	# Boot the starter so captures show running telemetry. Idempotent:
	# starting a running server is refused and ignored.
	server_id=$(rpc /carbonpanel.v1.ServerService/ListServers '{}' "${token}" 2>/dev/null | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const l=JSON.parse(s);const arr=l.servers||[];console.log(((Array.isArray(arr)?arr:[])[0]||{}).id||"")}catch(e){}})')
	if [ -n "${server_id}" ]; then
		rpc /carbonpanel.v1.ServerService/StartServer '{"id":"'${server_id}'","requestId":"review-bootstrap-start"}' "${token}" >/dev/null 2>&1 || true
	fi

	seed_showcase_pack "${token}"
}

# seed_showcase_pack creates a small fabric pack with real mods so studio and
# library captures show populated state. Runs only on a fresh pack; reruns
# resolve the existing pack and skip mod seeding (no duplicates). Best-effort:
# any failure only warns, never fails the boot.
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
}

down() {
	docker rm -f "${NAME}" >/dev/null 2>&1 || true
}

case "${1:-up}" in
up)
	down
	PORT="$(free_port "${PORT}")"
	mkdir -p "${DATA}/data" "${DATA}/backups" "${DATA}/tmp"
	echo "${PORT}" >"${DATA}.port"
	docker build -q -f "${REPO}/docker/Dockerfile.carbon-panel" \
		-t "${IMAGE}" --build-arg APP_VERSION=review "${REPO}" >/dev/null
	docker run -d --name "${NAME}" \
		--restart unless-stopped \
		-p "${PORT}:8080" \
		-v "${DATA}/data:/app/data" \
		-v "${DATA}/backups:/app/backups" \
		-v "${DATA}/tmp:/app/tmp" \
		-v /var/run/docker.sock:/var/run/docker.sock \
		"${IMAGE}" >/dev/null
	wait_for_health
	seed
	echo "review panel: http://127.0.0.1:${PORT}  (data: ${DATA})"
	echo "account: ${USER_NAME} / ${USER_PASS}"
	;;
down)
	down
	echo "stopped ${NAME}"
	;;
clean)
	down
	rm -rf "${DATA}" "${DATA}.port"
	echo "removed ${NAME} and ${DATA}"
	;;
*)
	sed -n '5,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac

