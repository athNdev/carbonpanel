#!/bin/sh
# Build an unprivileged-LXC rootfs artifact.
# Offline fixture mode is qualified here; production mode requires verified
# upstream pins and an operator signing key and is NOT qualified by this lane.
set -eu
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PYTHON=$(command -v python3 || command -v python)
exec "$PYTHON" "$DIR/node_lxc_artifact.py" build "$@"
