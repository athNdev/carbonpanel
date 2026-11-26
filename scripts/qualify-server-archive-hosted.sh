#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# Hosted-job wrapper for the disposable server-portability acceptance rehearsal.
#
# Everything this script creates lives in the caller-owned workspace under
# $RUNNER_TEMP: an exact-head controller image/container, a second privileged
# Docker-in-Docker engine with a private storage mount, and the evidence tree.
# It refuses to run outside a GitHub-hosted job so it can never adopt a
# developer workstation, a live panel or another checkout.
set -euo pipefail

work=${1:?usage: qualify-server-archive-hosted.sh <disposable-workspace>}
: "${STAGED_SHA:?}" "${CANDIDATE_VERSION:?}"
: "${GITHUB_ACTIONS:?}" "${RUNNER_TEMP:?}"
[[ "$GITHUB_ACTIONS" == true && "$RUNNER_ENVIRONMENT" == github-hosted ]]
work=$(realpath -m "$work")
[[ "$work" == "$RUNNER_TEMP/"* ]]
[[ "$STAGED_SHA" =~ ^[0-9a-f]{40}$ ]]
[[ "$(git rev-parse HEAD)" == "$STAGED_SHA" ]]
[[ -z "$(git status --porcelain --untracked-files=no)" ]]

controller_port=${CONTROLLER_PORT:-18080}
controller="server-archive-controller-$$"
remote_engine="server-archive-node-$$"
image="server-archive-rehearsal:$STAGED_SHA"
evidence="$work/evidence"
mkdir -p "$work"/{data,backups,tmp,remote-storage,image/linux_amd64} "$evidence"

cleanup() {
  status=$?
  set +e
  docker logs "$controller" > "$evidence/controller.log" 2>&1
  docker logs "$remote_engine" > "$evidence/dind.log" 2>&1
  docker inspect "$controller" > "$evidence/controller-inspect.json" 2>/dev/null
  docker rm -f "$controller" "$remote_engine" > "$evidence/cleanup.txt" 2>&1
  if [[ -f "$work/server-archive-rehearsal.json" ]]; then
    cp "$work/server-archive-rehearsal.json" "$evidence/"
  fi
  exit "$status"
}
trap cleanup EXIT

echo "=== Build the exact-head candidate and controller image ==="
mkdir -p web/carbon-panel/build
touch web/carbon-panel/build/.gitkeep
export CGO_ENABLED=0
ldflags="-s -w \
  -X github.com/athNdev/carbon-panel/internal/buildinfo.Version=$CANDIDATE_VERSION \
  -X github.com/athNdev/carbon-panel/internal/buildinfo.Commit=$STAGED_SHA \
  -X github.com/athNdev/carbon-panel/internal/buildinfo.Date=disposable-rehearsal"
go build -trimpath -buildvcs=false -ldflags "$ldflags" \
  -o "$work/image/linux_amd64/carbon-panel" ./cmd/carbon-panel
go build -trimpath -buildvcs=false -ldflags "$ldflags" \
  -o "$work/image/linux_amd64/carbon-storage" ./cmd/carbon-storage
"$work/image/linux_amd64/carbon-panel" --version | tee "$evidence/candidate-version.txt"
grep -F "$STAGED_SHA" "$evidence/candidate-version.txt"
cp docker/Dockerfile.release "$work/image/Dockerfile.release"
cp config.example.yaml "$work/image/config.example.yaml"
docker build --platform linux/amd64 --progress plain \
  --label "org.opencontainers.image.revision=$STAGED_SHA" \
  --build-arg "TARGETARCH=amd64" \
  --build-arg "APP_VERSION=$CANDIDATE_VERSION" \
  -f "$work/image/Dockerfile.release" -t "$image" "$work/image" \
  > "$evidence/image-build.txt" 2>&1
