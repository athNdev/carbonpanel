#!/bin/sh
# Fail-closed validation of a provenance profile (artifact pins or fixture).
set -eu
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PYTHON=$(command -v python3 || command -v python)
exec "$PYTHON" "$DIR/node_lxc_artifact.py" validate-provenance "$@"
