#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# First boot of the Carbon Panel CONTROLLER CT. Runs inside the guest only.
# Idempotent: an existing compose file, config, data, backups or tmp is never
# replaced. Retried transparently by systemd until the guest-local Docker
# daemon and image pull are available.
set -euo pipefail

IMAGE_FILE=/usr/local/share/carbon-panel/controller-image.txt
INSTALLER=/usr/local/share/carbon-panel/install-carbon-panel.sh
INSTALL_DIR=/opt/carbon-panel
STATE_DIR=/var/lib/carbon-panel-ct
DONE="$STATE_DIR/firstboot.done"

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

die() { log "ERROR: $*"; exit 1; }

[ -r "$IMAGE_FILE" ] || die "missing pinned controller image file"
[ -r "$INSTALLER" ] || die "missing bundled first-party installer"
IMAGE="$(cat "$IMAGE_FILE")"
case "$IMAGE" in
  *'@sha256:'*) ;;
  *) die "controller image is not digest-pinned" ;;
esac
[[ "$IMAGE" != *'latest'* ]] || die "moving tag is not allowed in controller image"

command -v docker >/dev/null 2>&1 || die "guest-local Docker CLI is not installed"
docker compose version >/dev/null 2>&1 || die "guest-local Docker Compose v2 is not installed"

for _ in $(seq 1 30); do
  if docker info >/dev/null 2>&1; then
    break
  fi
  log "waiting for guest-local Docker daemon"
  sleep 2
done
docker info >/dev/null 2>&1 || die "guest-local Docker daemon is unavailable"

mkdir -p "$STATE_DIR"
if [ -e "$DONE" ]; then
  log "first boot already completed; preserving operator state"
  exit 0
fi

# Delegate all existing-state decisions to the installer. It owns the
# marker, resumes an incomplete install (pull + up), and refuses to pull or
# recreate over an operator's already-running deployment. A failed pull must
# NOT be converted into a success marker here.
log "installing/resuming controller from digest-pinned image"
bash "$INSTALLER" install --dir "$INSTALL_DIR" --image "$IMAGE"
: >"$DONE"
log "controller first boot complete"
