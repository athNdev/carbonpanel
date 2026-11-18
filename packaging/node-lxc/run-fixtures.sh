#!/bin/sh
# Run the offline fixture qualification suite (no host, no network, no root).
set -eu
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PYTHON=$(command -v python3 || command -v python)
exec "$PYTHON" -m unittest discover -s "$DIR/tests" -p 'test_*.py' -v
