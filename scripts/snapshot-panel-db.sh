#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

# Snapshot the controller SQLite database to a timestamped file beside the
# data directory. Ordinary make test / web-build / compose up paths never
# invoke this script. Restore is a copy of that file back onto database.path
# with the panel stopped; worlds and bind mounts are left untouched.
#
# Usage:
#   scripts/snapshot-panel-db.sh [--data-dir DIR] [--db PATH]
#
# Defaults:
#   --data-dir ./data
#   --db       <data-dir>/carbon-panel.db
#
# Restore (panel stopped): scripts/compose-upgrade-rollback.sh --snapshot FILE
# or:
#   cp <snapshot> <database.path>
#   rm -f <database.path>-wal <database.path>-shm
# Do not copy over worlds, backups, or other files under the data directory.

set -euo pipefail

DATA_DIR="./data"
DB=""

usage() {
	sed -n '2,23p' "$0" | sed 's/^# \?//'
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--data-dir)
		DATA_DIR="${2:?--data-dir requires a path}"
		shift 2
		;;
	--db)
		DB="${2:?--db requires a path}"
		shift 2
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

if [[ ! -d "$DATA_DIR" ]]; then
	echo "data directory not found: $DATA_DIR" >&2
	exit 1
fi

DATA_ABS="$(cd "$DATA_DIR" && pwd)"
PARENT="$(cd "$DATA_ABS/.." && pwd)"
if [[ -z "$DB" ]]; then
	DB="$DATA_ABS/carbon-panel.db"
fi
if [[ ! -f "$DB" ]]; then
	echo "controller database not found: $DB" >&2
	exit 1
fi

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
DEST="$PARENT/carbon-panel-db-${STAMP}.sqlite"
if [[ -e "$DEST" ]]; then
	echo "refusing to overwrite existing snapshot: $DEST" >&2
	exit 1
fi

snapshot_with_sqlite3() {
	sqlite3 "$DB" "VACUUM INTO '$DEST'"
}

snapshot_with_python() {
	python3 - "$DB" "$DEST" <<'PY'
import sqlite3
import sys

src, dest = sys.argv[1], sys.argv[2]
source = sqlite3.connect(src)
try:
    target = sqlite3.connect(dest)
    try:
        source.backup(target)
    finally:
        target.close()
finally:
    source.close()
PY
}

if command -v sqlite3 >/dev/null 2>&1; then
	snapshot_with_sqlite3
elif command -v python3 >/dev/null 2>&1; then
	snapshot_with_python
else
	if [[ -e "${DB}-wal" || -e "${DB}-shm" ]]; then
		echo "sqlite3/python3 not found and ${DB} has WAL sidecars; stop the panel or install sqlite3 before snapshotting" >&2
		exit 1
	fi
	echo "sqlite3/python3 not found; copying ${DB} (stop the panel first for a consistent copy)" >&2
	cp -f "$DB" "$DEST"
fi

echo "Wrote controller database snapshot: $DEST"
echo "Restore with the panel stopped:"
echo "  cp \"$DEST\" \"$DB\""
echo "  rm -f \"${DB}-wal\" \"${DB}-shm\""
echo "Leave worlds and bind mounts untouched."
