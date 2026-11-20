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

# json_path reads a dot-separated path (numeric segments index arrays) from a
# Connect JSON response. An absent value prints nothing rather than "undefined".
json_path() {
	node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{let v=JSON.parse(s);for(const k of process.argv[1].split("."))v=v?.[k];if(v===undefined||v===null)return;console.log(typeof v==="object"?JSON.stringify(v):String(v))}catch(e){}})' "$1"
}

login_token() {
	local payload
	payload="$(REVIEW_SEED_USER="$USER_NAME" REVIEW_SEED_PASS="$USER_PASS" node -e 'console.log(JSON.stringify({username:process.env.REVIEW_SEED_USER,password:process.env.REVIEW_SEED_PASS}))')"
	rpc /carbonpanel.v1.AuthService/Login "${payload}" 2>/dev/null | json_path 'token'
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
		# Distinct named servers make the dashboard read as a real fleet. They
		# stay stopped: no extra game process is needed for a populated capture.
		# They are created before the starter so tooling that inspects the last
		# CreateServer request still observes the configured REVIEW_GAME_PORT.
		extra_port=$((GAME_PORT + 1))
		for extra_name in 'review-paper' 'review-creative'; do
			extra_payload="$(REVIEW_EXTRA_NAME="${extra_name}" REVIEW_EXTRA_PORT="${extra_port}" node -e 'console.log(JSON.stringify({name:process.env.REVIEW_EXTRA_NAME,mcVersion:"1.21.1",modLoader:"MOD_LOADER_PAPER",memory:1024,maxPlayers:8,port:Number(process.env.REVIEW_EXTRA_PORT),requestId:"review-bootstrap-"+process.env.REVIEW_EXTRA_NAME}))')"
			rpc /carbonpanel.v1.ServerService/CreateServer "${extra_payload}" "${token}" >/dev/null 2>&1 ||
				echo "could not seed named server ${extra_name}" >&2
			extra_port=$((extra_port + 1))
		done
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
	seed_showcase_world_library "${token}" "${server_id}"
	if wait_for_server_running "${token}"; then
		seed_showcase_player "${token}" "${server_id}"
	else
		echo "review server did not reach RUNNING; skipping player allowlist fixture" >&2
	fi
}

# Seed a real World Library entry, revision and compatibility report through
# production RPCs. Create-from-config stores only generated metadata and empty
# folders; the compatibility verdict is honestly "unknown" until a real world
# with region files is imported, so no compatibility is invented here.
seed_showcase_world_library() {
	local token="$1" server_id="$2" response entry_id revision_id
	response="$(rpc /carbonpanel.v1.WorldService/ListWorldEntries '{}' "${token}" 2>/dev/null || true)"
	entry_id="$(printf '%s' "${response}" | json_path 'entries.0.id')"
	if [ -z "${entry_id}" ]; then
		response="$(rpc /carbonpanel.v1.WorldService/CreateWorldFromConfig \
			'{"name":"Review Fresh World","edition":"java","mcVersion":"1.21.1","modLoader":"MOD_LOADER_VANILLA","dataVersion":"3953","values":{"level-name":"world"},"preset":"survival","licenseAttestation":"isolated review fixture","requestId":"review-bootstrap-world"}' \
			"${token}" 2>/dev/null)" || response=""
		entry_id="$(printf '%s' "${response}" | json_path 'entry.id')"
		revision_id="$(printf '%s' "${response}" | json_path 'revision.id')"
	fi
	if [ -z "${entry_id}" ]; then
		echo "could not seed the review world library entry" >&2
		return 0
	fi
	if [ -z "${revision_id}" ]; then
		response="$(rpc /carbonpanel.v1.WorldService/ListRevisions \
			"{\"worldEntryId\":\"${entry_id}\"}" "${token}" 2>/dev/null || true)"
		revision_id="$(printf '%s' "${response}" | json_path 'revisions.0.id')"
	fi
	if [ -z "${revision_id}" ] || [ -z "${server_id}" ]; then
		echo "could not resolve the review world revision for compatibility" >&2
		return 0
	fi
	rpc /carbonpanel.v1.WorldService/CheckCompatibility \
		"{\"revisionId\":\"${revision_id}\",\"serverId\":\"${server_id}\",\"targetMcVersion\":\"1.21.1\"}" \
		"${token}" >/dev/null 2>&1 ||
		echo "could not seed a review compatibility report" >&2
}

