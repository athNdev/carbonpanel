#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# Hosted-job wrapper for the disposable server-portability acceptance rehearsal.
#
# Everything this script creates lives in the caller-owned workspace under
# $RUNNER_TEMP: an exact-head controller image/container, a second privileged
# Docker-in-Docker engine on its own bridge with a private storage mount, and the
# evidence tree. It refuses to run outside a GitHub-hosted job so it can never
# adopt a developer workstation, a live panel or another checkout.
#
# This is the backend RPC rehearsal (OSS-426/427) with an empty frontend embed.
# OSS-428 browser acceptance belongs to #686 and is deliberately not claimed here.
set -euo pipefail
umask 077

work=${1:?usage: qualify-server-archive-hosted.sh <disposable-workspace>}
: "${STAGED_SHA:?}" "${CANDIDATE_VERSION:?}"
: "${GITHUB_ACTIONS:?}" "${RUNNER_TEMP:?}" "${RUNNER_ENVIRONMENT:?}"
[[ "$GITHUB_ACTIONS" == true && "$RUNNER_ENVIRONMENT" == github-hosted ]]
work=$(realpath -m "$work")
runner_temp=$(realpath -m "$RUNNER_TEMP")
[[ "$work" == "$runner_temp/"* && "$work" != "$runner_temp" ]]
[[ ! -e "$work" ]]

# Pin the nested engine by digest resolved from the primary Docker Hub registry.
# The tag is recorded only as provenance; the digest is what Docker runs.
dind_repository="library/docker"
dind_tag="dind"
dind_digest="sha256:fbbc96e0af32cdc190aff92ba97a1281a115351a723afe21af78879cc55126b0"
dind_image="docker:${dind_tag}@${dind_digest}"
dind_registry_source="https://registry-1.docker.io/v2/${dind_repository}/manifests/${dind_tag}"

label=server-archive
controller="server-archive-controller-$$"
remote_engine="server-archive-node-$$"
network="server-archive-net-$$"
controller_id=""
remote_engine_id=""
network_id=""
controller_port=${CONTROLLER_PORT:-18080}
image="server-archive-rehearsal:$STAGED_SHA"
stage=prepare

mkdir "$work"
mkdir -p "$work"/{data,backups,tmp,remote-storage,image/linux_amd64} "$work/evidence"
evidence="$work/evidence"

python3 - "$work/run.json" "$work" <<'PY'
import json, os, sys, time
from pathlib import Path
path, workspace = sys.argv[1], sys.argv[2]
stat = Path(workspace).stat()
record = {
    "schema": "server-archive-rehearsal-run-v1",
    "run_id": "%s-%s" % (os.environ.get("GITHUB_RUN_ID", "local"),
                         os.environ.get("GITHUB_RUN_ATTEMPT", "0")),
    "workspace": str(Path(workspace).resolve()),
    "runner_temp": str(Path(os.environ["RUNNER_TEMP"]).resolve()),
    "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    "workspace_device": stat.st_dev,
    "workspace_inode": stat.st_ino,
}
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, "w", encoding="utf-8") as handle:
    json.dump(record, handle, indent=2, sort_keys=True)
    handle.write("\n")
PY

owned_container() {
  local name=$1 expected=$2 observed_label observed_id
  observed_label=$(docker inspect "$name" \
    --format '{{index .Config.Labels "com.carbon-panel.rehearsal"}}' 2>/dev/null) || return 1
  observed_id=$(docker inspect "$name" --format '{{.Id}}' 2>/dev/null) || return 1
  [[ "$observed_label" == "$label" ]]
  [[ -z "$expected" || "$observed_id" == "$expected" ]]
}

owned_network() {
  local observed_label observed_id
  observed_label=$(docker network inspect "$network" \
    --format '{{index .Labels "com.carbon-panel.rehearsal"}}' 2>/dev/null) || return 1
  observed_id=$(docker network inspect "$network" --format '{{.Id}}' 2>/dev/null) || return 1
  [[ "$observed_label" == "$label" ]]
  [[ -z "$network_id" || "$observed_id" == "$network_id" ]]
}

