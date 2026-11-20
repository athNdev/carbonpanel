#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# Exercises shipped Compose entry points on a disposable GitHub-hosted runner.
set -euo pipefail

work=$(realpath "$1")
product=$(realpath "$2")
mode=$3
: "${BASELINE_IMAGE:?}" "${BASELINE_COMMIT:?}" "${CANDIDATE_IMAGE:?}" "${STAGED_SHA:?}"
[[ "$GITHUB_ACTIONS" == true && "$RUNNER_ENVIRONMENT" == github-hosted ]]
[[ "$work" == "$RUNNER_TEMP/"* && "$mode" =~ ^(host|bridge)$ ]]
[[ "$CANDIDATE_IMAGE" =~ ^127\.0\.0\.1:5000/carbonpanel@sha256:[0-9a-f]{64}$ ]]
[[ "$BASELINE_IMAGE" =~ ^ghcr\.io/[a-z0-9._/-]+@sha256:[0-9a-f]{64}$ ]]
[[ "$(git -C "$product" rev-parse HEAD)" == "$STAGED_SHA" ]]

export COMPOSE_PROJECT_NAME=compose-release-qualification
compose_file="$work/compose.json"
base_config="$work/shipped-compose.json"
out="$work/evidence"
mkdir -p "$out"
exec > >(tee -a "$out/compose-$mode.txt") 2>&1
docker compose version
docker version --format '{{.Server.Version}}'
docker compose -f "$product/docker-compose.yml" config --format json > "$base_config"
fault="$work/snapshot-full"
registry_pid=

compose() {
  docker compose -f "$compose_file" "$@"
}

cleanup() {
  if [[ -n "$registry_pid" ]]; then kill "$registry_pid" || true; fi
  if mountpoint -q "$fault/data"; then sudo umount "$fault/data"; fi
  if mountpoint -q "$fault"; then sudo umount "$fault"; fi
  if [[ -f "$compose_file" ]]; then compose down --timeout 20 || true; fi
}
trap cleanup EXIT

render() {
  local image=$1 state=$2
  mkdir -p "$state"/{data,backups,tmp}
  # Preserve the shipped service and Docker socket. Only relocate the bind
  # mounts, select the pinned image and make the documented network switch.
  jq -e --arg image "$image" --arg state "$state" --arg mode "$mode" '
    select((.services | keys) == ["carbon-panel"] and .services["carbon-panel"].network_mode == "host") |
    .services["carbon-panel"] |= (
      .image = $image |
      .container_name = "compose-qualification-panel" |
      .volumes |= map(if .target == "/app/data" then .source = ($state + "/data")
        elif .target == "/app/backups" then .source = ($state + "/backups")
        elif .target == "/app/tmp" then .source = ($state + "/tmp")
        else . end) |
      .environment.CARBONPANEL_SERVER_PORT = "18080" |
      .environment.CARBONPANEL_PROXY_ENABLED = "false" |
      .environment.CARBONPANEL_DOCKER_NETWORK_NAME = "compose-qualification-managed" |
      if $mode == "bridge" then
        del(.network_mode) | .environment.CARBONPANEL_SERVER_HOST = "0.0.0.0" |
        .ports = [{"target":18080,"published":"18080","host_ip":"127.0.0.1","protocol":"tcp"}]
      else .environment.CARBONPANEL_SERVER_HOST = "127.0.0.1" end
    )' "$base_config" > "$compose_file"
}

inspect_panel() {
  local revision=$1
  local id
  id=$(compose ps -q carbon-panel)
  docker inspect "$id" | jq -e --arg sha "$revision" '
    .[0] | select(.State.Running and .Config.Labels."org.opencontainers.image.revision" == $sha) |
    {id: .Id, image: .Image, ref: .Config.Image, revision: $sha}'
}

