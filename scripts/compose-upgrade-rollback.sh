#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

# Restore the controller SQLite file from a snapshot-panel-db artifact.
# Worlds, backups and other bind mounts are not read, written, or deleted.
# This is copy-back rollback, not a prune and not a volumes-deleting compose down.
#
# Default is dry-run (print the operator steps). Pass --apply to copy the
# snapshot onto database.path and drop leftover WAL sidecars. Stop the panel
# first (compose stop, or compose down without a volumes-delete flag).
#
# Usage:
#   scripts/compose-upgrade-rollback.sh --snapshot FILE [--compose FILE] [--data-dir DIR] [--db PATH] [--apply]
#
# Defaults:
#   --compose  docker-compose.yml (repo root, or $COMPOSE_FILE)
#   --data-dir host source of /app/data (default /opt/carbon-panel/data)
#   --db       <data-dir>/carbon-panel.db
#
# Interrupted upgrade / disk-full: if scripts/compose-upgrade.sh failed before
# compose up, the running image is unchanged. Copy back an earlier snapshot
# only if the live database is damaged. Do not prune volumes.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
COMPOSE="${COMPOSE_FILE:-$ROOT/docker-compose.yml}"
DATA_DIR="/opt/carbon-panel/data"
DB=""
SNAPSHOT=""
APPLY=false

usage() {
	sed -n '2,26p' "$0" | sed 's/^# \?//'
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
	--db)
		DB="${2:?--db requires a path}"
		shift 2
		;;
	--snapshot)
		SNAPSHOT="${2:?--snapshot requires a path}"
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

if [[ -z "$SNAPSHOT" ]]; then
	echo "--snapshot is required" >&2
	usage >&2
	exit 2
fi

if [[ ! -d "$DATA_DIR" ]]; then
	echo "data directory not found: $DATA_DIR" >&2
	exit 1
fi

DATA_ABS="$(cd "$DATA_DIR" && pwd)"
if [[ -z "$DB" ]]; then
	DB="$DATA_ABS/carbon-panel.db"
fi

if [[ ! -f "$SNAPSHOT" ]]; then
	echo "snapshot not found: $SNAPSHOT" >&2
	exit 1
fi

SNAPSHOT_ABS="$(cd "$(dirname "$SNAPSHOT")" && pwd)/$(basename "$SNAPSHOT")"
if [[ "$SNAPSHOT_ABS" == "$DB" ]]; then
	echo "refusing to copy a snapshot onto itself: $DB" >&2
	exit 1
fi

echo "Compose upgrade rollback is copy-back of the controller SQLite file only, not a prune."
echo "Stop the panel first (compose stop, or compose down without a volumes-delete flag)."
echo "Copy-back: cp \"$SNAPSHOT_ABS\" \"$DB\""
echo "Sidecars: rm -f \"${DB}-wal\" \"${DB}-shm\""
echo "Start the panel after copy-back. Do not prune volumes. Leave worlds and bind mounts untouched."
echo "World and backups directories are not deleted."

if [[ "$APPLY" != true ]]; then
	echo "Dry-run only. Pass --apply with the panel stopped to copy the snapshot onto the database file."
	exit 0
fi

cp "$SNAPSHOT_ABS" "$DB"
rm -f "${DB}-wal" "${DB}-shm"
echo "Restored controller database from $SNAPSHOT_ABS onto $DB"
echo "Leave worlds and bind mounts untouched."
