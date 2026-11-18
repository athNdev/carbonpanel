#!/bin/sh
# Read-only nested-runtime capability gate. Refuses unsupported hosts.
set -eu
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PYTHON=$(command -v python3 || command -v python)
exec "$PYTHON" "$DIR/node_lxc_artifact.py" capability-gate "$@"
