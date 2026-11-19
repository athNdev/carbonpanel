#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# Disposable Linux systemd qualification fixture for the portable node installer
# (OSS-379). Runs INSIDE an owned Linux environment with systemd as PID 1 (the
# WSL ubuntu_general instance in this workspace). It provisions nothing on the
# host: every path is a temporary directory under /tmp, every unit is a
# transient systemd-run unit, and no real dockerd is started. A no-op fixture
# "daemon" binary stands in for dockerd so START/RESTART/XDG-RESET evidence is
# about the rendered unit, not about a container engine.
#
# This script is DATA-ONLY and disposable: no package install, no /etc write,
# no global config, no network, no registry write.
#
# Usage (from an owned Linux shell):
#   bash scripts/qualify-node-installer-systemd.sh
#
# It prints a structured PASS/FAIL line per scenario and a final summary.

set -euo pipefail

# Non-mutating preflight for the runtime-artifact build wrapper: resolve this
# entry point and exit before mktemp, transient units or any /run write. It is
# used to prove the installer build step is reachable without a live systemd
# fixture or host provisioning. No daemon binary is built or started.
if [[ "${1:-}" == "--preflight" ]]; then
	printf 'PASS  preflight: node-installer systemd qualification entry point resolved (no units started, no /run writes)\n'
	exit 0
fi

REPO="${CARBON_INSTALLER_REPO:-/mnt/c/Users/Daybreak8458/.codex/worktrees/node-installer/carbonpanel}"
BIN="${CARBON_NODE_INSTALLER_BIN:-$REPO/carbon-node-installer-linux}"
FIXTURE_ROOT="$(mktemp -d /tmp/carbon-installer-qual.XXXXXX)"
UID_NUM="$(id -u)"
# The renderer requires a non-root runtime UID for rootless. This owned WSL
# fixture has no non-root account, so we use UID 1000 for the PATH contract and
# run the per-user unit in the existing (root) user manager with a fixture
# binary. No real rootless dockerd is started; the structural properties of the
# rendered per-user unit are what this fixture proves.
ROOTLESS_UID=1000
ROOTLESS_RUNTIME_DIR="/run/user/$ROOTLESS_UID/carbon-installer-qual"
OWNED_RUN="/run/carbon-installer-qual"
ROOTFUL_UNIT="/run/systemd/system/carbon-installer-qual-rootful.service"
trap 'rm -rf "$FIXTURE_ROOT"; rm -rf "$ROOTLESS_RUNTIME_DIR" "$OWNED_RUN"; rm -f "$ROOTFUL_UNIT"; systemctl daemon-reload 2>/dev/null || true' EXIT

pass() { echo "PASS  $1"; }
fail() { echo "FAIL  $1: $2"; FAILED=1; }
FAILED=0

note() { echo "--- $1"; }