image_id=$(docker image inspect "$image" --format '{{.Id}}')
image_revision=$(docker image inspect "$image" \
  --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')
test "$image_revision" = "$STAGED_SHA"

echo "=== Start the second Docker engine with private storage ==="
host_ip=$(hostname -I | tr ' ' '\n' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | head -1)
[[ -n "$host_ip" && "$host_ip" != 127.* ]]
docker run -d --name "$remote_engine" --privileged --network host \
  -v "$work/remote-storage:/storage" \
  -e DOCKER_TLS_CERTDIR= \
  docker:dind \
  --host="tcp://$host_ip:2375" --host=unix:///var/run/docker.sock \
  --bip=172.31.0.1/24 --default-address-pool=base=172.30.0.0/16,size=24 \
  > "$evidence/remote-engine-id.txt"
for _ in $(seq 1 120); do
  docker -H "tcp://$host_ip:2375" info > "$evidence/remote-engine-info.txt" 2>&1 && break
  sleep 1
done
docker -H "tcp://$host_ip:2375" info > "$evidence/remote-engine-info.txt"
remote_server_version=$(docker -H "tcp://$host_ip:2375" version --format '{{.Server.Version}}')
local_server_version=$(docker version --format '{{.Server.Version}}')
dind_digest=$(docker image inspect docker:dind --format '{{index .RepoDigests 0}}')
export WORKSPACE="$work" IMAGE_ID="$image_id" IMAGE_REVISION="$image_revision"
export IMAGE_REFERENCE="$image" LOCAL_DOCKER_VERSION="$local_server_version"
export REMOTE_DOCKER_VERSION="$remote_server_version" DIND_DIGEST="$dind_digest"
export STAGED_SHA CANDIDATE_VERSION

echo "=== Write the isolated controller configuration ==="
cat > "$work/config.yaml" <<EOF
server:
  host: "127.0.0.1"
  port: "$controller_port"
database:
  path: "$work/data/carbon-panel.db"
  panel_backup: "off"
storage:
  data_dir: "$work/data"
  backup_dir: "$work/backups"
  temp_dir: "$work/tmp"
docker:
  host: "unix:///var/run/docker.sock"
  network_name: "carbon-panel-archive-rehearsal"
minecraft:
  global_config:
    onlineMode: false
auth:
  local:
    enabled: true
    allow_registration: false
proxy:
  enabled: false
EOF

python3 - "$evidence/provenance.json" <<'PY'
import hashlib, json, os, sys
from pathlib import Path
path = Path(sys.argv[1])
binary = Path(os.environ["WORKSPACE"]) / "image/linux_amd64/carbon-panel"
record = {
    "commit": os.environ["STAGED_SHA"],
    "version": os.environ["CANDIDATE_VERSION"],
    "image": os.environ["IMAGE_ID"],
    "image_revision": os.environ["IMAGE_REVISION"],
    "image_reference": os.environ["IMAGE_REFERENCE"],
    "source_binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
    "local_docker_server_version": os.environ["LOCAL_DOCKER_VERSION"],
    "remote_docker_server_version": os.environ["REMOTE_DOCKER_VERSION"],
    "docker_dind_digest": os.environ["DIND_DIGEST"],
    "controller_config_sha256": hashlib.sha256(
        (Path(os.environ["WORKSPACE"]) / "config.yaml").read_bytes()).hexdigest(),
}
path.write_text(json.dumps(record, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY

echo "=== Start the isolated controller ==="
docker run -d --name "$controller" --network host \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$work:$work" \
  -e TZ=UTC \
  "$image" ./carbon-panel -config "$work/config.yaml" \
  > "$evidence/controller-id.txt"
for _ in $(seq 1 120); do
  curl --silent --fail "http://127.0.0.1:$controller_port/healthz" \
    > "$evidence/healthz.json" 2>/dev/null && break
  sleep 1
done
curl --silent --show-error --fail "http://127.0.0.1:$controller_port/healthz" \
  > "$evidence/healthz.json"

echo "=== Run the native archive acceptance rehearsal ==="
set +e
QUALIFY_NPM_PREFIX="$PWD/scripts/qualify-compose-client" \
python3 scripts/server_archive_rehearsal_ci.py \
  --workspace "$work" \
  --base "http://127.0.0.1:$controller_port" \
  --candidate-version "$CANDIDATE_VERSION" \
  --candidate-commit "$STAGED_SHA" \
  --provenance "$evidence/provenance.json" \
  --local-docker unix:///var/run/docker.sock \
  --remote-docker "tcp://$host_ip:2375" \
  --remote-storage-root /storage \
  --remote-controller-storage "$work/remote-storage" \
  --mc-version "${MC_VERSION:-1.21.1}" \
  --readiness-script "$PWD/scripts/qualify-archive-player.mjs"
status=$?
set -e
cp "$work/server-archive-rehearsal.json" "$evidence/" 2>/dev/null || true
exit "$status"
