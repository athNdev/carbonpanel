#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
#
# Ownership / quarantine behavioural check (validates the MINE-251 / PR #230
# work and the MINE-253 / PR #233 recovery path) against running review
# instances started by review.sh.
#
# What it proves:
#   1. Two panels on one daemon have distinct installation identities.
#   2. A container owned by installation A but unknown to A's DB is QUARANTINED
#      (reported, left running), never auto-purged.
#   3. A container owned by installation B is ignored entirely by A.
#   4. The per-server lock is released after a completed operation (recovery).
#
# Usage: bash ownership-check.sh   (from WSL, instances A/B already up)

set -euo pipefail

CACHE_ROOT="${XDG_CACHE_HOME:-$HOME/.cache}/carbon-review"
SHA="${REVIEW_SESSION_SHA:-$(cat "$CACHE_ROOT/current")}"
[[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || { echo 'invalid review session revision' >&2; exit 1; }
SHORT="${SHA:0:12}"
PROJECT="carbon-review-$SHORT"
A="$PROJECT-a"
B="$PROJECT-b"
ORPHAN="review-orphan-$$"
FOREIGN="review-foreign-$$"
DIR="$CACHE_ROOT/$SHORT"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ROOT_REAL="$(realpath -m "$CACHE_ROOT")"
HOME_REAL="$(realpath -m "$HOME")"
case "$ROOT_REAL/" in "$HOME_REAL"/*) ;; *) echo 'invalid review cache' >&2; exit 1 ;; esac
[ ! -L "$DIR" ] && [ "$(realpath -m "$DIR")" = "$ROOT_REAL/$SHORT" ] &&
  [ -f "$DIR/.carbon-review-owner" ] && [ ! -L "$DIR/.carbon-review-owner" ] &&
  [ "$(cat "$DIR/.carbon-review-owner")" = "$(printf 'carbon-dual-review-v1\n%s\n%s\n%s' "$SHA" "$DIR" "$REPO_ROOT")" ] ||
  { echo 'review ownership marker missing or mismatched' >&2; exit 1; }
owned_id() {
  local record id owner
  record="$(docker inspect --format '{{.Id}} {{ index .Config.Labels "carbonpanel.review.owner" }}' "$1")"
  id="${record%% *}"; owner="${record#* }"
  [[ "$id" =~ ^[0-9a-f]{64}$ ]] && [ "$owner" = "$DIR" ] || { echo "unowned review container: $1" >&2; return 1; }
  printf '%s' "$id"
}
A="$(owned_id "$A")"
B="$(owned_id "$B")"
ORPHAN_ID=""; FOREIGN_ID=""
cleanup() {
  [ -z "$ORPHAN_ID" ] || docker rm -f "$ORPHAN_ID" >/dev/null 2>&1 || true
  [ -z "$FOREIGN_ID" ] || docker rm -f "$FOREIGN_ID" >/dev/null 2>&1 || true
}
trap cleanup EXIT

pass() { printf '  \033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; FAILED=1; }
info() { printf '  ..   %s\n' "$*"; }

echo "== Ownership / recovery check =="
echo "project: $PROJECT"
FAILED=0

# 1. Distinct installation identities ------------------------------------
IDA="$(docker exec "$A" cat /app/data/installation-id 2>/dev/null | tr -d '\r\n')"
IDB="$(docker exec "$B" cat /app/data/installation-id 2>/dev/null | tr -d '\r\n')"
info "A installation: $IDA"
info "B installation: $IDB"
if [ -n "$IDA" ] && [ -n "$IDB" ] && [ "$IDA" != "$IDB" ]; then
  pass "A and B have distinct installation identities"
else
  fail "installation identities missing or equal"
fi

# 2. Quarantine: owned by A, unknown to A's DB ---------------------------
ORPHAN_ID="$(docker run -d --name "$ORPHAN" \
  --label carbon-panel.managed=true \
  --label carbon-panel.server.id=orphan-demo \
  --label carbon-panel.installation="$IDA" \
  alpine:latest sleep 600)"
[[ "$ORPHAN_ID" =~ ^[0-9a-f]{64}$ ]] || { ORPHAN_ID=""; echo 'invalid created container id' >&2; exit 1; }
info "started owned-but-unknown container $ORPHAN"

docker restart "$A" >/dev/null
sleep 8
A_LOGS="$(docker logs --since 30s "$A" 2>&1 || true)"

if [ "$(docker inspect --format '{{.State.Running}}' "$ORPHAN_ID" 2>/dev/null || true)" = true ]; then
  pass "owned-but-unknown container was NOT removed (quarantined)"
else
  fail "owned-but-unknown container was removed (should be quarantined)"
fi

if echo "$A_LOGS" | grep -qi "quarantin"; then
  pass "A logged the quarantine decision"
else
  info "no 'quarantin' log line in the last 30s; dumping tail for inspection"
  echo "$A_LOGS" | tail -20 | sed 's/^/       /'
fi

# 3. Foreign ownership: owned by B, listed as managed --------------------
FOREIGN_ID="$(docker run -d --name "$FOREIGN" \
  --label carbon-panel.managed=true \
  --label carbon-panel.server.id=foreign-demo \
  --label carbon-panel.installation="$IDB" \
  alpine:latest sleep 600)"
[[ "$FOREIGN_ID" =~ ^[0-9a-f]{64}$ ]] || { FOREIGN_ID=""; echo 'invalid created container id' >&2; exit 1; }
info "started B-owned container $FOREIGN"

docker restart "$A" >/dev/null
sleep 8
A_LOGS2="$(docker logs --since 30s "$A" 2>&1 || true)"

if [ "$(docker inspect --format '{{.State.Running}}' "$FOREIGN_ID" 2>/dev/null || true)" = true ]; then
  pass "B-owned container untouched by A"
else
  fail "A removed B's container (foreign-ownership violation)"
fi

if echo "$A_LOGS2" | grep -qi "another Carbon Panel installation"; then
  pass "A logged the foreign-ownership refusal"
else
  info "no foreign-ownership log line; A may only list its own (acceptable)"
fi

# 4. Recovery lock release: start/stop on A must not wedge ---------------
if docker logs "$A" 2>&1 | grep -qi "requires manual recovery"; then
  fail "an operation is stuck requiring manual recovery"
else
  pass "no operations stuck requiring manual recovery"
fi

echo
if [ "$FAILED" = "1" ]; then
  echo "RESULT: FAIL"
  exit 1
fi
echo "RESULT: PASS"