collect_evidence() {
  local code=$1
  {
    printf 'stage=%s\nstatus=%s\n' "$stage" "$code"
    printf 'workspace=%s\ncandidate=%s\nversion=%s\n' \
      "$work" "$STAGED_SHA" "$CANDIDATE_VERSION"
  } > "$evidence/run-status.txt"
  docker ps -a --format '{{.ID}} {{.Names}} {{.Image}} {{.Status}}' \
    > "$evidence/docker-ps.txt" 2>&1 || true
  docker inspect "$controller" "$remote_engine" \
    > "$evidence/containers-inspect.json" 2>&1 || true
  docker logs "$controller" > "$evidence/controller.log" 2>&1 || true
  docker logs "$remote_engine" > "$evidence/dind.log" 2>&1 || true
  docker network inspect "$network" > "$evidence/network-inspect.json" 2>&1 || true
  timeout 20 docker -H "${remote_docker:-tcp://127.0.0.1:1}" info \
    > "$evidence/remote-engine-info.txt" 2>&1 || true
  cp "$work/server-archive-rehearsal.json" "$evidence/" 2>/dev/null || true
}

cleanup() {
  local code=$?
  set +e
  trap - EXIT
  collect_evidence "$code"
  {
    if owned_container "$controller" "$controller_id"; then
      timeout 60 docker rm -f "$controller" || echo "could not remove controller"
    else
      echo "controller $controller is not this rehearsal's resource; left in place"
    fi
    if owned_container "$remote_engine" "$remote_engine_id"; then
      timeout 60 docker rm -f "$remote_engine" || echo "could not remove remote engine"
    else
      echo "remote engine $remote_engine is not this rehearsal's resource; left in place"
    fi
    if owned_network; then
      timeout 30 docker network rm "$network" || echo "could not remove bridge $network"
    else
      echo "bridge $network is not this rehearsal's resource; left in place"
    fi
  } > "$evidence/cleanup.txt" 2>&1
  exit "$code"
}
trap cleanup EXIT

stage=identity
[[ "$STAGED_SHA" =~ ^[0-9a-f]{40}$ ]]
[[ "$(git rev-parse HEAD)" == "$STAGED_SHA" ]]
[[ -z "$(git status --porcelain --untracked-files=no)" ]]
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

stage=dind-digest
python3 - "$dind_repository" "$dind_digest" <<'PY' | tee "$evidence/dind-registry-digest.txt"
import json, sys, urllib.request
repo, expected = sys.argv[1], sys.argv[2]
token_url = ("https://auth.docker.io/token?service=registry.docker.io"
             "&scope=repository:%s:pull" % repo)
with urllib.request.urlopen(token_url, timeout=30) as response:
    token = json.load(response)["token"]
request = urllib.request.Request(
    "https://registry-1.docker.io/v2/%s/manifests/%s" % (repo, expected),
    method="HEAD",
    headers={
        "Authorization": "Bearer " + token,
        "Accept": ("application/vnd.oci.image.index.v1+json, "
                   "application/vnd.docker.distribution.manifest.list.v2+json, "
                   "application/vnd.docker.distribution.manifest.v2+json"),
    })
with urllib.request.urlopen(request, timeout=30) as response:
    observed = response.headers.get("Docker-Content-Digest", "")
if observed != expected:
    raise SystemExit("registry digest %r does not match pinned %r"
                     % (observed, expected))
print(expected)
PY
stage=dind-pull
docker pull "$dind_image" > "$evidence/dind-pull.txt" 2>&1
docker image inspect "$dind_image" --format '{{json .RepoDigests}}' \
  > "$evidence/dind-repo-digests.json"
grep -F "$dind_digest" "$evidence/dind-repo-digests.json"

stage=build
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

stage=remote-engine
echo "=== Start the second Docker engine on its own bridge ==="
network_id=$(docker network create --driver bridge --subnet 172.29.0.0/24 \
  --label "com.carbon-panel.rehearsal=$label" \
  --label "com.carbon-panel.rehearsal.run=$STAGED_SHA" \
  "$network")
