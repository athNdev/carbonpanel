#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# Disposable refusal fixture: a fake docker binary proves that a colliding
# unowned sidecar is never removed or replaced by name. No daemon is contacted.
set -uo pipefail

work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
bin="${work}/bin"
export HOME="${work}/home"
data="${HOME}/.cache/carbon-panel-review/sessions/carbon-panel-review"
mkdir -p "$bin" "$data" "${data}/data"
printf 'carbon-review-v1\ncarbon-panel-review\n%s\n' "$data" >"${data}/.carbon-review-owner"

cat >"${bin}/docker" <<'STUB'
#!/usr/bin/env bash
case "$1" in
	info) exit 0 ;;
	ps) printf '%s\n' "$FAKE_NAMES" ;;
	inspect)
		if [ "${FAKE_INSPECT_MODE:-}" = "missing" ]; then exit 1; fi
		printf '%s\n' "$FAKE_INSPECT"
		;;
	rm) printf '%s\n' "${@: -1}" >>"${FAKE_LOG_RM}" ;;
	run)
		printf '%s\n' "$*" >>"${FAKE_LOG_RUN}"
		# Fail closed if the ownership label was omitted.
		case "$*" in *"carbonpanel.review.owner="*) exit 0 ;; *) exit 9 ;; esac
		;;
	*) exit 0 ;;
esac
STUB
chmod +x "${bin}/docker"

export PATH="${bin}:${PATH}"
export REVIEW_DEVSTACK_SOURCE_ONLY=1
export REVIEW_STACK_NAME=carbon-panel-review
export REVIEW_DATA_DIR="$data"
export FAKE_NAMES="carbon-panel-review-bluemap"
export FAKE_LOG_RM="${work}/rm.log"
export FAKE_LOG_RUN="${work}/run.log"
: >"$FAKE_LOG_RM"
: >"$FAKE_LOG_RUN"

# shellcheck source=devstack.sh
source "$(dirname "${BASH_SOURCE[0]}")/devstack.sh"
set +e

fail=0
assert_refused() {
	local label="$1"
	if [ -s "$FAKE_LOG_RM" ] || [ -s "$FAKE_LOG_RUN" ]; then
		echo "FAIL ${label}: refusal still mutated the daemon"
		fail=1
	else
		echo "PASS ${label}: unowned collision refused without removal or start"
	fi
	: >"$FAKE_LOG_RM"
	: >"$FAKE_LOG_RUN"
}

# A1: an existing container with no ownership label is never removed.
export FAKE_INSPECT="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef "
remove_owned_sidecar >/dev/null 2>&1
rc=$?
[ "$rc" -ne 0 ] || { echo "FAIL unowned inspect should refuse"; fail=1; }
assert_refused "unowned sidecar"

# A2: an ownership label from a different session is refused.
export FAKE_INSPECT="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef /some/other/session"
remove_owned_sidecar >/dev/null 2>&1
rc=$?
[ "$rc" -ne 0 ] || { echo "FAIL foreign owner should refuse"; fail=1; }
assert_refused "foreign owner"

# A3: start_bluemap must not replace an unowned collision.
export FAKE_INSPECT="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef "
start_bluemap "${data}/data/servers/x" "${data}/data/servers/x/modules/bluemap" world >/dev/null 2>&1
rc=$?
[ "$rc" -ne 0 ] || { echo "FAIL start with unowned collision should refuse"; fail=1; }
assert_refused "start refuses collision"

# A4: a genuinely owned sidecar is removed by its INSPECTED ID, not by name.
owned_id="aaaabbbbccccddddeeeeffff0000111122223333444455556666777788889999"
export FAKE_INSPECT="${owned_id} ${data}"
remove_owned_sidecar
rc=$?
if [ "$rc" -eq 0 ] && grep -Fqx "$owned_id" "$FAKE_LOG_RM"; then
	echo "PASS owned sidecar removed by immutable inspected id"
else
	echo "FAIL owned sidecar was not removed by inspected id"
	fail=1
fi

if [ "$fail" -ne 0 ]; then
	exit 1
fi
echo "ALL BLUEMAP OWNERSHIP REFUSAL CHECKS PASSED"
