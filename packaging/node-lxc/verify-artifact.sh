#!/bin/sh
# Verify checksums, manifest, SBOM, signature and secret guard. Fail closed.
set -eu
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PYTHON=$(command -v python3 || command -v python)
exec "$PYTHON" "$DIR/node_lxc_artifact.py" verify "$@"
