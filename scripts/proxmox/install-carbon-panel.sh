#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# Install or update Carbon Panel (controller) in an existing Linux guest using
# the shipped Docker image and a Compose file. This script does not provision a
# Proxmox host, create an LXC/VM, disable host security, or source third-party
# installers. It is intended for a prepared guest (for example a Proxmox LXC or
# VM) with Docker Engine and Docker Compose v2 using the guest-local Docker
# daemon.
#
# Inspect before running:
#   curl -fsSLO https://raw.githubusercontent.com/athNdev/carbonpanel/main/scripts/proxmox/install-carbon-panel.sh
#   less install-carbon-panel.sh
#   sudo bash install-carbon-panel.sh [install|update] [--dir DIR] [--image IMAGE]
#
# Existing compose/config and bind-mounted data/backups/tmp are preserved.

set -euo pipefail

SERVICE="carbon-panel"
COMPOSE_BASENAME="compose.yml"
MARKER_FILE=".carbon-panel-install-owned"
COMPOSE_TMP=""
LOCK_DIR=""
LOCK_HELD=false

cleanup_compose_tmp() {
  if [ -n "$COMPOSE_TMP" ]; then
    rm -f -- "$COMPOSE_TMP"
  fi
}

release_dir_lock() {
  if [ "$LOCK_HELD" = true ]; then
    # Only remove the pid file we wrote, then the (now empty) lock directory.
    if [ "$(cat "$LOCK_DIR/pid" 2>/dev/null || true)" = "$$" ]; then
      rm -f -- "$LOCK_DIR/pid"
      rmdir -- "$LOCK_DIR" 2>/dev/null || true
    fi
    LOCK_HELD=false
  fi
}

cleanup() {
  cleanup_compose_tmp
  release_dir_lock
}
trap cleanup EXIT

