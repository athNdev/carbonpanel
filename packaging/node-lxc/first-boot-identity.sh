#!/bin/sh
# Create unique first-boot identity and ADR0019 digest-only enrollment bundle.
set -eu
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PYTHON=$(command -v python3 || command -v python)
exec "$PYTHON" "$DIR/node_lxc_artifact.py" first-boot-identity "$@"