printf '%s\n' "$network_id" > "$evidence/network-id.txt"
# No host network and no published management port. The controller reaches the
# engine through the owned bridge; nested game ports are probed at the bridge
# address of the Docker-in-Docker container.
docker run -d --name "$remote_engine" --privileged --network "$network" \
  --network-alias "$remote_engine" \
  --label "com.carbon-panel.rehearsal=$label" \
  --label "com.carbon-panel.rehearsal.run=$STAGED_SHA" \
  -v "$work/remote-storage:/storage" \
  -e DOCKER_TLS_CERTDIR= \
  "$dind_image" \
  --host=tcp://0.0.0.0:2375 --host=unix:///var/run/docker.sock \
  --bip=172.31.0.1/24 --default-address-pool=base=172.30.0.0/16,size=24 \
  > "$evidence/remote-engine-id.txt"
remote_address=""
for _ in $(seq 1 60); do
  remote_address=$(docker inspect "$remote_engine" \
    --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null || true)
  [[ -n "$remote_address" ]] && break
  sleep 1
done
[[ "$remote_address" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]
[[ "$remote_address" != 127.* ]]
remote_docker="tcp://${remote_address}:2375"
export remote_docker
for _ in $(seq 1 120); do
  docker -H "$remote_docker" info > "$evidence/remote-engine-info.txt" 2>&1 && break
  sleep 1
done
docker -H "$remote_docker" info > "$evidence/remote-engine-info.txt"
remote_server_version=$(docker -H "$remote_docker" version --format '{{.Server.Version}}')
local_server_version=$(docker version --format '{{.Server.Version}}')
remote_engine_id=$(docker inspect "$remote_engine" --format '{{.Id}}')
nested_digests=$(docker -H "$remote_docker" image inspect "$dind_image" \
  --format '{{json .RepoDigests}}' 2>/dev/null || true)
if [[ -n "$nested_digests" ]]; then
  printf '%s\n' "$nested_digests" > "$evidence/nested-dind-repo-digests.json"
fi

stage=controller-config
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

stage=controller
echo "=== Start the isolated controller ==="
docker run -d --name "$controller" --network host \
  --label "com.carbon-panel.rehearsal=$label" \
  --label "com.carbon-panel.rehearsal.run=$STAGED_SHA" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$work:$work" \
  -e TZ=UTC \
  "$image" ./carbon-panel -config "$work/config.yaml" \
  > "$evidence/controller-id.txt"
controller_id=$(docker inspect "$controller" --format '{{.Id}}')
ready=0
for _ in $(seq 1 120); do
  if curl --silent --fail "http://127.0.0.1:$controller_port/healthz" \
      > "$evidence/healthz.json" 2> "$evidence/healthz.err"; then
    ready=1
    break
  fi
  sleep 1
done
if [[ "$ready" != 1 ]]; then
  stage=controller-health
  {
    echo "controller did not become healthy"
    cat "$evidence/healthz.err" 2>/dev/null || true
    docker inspect "$controller" --format '{{json .State}}' 2>/dev/null || true
  } > "$evidence/controller-health-failure.txt"
  false
fi

stage=fixture
export WORKSPACE="$work" IMAGE_ID="$image_id" IMAGE_REFERENCE="$image"
export IMAGE_REVISION="$image_revision" STAGED_SHA CANDIDATE_VERSION
export CONTROLLER_NAME="$controller" CONTROLLER_ID="$controller_id"
export CONTROLLER_PORT="$controller_port" REMOTE_ENGINE_NAME="$remote_engine"
export REMOTE_ENGINE_ID="$remote_engine_id" REMOTE_DOCKER="$remote_docker"
export REMOTE_ADDRESS="$remote_address" REMOTE_MANAGEMENT_PORT=2375
export DESTINATION_PROBE_HOST="$remote_address"
export REMOTE_STORAGE_ROOT=/storage
export REMOTE_CONTROLLER_STORAGE="$work/remote-storage"
export DIND_IMAGE="$dind_image" DIND_DIGEST="$dind_digest"
export DIND_REGISTRY_SOURCE="$dind_registry_source"
export LOCAL_DOCKER_VERSION="$local_server_version"
export REMOTE_DOCKER_VERSION="$remote_server_version"
export CANDIDATE_VERSION

# Created exclusively after all bindings exist, so the direct Python entry point
# can prove this run, checkout, controller container and remote endpoint together.
python3 - "$work/fixture.json" <<'PY'
import hashlib, json, os, time
from pathlib import Path
workspace = Path(os.environ["WORKSPACE"])
run_path = workspace / "run.json"
config_path = workspace / "config.yaml"
binary = workspace / "image/linux_amd64/carbon-panel"
def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()
record = {
    "schema": "server-archive-rehearsal-fixture-v1",
    "workspace": str(workspace.resolve()),
    "workspace_device": workspace.stat().st_dev,
    "workspace_inode": workspace.stat().st_ino,
    "runner_temp": str(Path(os.environ["RUNNER_TEMP"]).resolve()),
    "run_file": str(run_path.resolve()),
    "run_sha256": digest(run_path),
    "run_id": json.loads(run_path.read_text(encoding="utf-8"))["run_id"],
    "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    "commit": os.environ["STAGED_SHA"],
    "version": os.environ["CANDIDATE_VERSION"],
    "image": os.environ["IMAGE_ID"],
    "image_reference": os.environ["IMAGE_REFERENCE"],
    "image_revision": os.environ["IMAGE_REVISION"],
    "source_binary_sha256": digest(binary),
    "controller_name": os.environ["CONTROLLER_NAME"],
    "controller_id": os.environ["CONTROLLER_ID"],
    "controller_port": int(os.environ["CONTROLLER_PORT"]),
    "controller_image_id": os.environ["IMAGE_ID"],
    "controller_config": str(config_path.resolve()),
    "controller_config_sha256": digest(config_path),
    "base_url": "http://127.0.0.1:%s" % os.environ["CONTROLLER_PORT"],
    "remote_engine_name": os.environ["REMOTE_ENGINE_NAME"],
    "remote_engine_id": os.environ["REMOTE_ENGINE_ID"],
    "remote_docker": os.environ["REMOTE_DOCKER"],
    "remote_address": os.environ["REMOTE_ADDRESS"],
    "remote_management_port": int(os.environ["REMOTE_MANAGEMENT_PORT"]),
    "destination_probe_host": os.environ["DESTINATION_PROBE_HOST"],
    "remote_storage_root": os.environ["REMOTE_STORAGE_ROOT"],
    "remote_controller_storage": str(
        Path(os.environ["REMOTE_CONTROLLER_STORAGE"]).resolve()),
    "dind_image": os.environ["DIND_IMAGE"],
    "dind_digest": os.environ["DIND_DIGEST"],
    "dind_registry_source": os.environ["DIND_REGISTRY_SOURCE"],
    "local_docker_server_version": os.environ["LOCAL_DOCKER_VERSION"],
    "remote_docker_server_version": os.environ["REMOTE_DOCKER_VERSION"],
}
fd = os.open(workspace / "fixture.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, "w", encoding="utf-8") as handle:
    json.dump(record, handle, indent=2, sort_keys=True)
    handle.write("\n")
PY

stage=rehearsal
echo "=== Run the native archive acceptance rehearsal ==="
set +e
QUALIFY_NPM_PREFIX="$PWD/scripts/qualify-compose-client" \
python3 scripts/server_archive_rehearsal_ci.py \
  --workspace "$work" \
  --fixture "$work/fixture.json" \
  --base "http://127.0.0.1:$controller_port" \
  --candidate-version "$CANDIDATE_VERSION" \
  --candidate-commit "$STAGED_SHA" \
  --local-docker unix:///var/run/docker.sock \
  --remote-docker "$remote_docker" \
  --remote-storage-root /storage \
  --remote-controller-storage "$work/remote-storage" \
  --mc-version "${MC_VERSION:-1.21.1}" \
  --readiness-host 127.0.0.1 \
  --destination-readiness-host "$remote_address" \
  --readiness-script "$PWD/scripts/qualify-archive-player.mjs"
status=$?
set -e
cp "$work/server-archive-rehearsal.json" "$evidence/" 2>/dev/null || true
exit "$status"