privileges() {
  local id=$1
  docker inspect "$id" | jq -e '
    .[0] | select(.HostConfig.Privileged == false) |
    {user: .Config.User, privileged: .HostConfig.Privileged,
     cap_add: .HostConfig.CapAdd, cap_drop: .HostConfig.CapDrop,
     security: .HostConfig.SecurityOpt, devices: .HostConfig.Devices,
     readonly_rootfs: .HostConfig.ReadonlyRootfs, network: .HostConfig.NetworkMode,
     mounts: ([.Mounts[] | {Source, Destination, Type, RW}] | sort_by(.Destination))}'
}

assert_panel_mounts() {
  jq -e --arg state "$state" '
    (.mounts | map(.Destination) | sort) == ["/app/backups","/app/data","/app/tmp","/var/run/docker.sock"] and
    ([.mounts[] | select(.Type == "bind" and .RW)] | length) == 4 and
    ([.mounts[] | select(.Source == ($state + "/data") and .Destination == "/app/data")] | length) == 1 and
    (.cap_add == null or .cap_add == []) and (.devices == null or .devices == [])' "$1" >/dev/null
}

game_script=$(dirname "$0")/qualify-compose-game.mjs
echo "=== Clean candidate Compose install and Minecraft join ($mode) ==="
state="$work/clean-candidate"
render "$CANDIDATE_IMAGE" "$state"
compose up -d --pull always
node "$game_script" create "$state/game.json"
inspect_panel "$STAGED_SHA" | tee "$out/panel-clean.json"
node "$game_script" stop "$state/game.json"
docker rm "$(cat "$state/game.json.container")"
compose down --timeout 20

echo "=== Prior stable with a real world and controller database ==="
state="$work/upgrade"
render "$BASELINE_IMAGE" "$state"
compose up -d --pull always
node "$game_script" create "$state/game.json"
inspect_panel "$BASELINE_COMMIT" > "$out/panel-before.json"
before_id=$(jq -r '.id' "$out/panel-before.json")
game_id=$(cat "$state/game.json.container")
privileges "$before_id" > "$out/panel-privileges-before.json"
assert_panel_mounts "$out/panel-privileges-before.json"
privileges "$game_id" > "$out/game-privileges-before.json"
docker inspect "$game_id" --format 'game_image={{.Image}} game_reference={{.Config.Image}}'
jq -e --arg path "$state/data/servers/" '
  ([.mounts[] | select(.Destination == "/data" and (.Source | startswith($path)) and .Type == "bind" and .RW)] | length) == 1 and
  ([.mounts[] | select(.Destination == "/var/run/docker.sock")] | length) == 0' "$out/game-privileges-before.json" >/dev/null
sqlite3 "$state/data/carbon-panel.db" 'PRAGMA quick_check;' | grep -x ok

upgrade() {
  bash "$product/scripts/compose-upgrade.sh" --compose "$compose_file" --data-dir "$1" --apply
}

echo "=== Real ENOSPC before image recreation, game stays joinable ==="
render "$CANDIDATE_IMAGE" "$state"
mkdir "$fault"
sudo mount -t tmpfs -o size=1M,mode=0777 compose-qualification-full "$fault"
mkdir "$fault/data"
sudo mount --bind "$state/data" "$fault/data"
if dd if=/dev/zero of="$fault/fill" bs=64K count=32 status=none > "$out/full-volume.txt" 2>&1; then
  echo "failed to fill bounded qualification tmpfs" >&2
  exit 1
fi
grep -i 'no space left on device' "$out/full-volume.txt"
if upgrade "$fault/data" > "$out/snapshot-enospc.txt" 2>&1; then
  echo "upgrade unexpectedly succeeded with a full snapshot destination" >&2
  exit 1
fi
cat "$out/snapshot-enospc.txt"
grep -i 'database or disk is full' "$out/snapshot-enospc.txt"
grep -F 'not running compose up' "$out/snapshot-enospc.txt"
test "$(compose ps -q carbon-panel)" = "$before_id"
sudo umount "$fault/data"
sudo umount "$fault"
node "$game_script" probe "$state/game.json"