wait_for_server_running() {
	local token="$1" deadline=$((SECONDS + 180)) status response
	while ((SECONDS < deadline)); do
		response="$(rpc /carbonpanel.v1.ServerService/ListServers '{}' "${token}" 2>/dev/null || true)"
		status="$(printf '%s' "${response}" | json_path 'servers.0.status')"
		case "${status}" in
			SERVER_STATUS_RUNNING|3) return 0 ;;
		esac
		sleep 2
	done
	return 1
}

# Add one offline identity to the cross-server roster by manual allowlist add.
# The add goes through the real first-party identity provider; only an
# authoritative NOT_FOUND for this reserved fixture name yields an offline
# identity. If the provider is unavailable the add is skipped with a warning:
# player captures are best-effort and never borrow a real player's profile.
seed_showcase_player() {
	local token="$1" server_id="$2" count payload response query
	response="$(rpc /carbonpanel.v1.PlayerService/ListPlayers '{"serverId":"","query":"","limit":200}' "${token}" 2>/dev/null || true)"
	count="$(printf '%s' "${response}" | list_count players || printf '0')"
	if [ "${count}" -eq 0 ]; then
		# Two reserved fixture names: a transient provider failure must not cost
		# the player captures. Only an authoritative NOT_FOUND creates an
		# offline identity.
		for query in ReviewFixture ReviewFixtureTwo; do
			payload="$(REVIEW_SERVER_ID="${server_id}" REVIEW_PLAYER_QUERY="${query}" node -e 'console.log(JSON.stringify({serverId:process.env.REVIEW_SERVER_ID,identityKey:"",playerName:"",remove:false,requestId:"review-allowlist-"+process.env.REVIEW_PLAYER_QUERY,playerQuery:process.env.REVIEW_PLAYER_QUERY}))')"
			response="$(rpc /carbonpanel.v1.PlayerService/SetWhitelistEntry "${payload}" "${token}" 2>&1 || true)"
			count="$(rpc /carbonpanel.v1.PlayerService/ListPlayers '{"serverId":"","query":"","limit":200}' "${token}" 2>/dev/null | list_count players || printf '0')"
			if [ "${count}" -gt 0 ]; then
				echo "seeded offline review identity with ${query}; roster count ${count}"
				break
			fi
			echo "review allowlist query ${query} produced no roster identity: $(printf '%s' "${response}" | cut -c1-200)" >&2
		done
	fi
	# A public, non-private fixture UUID already used by this repo's own tests
	# and UI placeholders. A successful first-party lookup creates an identity
	# with a PrimaryUUID and a real skinRef, so the canonical detail page can
	# render a loaded face preview. Provider outages leave the offline fixture.
	payload="$(REVIEW_SERVER_ID="${server_id}" node -e 'console.log(JSON.stringify({serverId:process.env.REVIEW_SERVER_ID,identityKey:"",playerName:"",remove:false,requestId:"review-skin-fixture-001",playerQuery:"069a79f4-44e9-4726-a5be-fca90e38aaf5"}))')"
	response="$(rpc /carbonpanel.v1.PlayerService/SetWhitelistEntry "${payload}" "${token}" 2>&1 || true)"
	skin_response="$(rpc /carbonpanel.v1.PlayerService/ListPlayers '{"serverId":"","query":"069a79f4","limit":20}' "${token}" 2>/dev/null)"
	skin_state="$(printf '%s' "${skin_response}" | json_path 'players.0.skinAvailable')"
	skin_key="$(printf '%s' "${skin_response}" | json_path 'players.0.ref.identityKey')"
	if [ -n "${skin_key}" ]; then
		printf '%s\n' "${skin_key}" >"${DATA}/skin-identity-id"
	fi
	echo "review skin fixture skinAvailable: ${skin_state:-false}; add result: $(printf '%s' "${response}" | cut -c1-120)" >&2
	if [ -n "${skin_key}" ]; then
		skin_probe="$(rpc /carbonpanel.v1.PlayerService/GetPlayerSkin "{\"identityKey\":\"${skin_key}\",\"skinRef\":\"${skin_key}\",\"kind\":\"face\",\"serverId\":\"\"}" "${token}" 2>/dev/null)"
		skin_data_len="$(printf '%s' "${skin_probe}" | json_path 'asset.data' | wc -c)"
		skin_type="$(printf '%s' "${skin_probe}" | json_path 'asset.contentType')"
		echo "review skin RPC: dataChars=${skin_data_len} contentType=${skin_type:-none}" >&2
	fi
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

# A real BlueMap renderer for the gated World View capture. Managed install is
# unavailable on this stack because CreateNode mints a UUID node id, so the
# explicit world_view endpoint path is used instead: the renderer container
# shares the panel's loopback namespace and serves genuinely rendered tiles
# from a read-only bind of the seeded vanilla world. All steps are best-effort;
# a failure leaves the core captures untouched.
seed_showcase_bluemap() {
	local token="$1" node_id data_path host_path server_id level="world" mapdir deadline
	server_id="$(rpc /carbonpanel.v1.ServerService/ListServers '{}' "${token}" 2>/dev/null | json_path 'servers.0.id')"
	node_id="$(rpc /carbonpanel.v1.NodeService/ListNodes '{}' "${token}" 2>/dev/null | json_path 'nodes.0.id')"
	data_path="$(rpc /carbonpanel.v1.ServerService/GetServer "{\"id\":\"${server_id}\"}" "${token}" 2>/dev/null | json_path 'server.dataPath')"
	if [ -z "${server_id}" ] || [ -z "${node_id}" ] || [ -z "${data_path}" ]; then
		echo "could not resolve seeded server/node facts for BlueMap; skipping renderer" >&2
		return 0
	fi
	case "${data_path}" in
		/app/data/*) host_path="${DATA}/data/${data_path#/app/data/}" ;;
		*) host_path="${DATA}/data/servers/${server_id}" ;;
	esac
	DATA_PATH_FOR_BLUEMAP="${host_path}"
	echo "review BlueMap world path: ${data_path} -> ${host_path}" >&2
	if [ ! -d "${host_path}/${level}" ]; then
		echo "seeded world ${host_path}/${level} is absent; skipping BlueMap renderer" >&2
		return 0
	fi
	mapdir="${host_path}/modules/bluemap"
	# The panel container owns the server data directory; write the fixture
	# config as root inside that container, then bind the same paths into the
	# renderer container.
	cat >"${DATA}/config/setup-bluemap.sh" <<EOF
#!/bin/sh
set -eu
base="${data_path}/modules/bluemap"
mkdir -p "\${base}/config/maps" "\${base}/data" "\${base}/web"
printf '%s\n' '# Isolated review fixture' 'accept-download: true' 'render-thread-count: 1' 'region-file-check-interval: 5' > "\${base}/config/core.conf"
printf '%s\n' '# Seeded vanilla level, mounted read-only' 'id: "${level}"' 'name: "${level}"' 'world: "/app/world"' 'dimension: "minecraft:overworld"' 'storage: "file"' 'hires: { enabled: true }' 'lowres: { enabled: true }' > "\${base}/config/maps/${level}.conf"
EOF
	docker cp "${DATA}/config/setup-bluemap.sh" "${NAME}:/tmp/setup-bluemap.sh" >/dev/null 2>&1 || return 0
	docker exec -u root "${NAME}" sh /tmp/setup-bluemap.sh >/dev/null 2>&1 || return 0
	if ! start_bluemap "${host_path}" "${mapdir}" "${level}"; then
		echo "could not start the isolated BlueMap renderer; skipping map capture" >&2
		return 0
	fi
	deadline=$((SECONDS + 300))
	stable=0
	prev_count=-1
	while ((SECONDS < deadline)); do
		count=0
		if [ -f "${mapdir}/web/settings.json" ] && [ -d "${mapdir}/web/maps/${level}/tiles" ]; then
			count="$(find "${mapdir}/web/maps/${level}/tiles" -type f 2>/dev/null | wc -l | tr -d ' ')"
		fi
		if [ "${count}" -ge 20 ] && [ "${count}" = "${prev_count}" ]; then
			stable=$((stable + 1))
		else
			stable=0
		fi
		prev_count="${count}"
		if [ "${stable}" -ge 4 ] && [ "${count}" -ge 20 ]; then
			break
		fi
		sleep 3
	done
	if [ ! -f "${mapdir}/web/settings.json" ] ||
		[ -z "$(find "${mapdir}/web/maps/${level}/tiles" -type f -print -quit 2>/dev/null)" ]; then
		echo "BlueMap did not produce real tiles/settings.json within 300s; map capture will be unavailable" >&2
		return 0
	fi
	echo "review BlueMap tile files before panel restart: ${prev_count}" >&2
	mkdir -p "${DATA}/config"
	cat >"${DATA}/config/config.yaml" <<EOF
# Isolated review fixture: explicit node-local renderer endpoint.
world_view:
  endpoints:
    - node_id: "${node_id}"
      server_id: "${server_id}"
      kind: "bluemap"
      host: "127.0.0.1"
      port: 8100
      base_path: "/"
      health_path: "/"
      player_feed: false
EOF
	map_count="$(node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const m=JSON.parse(s).maps;console.log(Array.isArray(m)?m.length:0)}catch(e){console.log(0)}})' <"${mapdir}/web/settings.json" 2>/dev/null || printf '0')"
	echo "review BlueMap map count: ${map_count}" >&2
	echo "seeded real BlueMap tiles for ${server_id} on ${node_id}"
}

# Review sidecars are disposable but still containers on the host daemon.
# Never remove a name: inspect, verify the immutable ID and ownership label,
# then remove exactly that ID. A colliding unowned container is a hard refusal.
owned_sidecar_id() {
	local name="${1:-${NAME}-bluemap}" record id owner
	record="$(docker inspect --format '{{.Id}} {{ index .Config.Labels "carbonpanel.review.owner" }}' "$name" 2>/dev/null)" || return 1
	id="${record%% *}"; owner="${record#* }"
	if [[ ! "$id" =~ ^[0-9a-f]{64}$ ]]; then
		echo "refusing sidecar operation: ${name} has no immutable container id" >&2
		return 1
	fi
	if [ "$owner" != "$DATA" ]; then
		echo "refusing sidecar operation: ${name} is not owned by this review session" >&2
		return 1
	fi
	printf '%s' "$id"
}

sidecar_exists() {
	local name="${1:-${NAME}-bluemap}"
	docker ps -a --format '{{.Names}}' 2>/dev/null | grep -Fxq "$name"
}

remove_owned_sidecar() {
	local name="${1:-${NAME}-bluemap}"
	if ! sidecar_exists "$name"; then
		return 0
	fi
	assert_owner || return 1
	local id
	id="$(owned_sidecar_id "$name")" || return 1
	docker rm -f "$id" >/dev/null
}

# overlay_csp_safe_webapp replaces only the browser bundle in the generated
# BlueMap web root with the pinned CSP-safe build (ADR 0048). The pristine
# upstream bundle calls new Function() during locale translation; the panel's
# unchanged CSP blocks it and the map fails to load after the first tile. Real
# settings.json and real rendered maps/ tiles are never touched. Fail closed on
# digest drift so a stale or substituted bundle cannot qualify a capture.
overlay_csp_safe_webapp() {
	local mapdir="$1" fixture="${REPO}/scripts/review/fixtures/bluemap-webapp-jit"
	local web="${mapdir}/web" i
	[ -f "${fixture}/manifest.sha256" ] || {
		echo "review BlueMap CSP-safe fixture is absent; cannot qualify map capture" >&2
		return 1
	}
	# BlueMap writes its own web root when the renderer starts. Wait for it
	# before overlaying so the pinned files are the ones actually served.
	for ((i = 0; i < 60; i++)); do
		[ -f "${web}/index.html" ] && break
		sleep 1
	done
	if [ ! -f "${web}/index.html" ]; then
		echo "review BlueMap web root was not created; cannot overlay CSP-safe bundle" >&2
		return 1
	fi
	(cd "${fixture}" && sha256sum -c manifest.sha256 >/dev/null 2>&1) || {
		echo "review BlueMap CSP-safe bundle digest drift; refusing map capture" >&2
		return 1
	}
	# BlueMap owns the mounted web root inside its container. Copy through
	# docker cp (not the host filesystem) so the runner does not need write
	# permission on container-owned files; only the owned sidecar is touched.
	docker cp "${fixture}/index.html" "${NAME}-bluemap:/app/web/index.html" >/dev/null || return 1
	docker cp "${fixture}/assets/." "${NAME}-bluemap:/app/web/assets/" >/dev/null || return 1
	docker cp "${fixture}/lang/." "${NAME}-bluemap:/app/web/lang/" >/dev/null || return 1
	local digest
	digest="$(docker exec "${NAME}-bluemap" sha256sum /app/web/assets/index-BA9KJ4pi.js 2>/dev/null | cut -d' ' -f1)"
	echo "review BlueMap served webapp bundle: ${digest}" >&2
	[ "${digest}" = "ba940f930de0642acfd2ca8b0eda6aa0914ce02fcf673cee18262b3dd5217da3" ] || return 1
	set_bluemap_start_location "${mapdir}"
}

# BlueMap's default camera (distance 1500) shows the tiny seeded spawn area as
# a few pixels in a black void. Start the map zoomed into the real generated
# tiles using BlueMap's own startLocation setting: only the initial camera
# changes, never the real settings.json map list or rendered tile tree. The
# renderer rewrites settings.json while rendering, so apply this last, after
# the tile-coverage wait and immediately before the journey.
set_bluemap_start_location() {
	local mapdir="$1" web="${1}/web"
	local start_location="${REVIEW_BLUEMAP_START_LOCATION:-world:0:0:0:15:0:0:0:0:perspective}"
	local settings_tmp="${DATA}/config/bluemap-settings.json"
	[ -f "${web}/settings.json" ] || return 1
	START_LOCATION="${start_location}" node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const j=JSON.parse(s);j.startLocation=process.env.START_LOCATION;process.stdout.write(JSON.stringify(j))}catch(e){process.exit(1)}})' \
		<"${web}/settings.json" >"${settings_tmp}" || return 1
	docker cp "${settings_tmp}" "${NAME}-bluemap:/app/web/settings.json" >/dev/null || return 1
	local root_keys
	root_keys="$(node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{console.log(Object.keys(JSON.parse(s)).sort().join(","))}catch(e){console.log("parse_error")}})' <"${web}/settings.json")"
	echo "review BlueMap root settings keys: ${root_keys}" >&2
	echo "review BlueMap startLocation: ${start_location}" >&2
}

# Real BlueMap renderer and web server for the seeded world. BlueMap's own
# webserver serves hires/lowres tiles, settings and web assets correctly for the
# generated web root, so it must be the HTTP origin the panel probes and proxies.
# It shares the panel's network namespace to be reachable at 127.0.0.1:8100.
# Because network namespace sharing is tied to the panel container, this sidecar
# must be recreated after the panel container is recreated.
start_bluemap() {
	local host_path="$1" mapdir="$2" level="$3"
	remove_owned_sidecar "${NAME}-bluemap" || return 1
	docker run -d --name "${NAME}-bluemap" --network "container:${NAME}" \
		--label "carbonpanel.review.owner=${DATA}" \
		-v "${mapdir}/config:/app/config" \
		-v "${host_path}/${level}:/app/world:ro" \
		-v "${mapdir}/data:/app/data" \
		-v "${mapdir}/web:/app/web" \
		ghcr.io/bluemap-minecraft/bluemap:v5.29 -r -u -w >/dev/null 2>&1 || return 1
	overlay_csp_safe_webapp "${mapdir}" || return 1
}

start_panel() {
	docker run -d --name "${NAME}" \
		--label "carbonpanel.review.owner=${DATA}" \
		--restart unless-stopped \
		-p "127.0.0.1:${PORT}:8080" \
		-v "${DATA}/data:/app/data" \
		-v "${DATA}/backups:/app/backups" \
		-v "${DATA}/tmp:/app/tmp" \
		-v "${DATA}/config/config.yaml:/etc/carbon-panel/config.yaml:ro" \
		-v /var/run/docker.sock:/var/run/docker.sock \
		"${IMAGE}" >/dev/null
}

down() {
	docker info >/dev/null 2>&1 || { echo "Docker is unavailable; refusing teardown" >&2; return 1; }
	local names
	names="$(docker ps -a --format '{{.Names}}')" || { echo "cannot list containers; refusing teardown" >&2; return 1; }
	for sidecar in "${NAME}-bluemap" "${NAME}-bluemap-render"; do
		if printf '%s\n' "$names" | grep -Fxq "$sidecar"; then
			remove_owned_sidecar "$sidecar" || return 1
		fi
	done
	if printf '%s\n' "$names" | grep -Fxq "$NAME"; then
		assert_owner
		local record id owner
		record="$(docker inspect --format '{{.Id}} {{ index .Config.Labels "carbonpanel.review.owner" }}' "$NAME")"
		id="${record%% *}"; owner="${record#* }"
		[[ "$id" =~ ^[0-9a-f]{64}$ ]] && [ "$owner" = "$DATA" ] || { echo "refusing to stop an unowned container: $NAME" >&2; return 1; }
		docker rm -f "$id" >/dev/null
	fi
}

# Unit tests source this file with a fake docker on PATH to exercise the
# ownership guards without touching a daemon or running the up/down dispatch.
if [ "${REVIEW_DEVSTACK_SOURCE_ONLY:-}" = "1" ]; then
	return 0 2>/dev/null || exit 0
fi

case "${1:-up}" in
up)
	init_owner
	down
	PORT="$(free_port "${PORT}")"
	mkdir -p "${DATA}/data" "${DATA}/backups" "${DATA}/tmp" "${DATA}/config"
	echo "${PORT}" >"${DATA}/port"
	[ -f "${DATA}/config/config.yaml" ] || printf '{}\n' >"${DATA}/config/config.yaml"
	docker build -q -f "${REPO}/docker/Dockerfile.carbon-panel" \
		-t "${IMAGE}" --build-arg APP_VERSION=review "${REPO}" >/dev/null
	start_panel
	wait_for_health
	seed
	token="$(login_token)"
	rpc /carbonpanel.v1.ServerService/ListServers '{}' "${token}" 2>/dev/null |
		json_path 'servers.0.id' >"${DATA}/server-id" || true
	if [ -n "${token}" ]; then
		seed_showcase_bluemap "${token}" || true
		if [ -f "${DATA}/config/config.yaml" ] && grep -q 'world_view:' "${DATA}/config/config.yaml"; then
			# Restart the panel so the explicit renderer endpoint is loaded.
			docker rm -f "${NAME}" >/dev/null
			start_panel
			wait_for_health
			# BlueMap shares the panel's network namespace, so recreating the panel
			# drops that namespace. Restart BlueMap on the new panel container; it
			# serves the same real generated web root and rendered tiles from disk.
			server_path="${DATA_PATH_FOR_BLUEMAP:-}"
			if [ -n "${server_path}" ]; then
				if start_bluemap "${server_path}" "${server_path}/modules/bluemap" "${REVIEW_BLUEMAP_LEVEL:-world}"; then
					# BlueMap startup/rendering and the panel restart are asynchronous.
					# Only a semantic readiness signal may unblock the worlds
					# capture. Match the SEEDED server, never a positional row:
					# another target in the list must not mask this fixture.
					ready_deadline=$((SECONDS + 120))
					renderer_state=""
					fixture_server_id="$(cat "${DATA}/server-id" 2>/dev/null || true)"
					if [ -z "${fixture_server_id}" ]; then
						echo "review fixture server-id is empty; cannot scope renderer readiness" >&2
						return 0
					fi
					# Status-only evidence: the target id field and count actually
					# returned by the backend. No response bodies or secrets.
					first_probe="$(rpc /carbonpanel.v1.WorldViewService/ListWorldViewTargets '{}' "${token}" 2>/dev/null || true)"
					echo "review world-view target count: $(printf '%s' "${first_probe}" | list_count targets)" >&2
					echo "review world-view target keys: $(printf '%s' "${first_probe}" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const t=JSON.parse(s).targets||[];if(t[0])console.log(Object.keys(t[0]).sort().join(","))}catch(e){}})')" >&2
					echo "review world-view target server ids: $(printf '%s' "${first_probe}" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const t=JSON.parse(s).targets||[];console.log(t.map(x=>x.serverId||x.server_id||"none").join(","))}catch(e){}})')" >&2
					echo "review fixture server id: ${fixture_server_id}" >&2
					fixture_target=""
					while ((SECONDS < ready_deadline)); do
						# The panel's own health check performs the authoritative
						# upstream probe (map list, keyed root, gzip assets and
						# CSP-safe bundle). READY is the single semantic signal.
						probe="$(rpc /carbonpanel.v1.WorldViewService/ListWorldViewTargets '{}' "${token}" 2>/dev/null || true)"
						fixture_target="$(printf '%s' "${probe}" | REVIEW_SERVER_ID="${fixture_server_id}" node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const t=(JSON.parse(s).targets||[]).find(x=>x.serverId===process.env.REVIEW_SERVER_ID);if(t)console.log(JSON.stringify(t))}catch(e){}})')"
						renderer_state="$(printf '%s' "${fixture_target}" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const t=JSON.parse(s);console.log(t.rendererState||"")}catch(e){}})')"
						if [ -n "${fixture_server_id}" ] && [ "${renderer_state}" = "RENDERER_STATE_READY" ]; then
							break
						fi
						renderer_state="${renderer_state:-}"
						sleep 1
					done
					echo "review renderer state: ${renderer_state:-unknown}" >&2
					if [ -n "${fixture_target}" ]; then
						echo "review fixture target fields: $(printf '%s' "${fixture_target}" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const t=JSON.parse(s);console.log(JSON.stringify({state:t.rendererState,error:t.error,freshness:t.freshness,rendererType:t.rendererType,name:t.name}))}catch(e){console.log("parse_error")}})' | cut -c1-800)" >&2
					fi
					if [ "${renderer_state}" != "RENDERER_STATE_READY" ]; then
						map_probe="$(curl -fsS --max-time 3 'http://127.0.0.1:8100/settings.json' 2>&1 || true)"
						echo "review map settings maps: $(printf '%s' "${map_probe}" | tr '\n' ' ' | cut -c1-260)" >&2
						tex_probe="$(find "${server_path}/modules/bluemap/web/maps" -maxdepth 3 -name 'textures.json*' 2>/dev/null | head -n 5 | tr '\n' ' ' | cut -c1-260)"
						echo "review textures files: ${tex_probe}" >&2
						http_probe="$(curl -sS --max-time 3 -D - -o /dev/null 'http://127.0.0.1:8100/maps/world/textures.json.gz' 2>&1 || true)"
						echo "review textures http: $(printf '%s' "${http_probe}" | tr '\n' ' ' | cut -c1-300)" >&2
						echo "review renderer never became READY; worlds capture will fail closed" >&2
					else
						# READY means textures/settings are valid, but the viewport may
						# still request lowres tiles the renderer has not produced yet.
						# Let the real renderer continue filling the shared web root.
						tile_deadline=$((SECONDS + 120))
						tile_stable=0
						tile_prev=-1
						while ((SECONDS < tile_deadline)); do
							tile_count="$(find "${server_path}/modules/bluemap/web/maps" -path '*/tiles/*' -type f 2>/dev/null | wc -l | tr -d ' ')"
							tile_count="${tile_count:-0}"
							if [ "${tile_count}" -ge 60 ] && [ "${tile_count}" = "${tile_prev}" ]; then
								tile_stable=$((tile_stable + 1))
							else
								tile_stable=0
							fi
							tile_prev="${tile_count}"
							if [ "${tile_stable}" -ge 4 ] && [ "${tile_count}" -ge 60 ]; then
								break
							fi
							sleep 3
						done
						echo "review BlueMap tile files before journey: ${tile_prev}" >&2
						tile_sample="$(find "${server_path}/modules/bluemap/web/maps" -path '*/tiles/*' -type f 2>/dev/null | sort | head -12 | tr '\n' ' ' | cut -c1-500)"
						echo "review BlueMap tile sample: ${tile_sample}" >&2
						tile_lods="$(find "${server_path}/modules/bluemap/web/maps" -path '*/tiles/*' -type f 2>/dev/null | sed -n 's#.*maps/[^/]*/tiles/\([^/]*\)/.*#\1#p' | sort -u | tr '\n' ' ' | cut -c1-120)"
						echo "review BlueMap tile lods: ${tile_lods}" >&2
						settings_info="$(BLUEMAP_WEB="${server_path}/modules/bluemap/web" python3 -c "
import json,os
try:
 web=os.environ['BLUEMAP_WEB']
 d=json.load(open(os.path.join(web,'settings.json')))
 m=d.get('maps')
 print('maps='+str(m)[:300])
 if isinstance(m,list) and m:
  mid=m[0] if isinstance(m[0],str) else str(m[0])
  sp=os.path.join(web,'maps',mid,'settings.json')
  if os.path.exists(sp):
   ms=json.load(open(sp))
   print('map_keys='+str(sorted(ms.keys()))[:300])
   for k in ('hires','lowres','views','startPos','sorting'):
    if k in ms: print(k+'='+str(ms[k])[:180])
except Exception as e:
 print('settings_err='+type(e).__name__)
" 2>&1 || true)"
						echo "review BlueMap settings: $(printf '%s' "${settings_info}" | tr '\n' ' ' | cut -c1-700)" >&2
						# Apply last: the renderer rewrites settings.json as tiles
						# land, so an earlier overlay is silently lost by now.
						set_bluemap_start_location "${server_path}/modules/bluemap" || {
							echo "review camera startLocation could not be applied; capture would stay black" >&2
						}
					fi
				else
					echo "review map server container failed to start" >&2
				fi
			fi
		fi
	fi
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