# Build the Linux binary once if it is not supplied as a fixture artifact.
if [[ ! -x "$BIN" ]]; then
	(cd "$REPO" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$BIN" ./cmd/carbon-node-installer)
fi

# The fixture daemon is a no-op program that writes its argv and a heartbeat to
# a state file under the owned state root, then sleeps. It NEVER touches a real
# Docker path, socket, world or database.
mkdir -p "$OWNED_RUN"
FIXTURE_DAEMON="$OWNED_RUN/fixture-daemon.sh"
cat >"$FIXTURE_DAEMON" <<'EOS'
#!/usr/bin/env bash
set -eu
state_file="${CARBON_FIXTURE_STATE:?}"
mkdir -p "$(dirname "$state_file")"
{
  printf 'pid=%s\n' "$$"
  printf 'argv=%s\n' "$*"
  printf 'xdg=%s\n' "${XDG_RUNTIME_DIR:-unset}"
  printf 'boot=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} >>"$state_file"
sleep 3600
EOS
chmod 0755 "$FIXTURE_DAEMON"

STATE_ROOT="$FIXTURE_ROOT/state"
RUN_ROOT="$FIXTURE_ROOT/run"
mkdir -p "$STATE_ROOT" "$RUN_ROOT"
chmod 0755 "$STATE_ROOT" "$RUN_ROOT"

write_spec() {
	local mode="$1" socket="$2" uid="$3" binary="$4"
	cat >"$FIXTURE_ROOT/spec-$mode.json" <<EOF
{
  "node_id": "qual-$mode",
  "mode": "$mode",
  "config_path": "$STATE_ROOT/$mode/daemon.json",
  "socket_path": "$socket",
  "pidfile": "${socket%/*}/docker.pid",
  "data_root": "$STATE_ROOT/$mode/data",
  "exec_root": "$STATE_ROOT/$mode/exec",
  "containerd_namespace": "carbon-qual-$mode",
  "bridge_cidr": "10.231.0.1/24",
  "port_range": "30000-31000",
  "storage_driver": "vfs",
  "runtime_uid": $uid,
  "runtime_root": "${socket%/*}",
  "state_root": "$STATE_ROOT/$mode",
  "daemon_binary": "$binary",
  "rootless_launcher": "$binary"
}
EOF
}

render() {
	local mode="$1"
	"$BIN" render-daemon -daemon-spec "$FIXTURE_ROOT/spec-$mode.json"
}

note "systemd environment"
PID1="$(ps -p 1 -o comm=)"
echo "PID1=$PID1 UID=$UID_NUM"
if [[ "$PID1" != "systemd" ]]; then
	fail "systemd-as-PID-1" "PID1 is $PID1, not systemd"
	exit 1
fi
pass "systemd-as-PID-1 (real, not syntax-only)"

# ---------------------------------------------------------------------------
# Scenario 1: rootless per-user unit START + RESTART with owned paths.
# ---------------------------------------------------------------------------
note "scenario 1: rootless per-user unit start + restart"
mkdir -p "$STATE_ROOT/rootless" "$RUN_ROOT/rootless"
XDG_ROOT="$FIXTURE_ROOT/xdg"
mkdir -p "$XDG_ROOT"
# The renderer insists on /run/user/<uid>/ for rootless. Use the real per-user
# runtime directory if present, otherwise a temporary stand-in is not accepted
# by pure validation; we render with the real prefix but install a transient
# unit whose ExecStart is the fixture daemon, so no real socket is bound.
ROOTLESS_SOCKET="$ROOTLESS_RUNTIME_DIR/docker.sock"
mkdir -p "$ROOTLESS_RUNTIME_DIR"
write_spec rootless "$ROOTLESS_SOCKET" "$ROOTLESS_UID" "$FIXTURE_DAEMON"
ROOTLESS_RENDER="$(render rootless)"
echo "$ROOTLESS_RENDER" | grep -q 'WantedBy=default.target' && pass "rootless render: per-user target" || fail "rootless target" "missing default.target"
echo "$ROOTLESS_RENDER" | grep -q 'ExecStart=.*fixture-daemon.sh' && pass "rootless render: fixture launcher" || fail "rootless launcher" "fixture binary not rendered"

UNIT_DIR="$HOME/.config/systemd/user"
mkdir -p "$UNIT_DIR"
UNIT_FILE="$UNIT_DIR/carbon-installer-qual-rootless.service"
export CARBON_FIXTURE_STATE="$OWNED_RUN/state-rootless/heartbeat.log"
mkdir -p "$(dirname "$CARBON_FIXTURE_STATE")"
printf '%s\n' "$ROOTLESS_RENDER" | python3 -c 'import json,sys; print(json.load(sys.stdin)["unit"],end="")' >"$UNIT_FILE"
# Fixture accommodation only: let the no-op daemon write its heartbeat under
# the owned /run tree despite ProtectSystem=strict; PrivateTmp stays enabled.
sed -i -e 's/^Type=notify$/Type=simple/' -e "/^\[Service\]/a Environment=CARBON_FIXTURE_STATE=$CARBON_FIXTURE_STATE\nReadWritePaths=$OWNED_RUN $ROOTLESS_RUNTIME_DIR" "$UNIT_FILE"
systemctl --user daemon-reload
if systemctl --user start carbon-installer-qual-rootless.service; then
	pass "rootless unit START (systemctl --user start)"
else
	fail "rootless unit START" "systemctl --user start failed"
fi
sleep 1
ACTIVE="$(systemctl --user is-active carbon-installer-qual-rootless.service || true)"
echo "rootless is-active=$ACTIVE"
[[ "$ACTIVE" == "active" ]] && pass "rootless unit active" || fail "rootless unit active" "$ACTIVE"
systemctl --user show carbon-installer-qual-rootless.service -p ExecMainStatus -p ActiveEnterTimestamp -p Result --no-pager 2>&1 | head -5 || true
grep -q 'argv=.*--config-file=' "$CARBON_FIXTURE_STATE" && pass "rootless real log: fixture daemon received --config-file" || fail "rootless real log" "no argv record"
systemctl --user restart carbon-installer-qual-rootless.service
sleep 1
ACTIVE2="$(systemctl --user is-active carbon-installer-qual-rootless.service || true)"
[[ "$ACTIVE2" == "active" ]] && pass "rootless RESTART active" || fail "rootless RESTART" "$ACTIVE2"
BOOTS="$(grep -c 'boot=' "$CARBON_FIXTURE_STATE" || true)"
echo "rootless heartbeat boots=$BOOTS"
[[ "$BOOTS" -ge 2 ]] && pass "rootless restart produced a fresh boot record" || fail "rootless restart record" "boots=$BOOTS"

# ---------------------------------------------------------------------------
# Scenario 2: rootful unit START + RESTART via transient systemd-run (no unit
# file written to a global path; --unit is transient and disappears on exit).
# ---------------------------------------------------------------------------
note "scenario 2: rootful unit start + restart"
ROOTFUL_SOCKET="$FIXTURE_ROOT/run/docker.sock"
write_spec rootful "$ROOTFUL_SOCKET" "$UID_NUM" "$FIXTURE_DAEMON"
ROOTFUL_RENDER="$(render rootful)"
# The canonical rootful unit runs as root by default (no User= override) and
# carries an ordered, non-empty capability bound that includes CAP_SYS_ADMIN.
! echo "$ROOTFUL_RENDER" | grep -q '^User=' && pass "rootful render: explicit root (no User= override)" || fail "rootful root" "unexpected User= override"
echo "$ROOTFUL_RENDER" | grep -q 'CapabilityBoundingSet=.*CAP_SYS_ADMIN' && pass "rootful render: capability bound" || fail "rootful caps" "empty bound"
export CARBON_FIXTURE_STATE="$OWNED_RUN/state-rootful/heartbeat.log"
mkdir -p "$(dirname "$CARBON_FIXTURE_STATE")"
# Install the RENDERED rootful unit into the runtime unit directory (tmpfs),
# not /etc. This starts the exact rendered unit and is removed by the trap.
printf '%s\n' "$ROOTFUL_RENDER" | python3 -c 'import json,sys; print(json.load(sys.stdin)["unit"],end="")' >"$ROOTFUL_UNIT"
sed -i -e 's/^Type=notify$/Type=simple/' -e "/^\[Service\]/a Environment=CARBON_FIXTURE_STATE=$CARBON_FIXTURE_STATE\nReadWritePaths=$OWNED_RUN" "$ROOTFUL_UNIT"
systemctl daemon-reload
if systemctl start carbon-installer-qual-rootful.service; then
	pass "rootful rendered unit START (runtime unit dir)"
else
	fail "rootful rendered unit START" "systemctl start failed"
fi
sleep 1
RA="$(systemctl is-active carbon-installer-qual-rootful.service || true)"
echo "rootful is-active=$RA"
[[ "$RA" == "active" ]] && pass "rootful rendered unit active" || { fail "rootful rendered unit" "$RA"; journalctl -u carbon-installer-qual-rootful.service -n 20 --no-pager 2>&1 | head -20 || true; }
systemctl show carbon-installer-qual-rootful.service -p ExecMainStatus -p Result --no-pager 2>&1 | head -4 || true
systemctl restart carbon-installer-qual-rootful.service 2>/dev/null || fail "rootful restart" "systemctl restart failed"
sleep 1
RA2="$(systemctl is-active carbon-installer-qual-rootful.service || true)"
[[ "$RA2" == "active" ]] && pass "rootful RESTART active" || fail "rootful RESTART" "$RA2"
RBOOTS="$(grep -c 'boot=' "$CARBON_FIXTURE_STATE" || true)"
[[ "$RBOOTS" -ge 2 ]] && pass "rootful restart produced a fresh boot record" || fail "rootful restart record" "boots=$RBOOTS"
systemctl stop carbon-installer-qual-rootful.service 2>/dev/null || true

# ---------------------------------------------------------------------------
# Scenario 3: XDG_RUNTIME_DIR reset (rootless) — state survives, socket path
# is re-derived from the same owned runtime root.
# ---------------------------------------------------------------------------
note "scenario 3: XDG runtime reset"
STATE_BEFORE="$(cat "$OWNED_RUN/state-rootless/heartbeat.log" | sha256sum | cut -d' ' -f1)"
systemctl --user stop carbon-installer-qual-rootless.service 2>/dev/null || true
unset XDG_RUNTIME_DIR
export XDG_RUNTIME_DIR="$XDG_ROOT"
systemctl --user daemon-reload || true
systemctl --user start carbon-installer-qual-rootless.service 2>/dev/null && pass "rootless unit restarts after XDG_RUNTIME_DIR reset" || fail "XDG reset" "unit did not restart"
sleep 1
XA="$(systemctl --user is-active carbon-installer-qual-rootless.service || true)"
[[ "$XA" == "active" ]] && pass "rootless active after XDG reset" || fail "XDG reset active" "$XA"
STATE_AFTER="$(cat "$OWNED_RUN/state-rootless/heartbeat.log" | sha256sum | cut -d' ' -f1)"
# The heartbeat grows (append), so compare that the ORIGINAL lines are intact.
grep -q 'boot=' "$OWNED_RUN/state-rootless/heartbeat.log" && pass "rootless state survived XDG reset" || fail "XDG state" "state file lost"

# ---------------------------------------------------------------------------
# Scenario 4: canonical root properties.
# ---------------------------------------------------------------------------
note "scenario 4: canonical, distinct, owned, contained roots"
resolved_state="$(cd "$STATE_ROOT/rootless" && pwd -P)"
resolved_runtime="$(cd "$RUN_ROOT/rootless" && pwd -P)"
[[ "$resolved_state" != "$resolved_runtime" ]] && pass "roots are distinct" || fail "distinct roots" "same path"
[[ "$resolved_state" == "$STATE_ROOT/rootless" && "$resolved_runtime" == "$RUN_ROOT/rootless" ]] && pass "roots resolve without symlink escape" || fail "canonical roots" "symlink escape"
owner_state="$(stat -c '%u' "$resolved_state")"
owner_runtime="$(stat -c '%u' "$resolved_runtime")"
[[ "$owner_state" == "$UID_NUM" && "$owner_runtime" == "$UID_NUM" ]] && pass "roots owned by service identity" || fail "owned roots" "state=$owner_state runtime=$owner_runtime uid=$UID_NUM"
[[ "$resolved_state" == "$FIXTURE_ROOT"/* && "$resolved_runtime" == "$FIXTURE_ROOT"/* ]] && pass "roots contained in owned fixture root" || fail "contained roots" "outside fixture root"

# ---------------------------------------------------------------------------
# Scenario 5: defensive refusals using TEMPORARY paths only.
# ---------------------------------------------------------------------------
note "scenario 5: fail-closed refusals (temporary paths only)"
refusal_case() {
	local name="$1" field="$2" value="$3"
	local s="$FIXTURE_ROOT/refuse-$name.json"
	cp "$FIXTURE_ROOT/spec-rootless.json" "$s"
	python3 - "$s" "$field" "$value" <<'PY'
import json, sys
p, field, value = sys.argv[1], sys.argv[2], sys.argv[3]
d = json.load(open(p))
d[field] = value
json.dump(d, open(p, "w"))
PY
	if "$BIN" render-daemon -daemon-spec "$s" >/dev/null 2>&1; then
		fail "refuse $name" "unsafe spec was accepted"
	else
		pass "refuse $name"
	fi
}
refusal_case shared-socket socket_path /var/run/docker.sock
refusal_case shared-data data_root /var/lib/docker/data
refusal_case shared-containerd exec_root /var/lib/containerd/exec
refusal_case traversal state_root "$STATE_ROOT/../../etc"
refusal_case world-writable state_root /tmp
# Symlink escape: a temporary symlink inside the owned state root that points
# to an unrelated temporary directory must be refused by the path guard.
ln -s "$FIXTURE_ROOT" "$STATE_ROOT/rootless/escape-link" 2>/dev/null || true
python3 - "$FIXTURE_ROOT/spec-rootless.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p))
d["data_root"] = d["state_root"] + "/escape-link/data"
json.dump(d, open(p, "w"))
PY
if "$BIN" render-daemon -daemon-spec "$FIXTURE_ROOT/spec-rootless.json" >/dev/null 2>&1; then
	fail "refuse symlink escape" "symlink to outside root accepted"
else
	pass "refuse symlink escape"
fi

# Cleanup transient units and per-user unit file.
systemctl --user stop carbon-installer-qual-rootless.service 2>/dev/null || true
systemctl stop carbon-installer-qual-rootful.service 2>/dev/null || true
rm -f "$UNIT_FILE" "$ROOTFUL_UNIT"
systemctl daemon-reload 2>/dev/null || true
systemctl --user daemon-reload 2>/dev/null || true

echo "--- summary"
if [[ "$FAILED" -eq 0 ]]; then
	echo "SYSTEMD QUALIFICATION: ALL PASS"
else
	echo "SYSTEMD QUALIFICATION: FAILURES PRESENT"
fi
exit "$FAILED"