usage() {
  cat <<'EOF'
Usage: install-carbon-panel.sh [install|update|help] [--dir DIR] [--image IMAGE]

Install or update the Carbon Panel controller in an existing Linux guest with
guest-local Docker Engine and Docker Compose v2.

Commands:
  install   Create install directory/compose if missing and start the
            carbon-panel service. If compose.yml already exists, preserves it
            and does not pull or recreate a configured deployment; it may only
            resume an incomplete install previously started by this script.
  update    Pull and recreate only the carbon-panel service using the existing
            compose.yml; preserves bind-mounted data/backups/tmp and config.
            Snapshot the controller database before upgrading (see below).
  help      Show this help

Options:
  --dir DIR      Install directory path (canonicalized). Default:
                 /opt/carbon-panel or $CARBON_PANEL_DIR
  --image IMAGE  Container image for a newly written compose.yml only.
                 Default: atharvnaphade/carbonpanel:latest or $CARBON_PANEL_IMAGE
                 Rejected for update to preserve existing compose config.

Scope and safety:
  - Supported for an existing guest-local Docker deployment only.
  - Requires Docker context "default" with a local unix socket
    (unix:///var/run/docker.sock). Refuses remote contexts/endpoints and
    non-local DOCKER_HOST/DOCKER_CONTEXT overrides because bind mount paths
    must resolve on this guest and generated compose mounts /var/run/docker.sock.
  - Does not create Proxmox guests, change host security, or delete bind mounts.
  - Does not overwrite an existing compose.yml, config.yaml, data, backups, or tmp.
  - Refuses concurrent install/update for the same install directory
    (fail-closed lock) and publishes a new compose.yml with an atomic
    no-clobber link, so a compose created concurrently is never
    overwritten or resumed as this script's partial.
  - Compose bind mounts use fixed relative paths (./data, ./backups, ./tmp)
    resolved from the install directory.

Recommended upgrade workflow:
  Snapshot the controller database before upgrading. For guarded snapshot and
  rollback helpers, use the Carbon Panel repository scripts:
    scripts/snapshot-panel-db.sh
    scripts/compose-upgrade.sh
    scripts/compose-upgrade-rollback.sh
  (see docs/getting-started/docker-compose/ and docs/runbook-panel-db-restore.md)
EOF
}

log() { printf '%s\n' "$*"; }
err() { printf 'error: %s\n' "$*" >&2; }
die() { err "$*"; exit 1; }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

acquire_dir_lock() {
  # Fail-closed per-directory lock. mkdir(2) is atomic on the guest
  # filesystem; a stale lock is never taken over automatically because a
  # live/unknown holder cannot be distinguished safely. The operator removes
  # it only after confirming no installer is running.
  LOCK_DIR="$DIR/.carbon-panel-install.lock"
  if ! mkdir "$LOCK_DIR" 2>/dev/null; then
    die "another install/update is already running for $DIR (lock: $LOCK_DIR); wait for it to finish, or remove the lock only after confirming no installer is running"
  fi
  LOCK_HELD=true
  if ! printf '%s\n' "$$" >"$LOCK_DIR/pid"; then
    # We created this lock directory, so it is safe to unlink our pid and
    # remove it; never recurse.
    rm -f -- "$LOCK_DIR/pid"
    rmdir -- "$LOCK_DIR" 2>/dev/null || true
    LOCK_HELD=false
    die "unable to write install lock pid file: $LOCK_DIR/pid"
  fi
}

compose_digest() {
  sha256sum "$1" | awk '{print $1}'
}

marker_matches_compose() {
  local expected actual
  expected="$(cat "$DIR/$MARKER_FILE" 2>/dev/null || true)"
  actual="$(compose_digest "$COMPOSE_FILE" 2>/dev/null || true)"
  [ -n "$expected" ] && [ -n "$actual" ] && [ "$expected" = "$actual" ]
}

write_marker() {
  compose_digest "$COMPOSE_FILE" >"$DIR/$MARKER_FILE"
}

has_control_chars() {
  local s=$1
  [[ "$s" == *$'\n'* || "$s" == *$'\r'* || "$s" == *$'\t'* ]]
}

validate_dir_input() {
  local dir=$1
  [ -n "$dir" ] || die "install directory must not be empty"
  if has_control_chars "$dir"; then
    die "install directory contains unsupported control characters"
  fi
}

validate_image_ref() {
  local image=$1
  [ -n "$image" ] || die "image must not be empty"
  if has_control_chars "$image"; then
    die "image contains unsupported control characters"
  fi
  [[ "$image" =~ [[:space:]] ]] && die "image must not contain whitespace"
  [[ "$image" == *'"'* || "$image" == *'\'* ]] && die "image contains unsupported quote/backslash characters"
  [[ "$image" =~ ^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$ ]] || die "image contains unsupported characters: $image"
}

canonicalize_dir_install() {
  local dir=$1
  mkdir -p "$dir"
  (cd "$dir" && pwd -P)
}

canonicalize_dir_existing() {
  local dir=$1
  [ -d "$dir" ] || die "install directory not found: $dir"
  (cd "$dir" && pwd -P)
}

require_local_docker_context() {
  local ctx endpoint
  if [ -n "${DOCKER_CONTEXT:-}" ] && [ "${DOCKER_CONTEXT}" != "default" ]; then
    die "configured DOCKER_CONTEXT is not supported; use the guest-local default Docker context"
  fi
  case "${DOCKER_HOST:-}" in
    ""|unix:///var/run/docker.sock)
      ;;
    *)
      die "configured DOCKER_HOST is not supported; this installer requires the guest-local unix socket /var/run/docker.sock"
      ;;
  esac

  ctx="$(docker context show 2>/dev/null || true)"
  [ -n "$ctx" ] || die "unable to determine Docker context"
  [ "$ctx" = "default" ] || die "unsupported Docker context: use the guest-local default Docker daemon"

  endpoint="$(docker context inspect --format '{{with .Endpoints.docker}}{{.Host}}{{end}}' "$ctx" 2>/dev/null || true)"
  [ -n "$endpoint" ] || die "unable to determine Docker endpoint for default context"
  [ "$endpoint" = "unix:///var/run/docker.sock" ] || die "unsupported Docker endpoint for default context: guest-local unix socket /var/run/docker.sock is required"
}

require_compose_v2() {
  docker compose version >/dev/null 2>&1 || die "Docker Compose v2 not found (required command: 'docker compose')"
  DC=(docker compose)
}

compose() {
  (cd "$DIR" && "${DC[@]}" -f "$COMPOSE_FILE" "$@")
}

service_exists() {
  local cid
  cid="$(compose ps -aq "$SERVICE" 2>/dev/null || true)"
  [ -n "$cid" ]
}

write_compose() {
  # Fixed relative bind mounts are resolved from the install directory.
  # Do not overwrite an existing compose file. Write atomically so an
  # interrupted install never leaves a truncated compose behind.
  COMPOSE_TMP="$(mktemp "$COMPOSE_FILE.tmp.XXXXXX")" || die "unable to create temporary compose file in $DIR"
  cat >"$COMPOSE_TMP" <<EOF
# Managed by scripts/proxmox/install-carbon-panel.sh (Carbon Panel controller).
# Review before changes. Existing data/backups/tmp and config are preserved.
services:
  carbon-panel:
    image: "${IMAGE}"
    container_name: carbon-panel
    restart: unless-stopped
    network_mode: host
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./data:/app/data
      - ./backups:/app/backups
      - ./tmp:/app/tmp
      # Uncomment to use a config file (kept on the host across updates):
      # - ./config.yaml:/app/config.yaml:ro
    environment:
      - TZ=UTC
    extra_hosts:
      - "host.docker.internal:host-gateway"
EOF
  chmod 0644 "$COMPOSE_TMP" 2>/dev/null || true
  # link(2) is atomic and fails when the target exists, unlike mv. -T keeps
  # the target itself as the link path even if it is a directory or symlink.
  # A compose created after the earlier existence check must win.
  if ! ln -T -- "$COMPOSE_TMP" "$COMPOSE_FILE" 2>/dev/null; then
    rm -f -- "$COMPOSE_TMP"
    COMPOSE_TMP=""
    rm -f -- "$DIR/$MARKER_FILE"
    die "could not publish $COMPOSE_FILE atomically (a compose file may have appeared concurrently); refusing to overwrite it. Re-run install to preserve the existing deployment."
  fi
  rm -f -- "$COMPOSE_TMP"
  COMPOSE_TMP=""
}

ACTION="install"
DIR_INPUT="${CARBON_PANEL_DIR:-/opt/carbon-panel}"
IMAGE="${CARBON_PANEL_IMAGE:-atharvnaphade/carbonpanel:latest}"
IMAGE_SET=false

while [ "$#" -gt 0 ]; do
  case "$1" in
    install|update)
      ACTION="$1"
      shift
      ;;
    help|-h|--help)
      usage
      exit 0
      ;;
    --dir)
      [ "$#" -ge 2 ] || die "--dir requires a value"
      DIR_INPUT="$2"
      shift 2
      ;;
    --image)
      [ "$#" -ge 2 ] || die "--image requires a value"
      IMAGE="$2"
      IMAGE_SET=true
      shift 2
      ;;
    *)
      die "unknown argument: $1 (use --help)"
      ;;
  esac
