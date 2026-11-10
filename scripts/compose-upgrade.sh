#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

# Recreate the Carbon Panel compose service onto a newer image without deleting
# host bind mounts. Worlds, the controller SQLite file and backups stay on the
# host because the shipped compose file bind-mounts them; this script never
# passes a volumes-delete flag to compose down and never removes Docker volumes.
#
# Default is dry-run (print the operator steps). Pass --apply to snapshot the
# controller database and run `docker compose up -d --pull always`.
#
# Usage:
#   scripts/compose-upgrade.sh [--compose FILE] [--data-dir DIR] [--apply]
#
# Defaults:
#   --compose  docker-compose.yml (repo root, or $COMPOSE_FILE)
#   --data-dir host source of /app/data (default /opt/carbon-panel/data)
#
# Helm and Terraform are not shipped in this repository. Do not treat this
# script as a Kubernetes or cloud rollout.
#
# Interrupted upgrade / disk-full: if the snapshot fails, this script stops
# before compose up. Worlds stay on the host. Roll back a damaged database
# with scripts/compose-upgrade-rollback.sh (copy-back, not a prune).
#
# After --apply, confirm the server list and that world directories on the
# bind mount still exist. A live create→join after upgrade still needs a
# host with Docker and a disposable Minecraft server.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
COMPOSE="${COMPOSE_FILE:-$ROOT/docker-compose.yml}"
DATA_DIR="/opt/carbon-panel/data"
APPLY=false

usage() {
	sed -n '2,32p' "$0" | sed 's/^# \?//'
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--compose)
		COMPOSE="${2:?--compose requires a path}"
		shift 2
		;;
	--data-dir)
		DATA_DIR="${2:?--data-dir requires a path}"
		shift 2
		;;
	--apply)
		APPLY=true
		shift
		;;
	--dry-run)
		APPLY=false
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "unknown argument: $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

if [[ ! -f "$COMPOSE" ]]; then
	echo "compose file not found: $COMPOSE" >&2
	exit 1
fi

if grep -E '^[[:space:]]*privileged:[[:space:]]*true([[:space:]]|$)' "$COMPOSE" >/dev/null; then
	echo "refusing to upgrade: compose file enables privileged mode" >&2
	exit 1
fi

if grep -Eiq 'docker[[:space:]]+compose[[:space:]]+down[[:space:]]+(-v|--volumes)' "$COMPOSE"; then
	echo "refusing to upgrade: compose file documents a volumes-deleting down" >&2
	exit 1
fi

SNAPSHOT_CMD=("bash" "$ROOT/scripts/snapshot-panel-db.sh" "--data-dir" "$DATA_DIR")
UP_CMD=(docker compose -f "$COMPOSE" up -d --pull always)

echo "Compose upgrade (bind mounts stay on the host; worlds and the controller DB are not deleted)."
echo "Snapshot: ${SNAPSHOT_CMD[*]}"
echo "Recreate: ${UP_CMD[*]}"
echo "Do not pass a volumes-delete flag to compose down. Leave worlds and bind mounts untouched."
echo "Helm and Terraform are not shipped; this is the Compose image-recreate path only."

if [[ "$APPLY" != true ]]; then
	echo "Dry-run only. Pass --apply on the install host to snapshot and recreate."
	exit 0
fi

if [[ ! -d "$DATA_DIR" ]]; then
	echo "data directory not found: $DATA_DIR" >&2
	exit 1
fi

if ! "${SNAPSHOT_CMD[@]}"; then
	echo "snapshot failed (disk full, interrupted copy, or missing tools); not running compose up."
	echo "Worlds and bind mounts are untouched. Rollback is copy-back of a prior snapshot via scripts/compose-upgrade-rollback.sh, not a prune."
	exit 1
fi
"${UP_CMD[@]}"
echo "Compose service recreated. Confirm the server list; world directories on the bind mount must still be present."
echo "Leave worlds and bind mounts untouched."
