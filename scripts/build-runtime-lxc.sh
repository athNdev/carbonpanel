#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# Allowlisted build entry point for the unprivileged LXC rootfs artifact.
#
# It is a thin wrapper: the real entry point on main is
# packaging/node-lxc/build-rootfs.sh, which delegates to node_lxc_artifact.py.
# This lane runs its documented offline fixture profile only (no live LXC
# import/boot, no host provisioning). Production provenance, a base tarball and
# an operator signing key are owned by the artifact lane, not by this wrapper.
#
# ``--preflight`` resolves the wrapper AND its delegate via the offline
# runtime_artifacts boundary and exits without executing anything or writing
# any file. It is what the offline reachability fixture exercises.
set -euo pipefail

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
PYTHON="$(command -v python3 || command -v python)"
ARTIFACT="lxc-rootfs"
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

# Delegate to the real, offline LXC rootfs entry point (no live LXC import).
# build-rootfs.sh already supplies the `build` subcommand; pass only its flags.
bash "$ROOT/packaging/node-lxc/build-rootfs.sh" \
	--source-dir "$ROOT/packaging/node-lxc/fixtures/rootfs-minimal" \
	--out-dir "$OUTPUT" \
	--version "$VERSION" \
	--platform amd64 \
	--provenance "$ROOT/packaging/node-lxc/config/provenance.fixture.json" \
	--fixture

cat >"$OUTPUT/BUILD-PREFLIGHT.json" <<EOF
{
  "artifact": "$ARTIFACT",
  "version": "$VERSION",
  "commit": "$COMMIT",
  "delegate": "packaging/node-lxc/build-rootfs.sh",
  "mode": "offline-fixture",
  "live_build": false
}
EOF