done

validate_dir_input "$DIR_INPUT"

if [ "$ACTION" = "update" ] && [ "$IMAGE_SET" = true ]; then
  die "--image is not applied during update to preserve existing compose config; edit the compose file if you intend to change the image"
fi

if [ "$ACTION" = "install" ]; then
  validate_image_ref "$IMAGE"
fi

need_cmd docker
need_cmd sha256sum
require_local_docker_context
if ! docker info >/dev/null 2>&1; then
  die "cannot talk to guest-local Docker daemon (is Docker running and do you have permission?)"
fi
require_compose_v2

case "$ACTION" in
  install)
    DIR="$(canonicalize_dir_install "$DIR_INPUT")"
    ;;
  update)
    DIR="$(canonicalize_dir_existing "$DIR_INPUT")"
    ;;
  *)
    die "unsupported action: $ACTION"
    ;;
esac

COMPOSE_FILE="$DIR/$COMPOSE_BASENAME"
DATA_DIR="$DIR/data"
BACKUPS_DIR="$DIR/backups"
TMP_DIR="$DIR/tmp"

acquire_dir_lock

case "$ACTION" in
  install)
    log "Carbon Panel controller installer (existing guest, guest-local Docker Compose v2)"
    log "Install directory: $DIR"
    mkdir -p "$DATA_DIR" "$BACKUPS_DIR" "$TMP_DIR"

    if [ -f "$COMPOSE_FILE" ]; then
      log "Existing compose file found; preserving it: $COMPOSE_FILE"
      if [ -f "$DIR/$MARKER_FILE" ] && marker_matches_compose; then
        log "Incomplete install detected (installer-owned marker present). Resuming pull/start..."
        if ! compose pull "$SERVICE"; then
          die "resume pull failed; compose/config preserved. Fix connectivity or image access, then rerun install to resume (data/backups preserved)."
        fi
        if ! compose up -d "$SERVICE"; then
          die "resume failed; compose/config preserved. Inspect Docker logs and rerun install to resume (data/backups preserved)."
        fi
        rm -f "$DIR/$MARKER_FILE"
        log "Resume complete."
      else
        if [ -f "$DIR/$MARKER_FILE" ]; then
          log "Ownership marker does not match the current compose; not resuming it."
          rm -f "$DIR/$MARKER_FILE"
        fi
        if service_exists; then
          log "Existing carbon-panel service detected. No pull or container recreate performed."
          log "To update the controller (after snapshotting the controller DB), run:"
          log "  sudo bash \"$0\" update --dir \"$DIR\""
        else
          log "Compose exists but carbon-panel service is not present; preserving existing deployment configuration."
          log "No pull or container recreate performed. If this was an interrupted install created by this script, remove '$COMPOSE_FILE' only if you intend to regenerate it, or run update/recovery as appropriate."
          exit 1
        fi
      fi
      log "Data: $DATA_DIR  Backups: $BACKUPS_DIR  Tmp: $TMP_DIR"
      exit 0
    fi

    write_compose
    write_marker
    log "Wrote compose file: $COMPOSE_FILE"
    log "Pulling and starting Carbon Panel (service: $SERVICE)..."
    if ! compose pull "$SERVICE"; then
      die "pull failed; compose/config preserved. Fix connectivity or image access, then rerun install to resume (data/backups preserved)."
    fi
    if ! compose up -d "$SERVICE"; then
      die "container start failed; compose/config preserved. Inspect Docker logs and rerun install to resume (data/backups preserved)."
    fi
    rm -f "$DIR/$MARKER_FILE"

    log
    log "Carbon Panel should be available at http://<guest-ip>:8080 (default)."
    log "Data: $DATA_DIR  Backups: $BACKUPS_DIR  Tmp: $TMP_DIR"
    log "Before upgrading, snapshot the controller DB. See docs and repo scripts:"
    log "  scripts/snapshot-panel-db.sh, scripts/compose-upgrade.sh, scripts/compose-upgrade-rollback.sh"
    log "Convenience update command (preserves data/config): sudo bash \"$0\" update --dir \"$DIR\""
    ;;
  update)
    [ -f "$COMPOSE_FILE" ] || die "compose file not found: $COMPOSE_FILE (run install first)"
    log "Updating Carbon Panel controller in: $DIR"
    log "Recommended: snapshot the controller database before upgrading."
    log "See repo scripts/snapshot-panel-db.sh, scripts/compose-upgrade.sh, scripts/compose-upgrade-rollback.sh"
    log "Preserving bind-mounted data/backups/tmp and existing compose/config."
    if ! compose pull "$SERVICE"; then
      die "update pull failed; existing container, compose/config and data preserved. Fix connectivity or image access, then rerun update."
    fi
    if ! compose up -d "$SERVICE"; then
      die "update recreate failed; compose/config and data preserved. Inspect Docker logs, then rerun update."
    fi
    log "Update complete. Confirm the panel is reachable and server data is intact."
    ;;
esac