echo "=== Interrupt the operator command during its pull ==="
# The registry accepts the loopback connection and leaves it pending. TERM
# stops the actual upgrade operator command while the old service is live.
python3 -u - "$work/pull-ready" > "$out/pull-interrupted-registry.txt" <<'PY' &
from pathlib import Path
import socket
import sys
import time

with socket.socket() as listener:
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind(("127.0.0.1", 5001))
    listener.listen()
    Path(sys.argv[1]).touch()
    connection, _ = listener.accept()
    with connection:
        connection.recv(4096)
        print("Loopback registry received interrupted pull request", flush=True)
        time.sleep(60)
PY
registry_pid=$!
for ((i=0; i<15; i++)); do
  [[ ! -f "$work/pull-ready" ]] || break
  sleep 1
done
test -f "$work/pull-ready"
render '127.0.0.1:5001/carbonpanel:interrupted' "$state"
set +e
timeout -k 4s 12s bash "$product/scripts/compose-upgrade.sh" \
  --compose "$compose_file" --data-dir "$state/data" --apply > "$out/pull-interrupted.txt" 2>&1
interrupt_status=$?
set -e
cat "$out/pull-interrupted.txt"
test "$interrupt_status" = 124
grep -F 'Wrote controller database snapshot:' "$out/pull-interrupted.txt"
grep -F 'received interrupted pull request' "$out/pull-interrupted-registry.txt"
test "$(compose ps -q carbon-panel)" = "$before_id"
kill "$registry_pid"
wait "$registry_pid" || true
registry_pid=
node "$game_script" probe "$state/game.json"

echo "=== Upgrade to exact staged payload and rejoin ==="
render "$CANDIDATE_IMAGE" "$state"
bash "$product/scripts/compose-upgrade.sh" --compose "$compose_file" --data-dir "$state/data"
upgrade "$state/data" | tee "$out/upgrade.txt"
snapshot=$(sed -n 's/^Wrote controller database snapshot: //p' "$out/upgrade.txt")
test -f "$snapshot"
sqlite3 "$snapshot" 'PRAGMA integrity_check;' | grep -x ok
sha256sum "$snapshot" | tee "$out/snapshot-sha256.txt"
node "$game_script" post-upgrade "$state/game.json"
node "$game_script" post-change "$state/game.json"
inspect_panel "$STAGED_SHA" > "$out/panel-after.json"
after_id=$(jq -r '.id' "$out/panel-after.json")
test "$after_id" != "$before_id"
test "$(jq -r '.image' "$out/panel-after.json")" != "$(jq -r '.image' "$out/panel-before.json")"
privileges "$after_id" > "$out/panel-privileges-after.json"
cmp "$out/panel-privileges-before.json" "$out/panel-privileges-after.json"
test "$(cat "$state/game.json.container")" = "$game_id"
privileges "$game_id" > "$out/game-privileges-after.json"
cmp "$out/game-privileges-before.json" "$out/game-privileges-after.json"
echo "Panel and game effective Docker privileges and mounts unchanged"

echo "=== Copy-back and rejoin through the prior stable image ==="
compose stop carbon-panel
# The Linux image owns the controller database through a root-owned bind mount.
sudo bash "$product/scripts/compose-upgrade-rollback.sh" --compose "$compose_file" \
  --data-dir "$state/data" --snapshot "$snapshot" --apply
cmp "$snapshot" "$state/data/carbon-panel.db"
render "$BASELINE_IMAGE" "$state"
compose up -d --pull always
node "$game_script" restart "$state/game.json"
inspect_panel "$BASELINE_COMMIT" > "$out/panel-rollback.json"
test "$(jq -r '.image' "$out/panel-rollback.json")" = "$(jq -r '.image' "$out/panel-before.json")"
node "$game_script" stop "$state/game.json"
echo "PASS: $mode clean install, ENOSPC refusal, interrupted pull, staged upgrade, privilege comparison, rollback, restart and join"
