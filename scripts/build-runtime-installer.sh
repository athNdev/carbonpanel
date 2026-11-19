#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# Allowlisted build entry point for the portable installer artifact.
#
# It is a thin wrapper: the real entry point on main is
# scripts/qualify-node-installer-systemd.sh, a disposable Linux systemd
# qualification fixture. That fixture creates transient units and /run paths,
# so this lane delegates to it ONLY in its ``--preflight`` mode, which resolves
# the entry point without starting any service or mutating the host. No live
# systemd, dockerd, world or database is touched here.
#
# ``--preflight`` on this wrapper resolves both the wrapper AND its delegate
# via the offline runtime_artifacts boundary and exits without executing
# anything or writing any file. It is what the offline reachability fixture
# exercises.
set -euo pipefail

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
PYTHON="$(command -v python3 || command -v python)"
ARTIFACT="installer"
VERSION=""
COMMIT=""
OUTPUT=""
PREFLIGHT=0

while [ "$#" -gt 0 ]; do
	case "$1" in
		--version) [ "$#" -ge 2 ] || { echo "missing value for --version" >&2; exit 2; }; VERSION="$2"; shift 2 ;;
		--commit) [ "$#" -ge 2 ] || { echo "missing value for --commit" >&2; exit 2; }; COMMIT="$2"; shift 2 ;;
		--output) [ "$#" -ge 2 ] || { echo "missing value for --output" >&2; exit 2; }; OUTPUT="$2"; shift 2 ;;
		--preflight) PREFLIGHT=1; shift ;;
		*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
done

# Fail-closed preflight: both the wrapper and the real delegate must exist.
"$PYTHON" "$ROOT/scripts/runtime_artifacts.py" preflight-build --artifact "$ARTIFACT"

if [ "$PREFLIGHT" -eq 1 ]; then
	exit 0
fi

[ -n "$VERSION" ] || { echo "missing --version" >&2; exit 2; }
[ -n "$COMMIT" ] || { echo "missing --commit" >&2; exit 2; }
[ -n "$OUTPUT" ] || { echo "missing --output" >&2; exit 2; }
"$PYTHON" "$ROOT/scripts/runtime_artifacts.py" assert-no-app-release \
	--version "$VERSION" --artifact "$ARTIFACT"
mkdir -p "$OUTPUT"

# Delegate to the real entry point in its non-mutating preflight mode only.
bash "$ROOT/scripts/qualify-node-installer-systemd.sh" --preflight

cat >"$OUTPUT/BUILD-PREFLIGHT.json" <<EOF
{
  "artifact": "$ARTIFACT",
  "version": "$VERSION",
  "commit": "$COMMIT",
  "delegate": "scripts/qualify-node-installer-systemd.sh",
  "mode": "delegate-preflight-only",
  "live_build": false
}
EOF
