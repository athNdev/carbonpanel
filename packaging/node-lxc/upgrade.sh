#!/bin/sh
# Staged artifact upgrade / rollback for the LXC node.
set -eu
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PYTHON=$(command -v python3 || command -v python)
exec "$PYTHON" "$DIR/node_lxc_artifact.py" upgrade "$@"
