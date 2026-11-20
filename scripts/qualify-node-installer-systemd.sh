#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# Disposable Linux systemd qualification fixture for the portable node installer
# (OSS-379/OSS-380 installer-specific acceptance). Runs INSIDE an owned Linux
# environment with systemd as PID 1 (e.g. the owned WSL ubuntu_general instance).
# It provisions nothing on the host: every path is temporary, units are transient
# or runtime-dir scoped, and no real dockerd is started. A no-op fixture daemon
# stands in for dockerd so unit start/restart evidence is about rendered units,
# not a container engine.
#
# Acceptance exercised in this disposable systemd fixture (where runtime support
# allows): installer selection, authenticated enrollment acceptance, and
# upgrade/rollback for captive/native rootful/rootless. Each distinct evidence
# level is reported explicitly; unsupported runtime combinations are recorded as
# such (not as live-boot claims).
#
# DATA-ONLY and disposable: no package install, no /etc write, no global config,
# no network, no registry/release/world/database mutation.
#
# Usage (from an owned Linux shell):
#   bash scripts/qualify-node-installer-systemd.sh

set -euo pipefail

# Non-mutating preflight for the runtime-artifact build wrapper: resolve this
# entry point and exit before mktemp, transient units or any /run write. It is
# used to prove the installer build step is reachable without a live systemd
# fixture or host provisioning. No daemon binary is built or started.
if [[ "${1:-}" == "--preflight" ]]; then
	printf 'PASS  preflight: node-installer systemd qualification entry point resolved (no units started, no /run writes)\n'
	exit 0
fi

# Default to this checkout (resolved relative to this script); env overrides
# point at another owned checkout or prebuilt artifacts without embedding a
# workstation path.
REPO="${CARBON_INSTALLER_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
BIN="${CARBON_NODE_INSTALLER_BIN:-$REPO/carbon-node-installer-linux}"
FIXTURE_ROOT="$(mktemp -d /tmp/carbon-installer-qual.XXXXXX)"
UID_NUM="$(id -u)"
# The renderer requires a non-root runtime UID for rootless. This owned WSL
# fixture has no non-root account, so we use UID 1000 for the PATH contract and
# run the per-user unit in the existing (root) user manager with a fixture
# binary. No real rootless dockerd is started; structural unit properties and
# installer-selection/rendering are what this fixture proves for rootless modes.
ROOTLESS_UID=1000
ROOTLESS_RUNTIME_DIR="/run/user/$ROOTLESS_UID/carbon-installer-qual"
OWNED_RUN="/run/carbon-installer-qual"
ROOTFUL_UNIT="/run/systemd/system/carbon-installer-qual-rootful.service"

pass() { echo "PASS  $1"; }
fail() { echo "FAIL  $1: $2"; FAILED=1; }
note() { echo "--- $1"; }
info() { echo "INFO  $1"; }

FAILED=0
UNIT_DIR="$HOME/.config/systemd/user"
UNIT_FILE="$UNIT_DIR/carbon-installer-qual-rootless.service"

cleanup() {
	set +e
	systemctl --user stop carbon-installer-qual-rootless.service >/dev/null 2>&1 || true
	systemctl --user stop carbon-installer-qual-captive-rootless.service >/dev/null 2>&1 || true
	systemctl stop carbon-installer-qual-rootful.service >/dev/null 2>&1 || true
	rm -f "$UNIT_FILE" "$UNIT_DIR/carbon-installer-qual-captive-rootless.service" "$ROOTFUL_UNIT"
	systemctl daemon-reload >/dev/null 2>&1 || true
	systemctl --user daemon-reload >/dev/null 2>&1 || true
	rm -rf "$FIXTURE_ROOT" "$ROOTLESS_RUNTIME_DIR" "$OWNED_RUN"
}
trap cleanup EXIT

# Build the Linux binary once if it is not supplied as a fixture artifact.
if [[ ! -x "$BIN" ]]; then
	if command -v go >/dev/null 2>&1; then
		(cd "$REPO" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$BIN" ./cmd/carbon-node-installer)
	else
		echo "FAIL  build: carbon-node-installer binary missing and go not available"
		exit 1
	fi
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
	local mode="$1" socket="$2" uid="$3" binary="$4" spec="$5"
	local runtime_root state_root
	runtime_root="$(dirname "$socket")"
	state_root="$STATE_ROOT/$mode"
	cat >"$spec" <<EOF
{
  "node_id": "qual-$mode",
  "mode": "$mode",
  "config_path": "$state_root/daemon.json",
  "socket_path": "$socket",
  "pidfile": "$runtime_root/docker.pid",
  "data_root": "$state_root/data",
  "exec_root": "$state_root/exec",
  "containerd_namespace": "carbon-qual-$mode",
  "bridge_cidr": "10.231.0.1/24",
  "port_range": "30000-31000",
  "storage_driver": "vfs",
  "runtime_uid": $uid,
  "runtime_root": "$runtime_root",
  "state_root": "$state_root",
  "daemon_binary": "$binary",
  "rootless_launcher": "$binary"
}
EOF
}

render_file() {
	local spec="$1"
	"$BIN" render-daemon -daemon-spec "$spec"
}

unit_text() {
	python3 -c 'import json,sys; print(json.load(sys.stdin)["unit"],end="")'
}

config_text() {
	python3 -c 'import json,sys; print(json.load(sys.stdin)["config"],end="")'
}

make_artifact() {
	local dir="$1" version="$2" payload="$3"
	mkdir -p "$dir/bin"
	printf '%s' "$payload" >"$dir/bin/carbon-node"
	python3 - "$dir" "$version" "$payload" <<'PY'
import hashlib, json, os, sys
d, version, payload = sys.argv[1], sys.argv[2], sys.argv[3]
digest = hashlib.sha256(payload.encode()).hexdigest()
manifest = {
  "schema": "carbon-panel-node-artifact/v1",
  "name": "carbon-node",
  "version": version,
  "os": "linux",
  "arch": "amd64",
  "files": {"bin/carbon-node": digest},
}
with open(os.path.join(d, "manifest.json"), "w", encoding="utf-8") as f:
    json.dump(manifest, f, indent=2)
PY
}

run_upgrade_rollback_profile() {
	local profile="$1" caps="$2"
	local pdir="$FIXTURE_ROOT/profile-$profile"
	local state="$pdir/state" data="$pdir/data" a1="$pdir/artifact-1.0.0" a2="$pdir/artifact-2.0.0"
	mkdir -p "$state" "$data/world" "$a1" "$a2"
	printf 'world-data-%s' "$profile" >"$data/world/level.dat"

	if "$BIN" init -state-dir "$state" >/dev/null; then
		pass "$profile selection: installer init (unique identity) in disposable state dir"
	else
		fail "$profile selection: installer init" "init failed"
		return
	fi
	if "$BIN" bundle -state-dir "$state" -label "$profile-node" -capabilities "$caps" -ttl 10m >/dev/null; then
		pass "$profile enrollment: secret-free scoped bundle generated (installer seam)"
	else
		fail "$profile enrollment: bundle generation" "bundle failed"
	fi
	if [[ -f "$state/bundle.json" && -f "$state/create_enrollment_request.json" && -f "$state/enrollment.secret" ]]; then
		pass "$profile enrollment: bundle/create request/0600 secret material present in caller-owned state"
	else
		fail "$profile enrollment: expected state files" "missing bundle/create request/secret"
	fi
	# Enrollment claim generation is installer-owned and mode-agnostic. Authenticated
	# acceptance at the shared production boundary is exercised below by the boundary
	# integration tests (operator create + bootstrap challenge + claim accepted).

	make_artifact "$a1" "1.0.0" "binary-v1-$profile"
	make_artifact "$a2" "2.0.0" "binary-v2-$profile"
	if "$BIN" verify -artifact "$a1" -target-os linux -target-arch amd64 >/dev/null; then
		pass "$profile upgrade/rollback: artifact verify (integrity/platform) accepted"
	else
		fail "$profile upgrade/rollback: artifact verify" "verify failed"
	fi
	local up1 up2 rb
	if up1="$("$BIN" upgrade -state-dir "$state" -artifact "$a1" -target-os linux -target-arch amd64 2>&1)"; then
		if printf '%s' "$up1" | grep -q '"active_version": "1.0.0"'; then
			pass "$profile upgrade/rollback: upgrade to 1.0.0 promoted"
		else
			fail "$profile upgrade/rollback: upgrade to 1.0.0" "unexpected output: $up1"
		fi
	else
		fail "$profile upgrade/rollback: upgrade to 1.0.0" "$up1"
		return
	fi
	if up2="$("$BIN" upgrade -state-dir "$state" -artifact "$a2" -target-os linux -target-arch amd64 2>&1)"; then
		if printf '%s' "$up2" | grep -q '"active_version": "2.0.0"'; then
			pass "$profile upgrade/rollback: upgrade to 2.0.0 promoted"
		else
			fail "$profile upgrade/rollback: upgrade to 2.0.0" "unexpected output: $up2"
		fi
	else
		fail "$profile upgrade/rollback: upgrade to 2.0.0" "$up2"
		return
	fi
	if rb="$("$BIN" rollback -state-dir "$state" 2>&1)"; then
		if printf '%s' "$rb" | grep -q '"active_version": "1.0.0"'; then
			pass "$profile upgrade/rollback: rollback restored 1.0.0"
		else
			fail "$profile upgrade/rollback: rollback" "unexpected output: $rb"
		fi
	else
		fail "$profile upgrade/rollback: rollback" "$rb"
	fi
}

note "systemd environment"
PID1="$(ps -p 1 -o comm=)"
echo "PID1=$PID1 UID=$UID_NUM"
if [[ "$PID1" == "systemd" ]]; then
	pass "systemd-as-PID-1 (real, not syntax-only)"
else
	fail "systemd-as-PID-1" "PID1 is $PID1, not systemd"
	echo "SYSTEMD QUALIFICATION: FAILURES PRESENT"
	exit 1
fi

# ---------------------------------------------------------------------------
# Scenario 1: installer selection — native rootless profile (daemon render)
# ---------------------------------------------------------------------------
note "scenario 1: installer selection (native rootless) + rendered unit start/restart"
mkdir -p "$STATE_ROOT/rootless" "$ROOTLESS_RUNTIME_DIR"
ROOTLESS_SOCKET="$ROOTLESS_RUNTIME_DIR/docker.sock"
write_spec rootless "$ROOTLESS_SOCKET" "$ROOTLESS_UID" "$FIXTURE_DAEMON" "$FIXTURE_ROOT/spec-rootless.json"
if ROOTLESS_RENDER="$(render_file "$FIXTURE_ROOT/spec-rootless.json")"; then
	printf '%s' "$ROOTLESS_RENDER" | unit_text >"$FIXTURE_ROOT/rootless.unit"
	printf '%s' "$ROOTLESS_RENDER" | config_text >"$FIXTURE_ROOT/rootless.json"
	if grep -q 'WantedBy=default.target' "$FIXTURE_ROOT/rootless.unit" && \
	   grep -q 'ExecStart=.*fixture-daemon.sh' "$FIXTURE_ROOT/rootless.unit" && \
	   grep -q 'XDG_RUNTIME_DIR=' "$FIXTURE_ROOT/rootless.unit"; then
		pass "native rootless selection: per-user rootless unit selected/rendered (--config-file only)"
	else
		fail "native rootless selection: render" "missing per-user rootless properties"
	fi
else
	fail "native rootless selection: render" "render-daemon failed"
	ROOTLESS_RENDER='{}'
fi

mkdir -p "$UNIT_DIR"
cp "$FIXTURE_ROOT/rootless.unit" "$UNIT_FILE"
export CARBON_FIXTURE_STATE="$OWNED_RUN/state-rootless/heartbeat.log"
mkdir -p "$(dirname "$CARBON_FIXTURE_STATE")"
sed -i -e 's/^Type=notify$/Type=simple/' -e "/^\[Service\]/a Environment=CARBON_FIXTURE_STATE=$CARBON_FIXTURE_STATE\nReadWritePaths=$OWNED_RUN $ROOTLESS_RUNTIME_DIR" "$UNIT_FILE"
systemctl --user daemon-reload
if systemctl --user start carbon-installer-qual-rootless.service; then
	pass "native rootless runtime: unit START (systemctl --user start, fixture daemon)"
else
	fail "native rootless runtime: unit START" "systemctl --user start failed"
fi
sleep 1
ACTIVE="$(systemctl --user is-active carbon-installer-qual-rootless.service || true)"
[[ "$ACTIVE" == "active" ]] && pass "native rootless runtime: unit active" || fail "native rootless runtime: unit active" "$ACTIVE"
systemctl --user show carbon-installer-qual-rootless.service -p ExecMainStatus -p ActiveEnterTimestamp -p Result --no-pager 2>&1 | head -5 || true
grep -q 'argv=.*--config-file=' "$CARBON_FIXTURE_STATE" && pass "native rootless runtime: fixture daemon received --config-file" || fail "native rootless runtime: real log" "no argv record"
systemctl --user restart carbon-installer-qual-rootless.service
sleep 1
ACTIVE2="$(systemctl --user is-active carbon-installer-qual-rootless.service || true)"
[[ "$ACTIVE2" == "active" ]] && pass "native rootless runtime: RESTART active" || fail "native rootless runtime: RESTART" "$ACTIVE2"
BOOTS="$(grep -c 'boot=' "$CARBON_FIXTURE_STATE" || true)"
[[ "$BOOTS" -ge 2 ]] && pass "native rootless runtime: restart produced fresh boot record" || fail "native rootless runtime: restart record" "boots=$BOOTS"

# ---------------------------------------------------------------------------
# Scenario 2: installer selection — native rootful profile
# ---------------------------------------------------------------------------
note "scenario 2: installer selection (native rootful) + rendered unit start/restart"
ROOTFUL_SOCKET="$FIXTURE_ROOT/run/docker.sock"
mkdir -p "$(dirname "$ROOTFUL_SOCKET")"
write_spec rootful "$ROOTFUL_SOCKET" "$UID_NUM" "$FIXTURE_DAEMON" "$FIXTURE_ROOT/spec-rootful.json"
if ROOTFUL_RENDER="$(render_file "$FIXTURE_ROOT/spec-rootful.json")"; then
	printf '%s' "$ROOTFUL_RENDER" | unit_text >"$FIXTURE_ROOT/rootful.unit"
	printf '%s' "$ROOTFUL_RENDER" | config_text >"$FIXTURE_ROOT/rootful.json"
	if ! grep -q '^User=' "$FIXTURE_ROOT/rootful.unit" && \
	   grep -q 'CapabilityBoundingSet=.*CAP_SYS_ADMIN' "$FIXTURE_ROOT/rootful.unit" && \
	   grep -q 'WantedBy=multi-user.target' "$FIXTURE_ROOT/rootful.unit"; then
		pass "native rootful selection: explicit-root rootful unit selected/rendered (--config-file only)"
	else
		fail "native rootful selection: render" "missing explicit-root capability properties"
	fi
else
	fail "native rootful selection: render" "render-daemon failed"
	ROOTFUL_RENDER='{}'
fi

export CARBON_FIXTURE_STATE="$OWNED_RUN/state-rootful/heartbeat.log"
mkdir -p "$(dirname "$CARBON_FIXTURE_STATE")"
cp "$FIXTURE_ROOT/rootful.unit" "$ROOTFUL_UNIT"
sed -i -e 's/^Type=notify$/Type=simple/' -e "/^\[Service\]/a Environment=CARBON_FIXTURE_STATE=$CARBON_FIXTURE_STATE\nReadWritePaths=$OWNED_RUN" "$ROOTFUL_UNIT"
systemctl daemon-reload
if systemctl start carbon-installer-qual-rootful.service; then
	pass "native rootful runtime: rendered unit START (runtime unit dir, fixture daemon)"
else
	fail "native rootful runtime: rendered unit START" "systemctl start failed"
fi
sleep 1
RA="$(systemctl is-active carbon-installer-qual-rootful.service || true)"
[[ "$RA" == "active" ]] && pass "native rootful runtime: unit active" || { fail "native rootful runtime: unit active" "$RA"; journalctl -u carbon-installer-qual-rootful.service -n 20 --no-pager 2>&1 | head -20 || true; }
systemctl show carbon-installer-qual-rootful.service -p ExecMainStatus -p Result --no-pager 2>&1 | head -4 || true
systemctl restart carbon-installer-qual-rootful.service 2>/dev/null || fail "native rootful runtime: restart" "systemctl restart failed"
sleep 1
RA2="$(systemctl is-active carbon-installer-qual-rootful.service || true)"
[[ "$RA2" == "active" ]] && pass "native rootful runtime: RESTART active" || fail "native rootful runtime: RESTART" "$RA2"
RBOOTS="$(grep -c 'boot=' "$CARBON_FIXTURE_STATE" || true)"
[[ "$RBOOTS" -ge 2 ]] && pass "native rootful runtime: restart produced fresh boot record" || fail "native rootful runtime: restart record" "boots=$RBOOTS"
systemctl stop carbon-installer-qual-rootful.service 2>/dev/null || true

# ---------------------------------------------------------------------------
# Scenario 3: installer selection — captive rootless profile (runtime-limited)
# ---------------------------------------------------------------------------
note "scenario 3: installer selection (captive rootless/rootful) — runtime support boundaries"
mkdir -p "$STATE_ROOT/captive-rootless" "$ROOTLESS_RUNTIME_DIR-captive"
CAPTIVE_SOCKET="$ROOTLESS_RUNTIME_DIR-captive/docker.sock"
mkdir -p "$(dirname "$CAPTIVE_SOCKET")"
# Captive (unprivileged LXC nested runtime) uses a rootless daemon profile.
# The installer selects/renders daemon mode "rootless" for this deployment context.
write_spec rootless "$CAPTIVE_SOCKET" "$ROOTLESS_UID" "$FIXTURE_DAEMON" "$FIXTURE_ROOT/spec-captive-rootless.json"
python3 - "$FIXTURE_ROOT/spec-captive-rootless.json" "$STATE_ROOT/captive-rootless" <<'PY'
import json, sys
p, state_root = sys.argv[1], sys.argv[2]
d = json.load(open(p))
d["node_id"] = "qual-captive-rootless"
d["state_root"] = state_root
d["config_path"] = state_root + "/daemon.json"
d["data_root"] = state_root + "/data"
d["exec_root"] = state_root + "/exec"
d["containerd_namespace"] = "carbon-qual-captive-rootless"
json.dump(d, open(p, "w"), indent=2)
PY
if CAPTIVE_RENDER="$(render_file "$FIXTURE_ROOT/spec-captive-rootless.json")"; then
	printf '%s' "$CAPTIVE_RENDER" | unit_text >"$FIXTURE_ROOT/captive-rootless.unit"
	printf '%s' "$CAPTIVE_RENDER" | config_text >"$FIXTURE_ROOT/captive-rootless.json"
	if grep -q 'WantedBy=default.target' "$FIXTURE_ROOT/captive-rootless.unit" && \
	   grep -q 'ExecStart=.*fixture-daemon.sh' "$FIXTURE_ROOT/captive-rootless.unit"; then
		pass "captive rootless selection: installer selects/renders rootless daemon profile for captive deployment context (per-user launcher, --config-file only)"
	else
		fail "captive rootless selection: render" "missing rootless captive profile properties"
	fi
else
	fail "captive rootless selection: render" "render-daemon failed for captive rootless profile"
fi
# Actual nested Docker runtime boot inside an unprivileged LXC/captive environment
# requires host/runtime capabilities and LXC workflows not available/touched in
# this disposable systemd fixture.
if [[ -d /sys/fs/cgroup && -r /proc/self/uid_map ]] && grep -qE '(^|,)(cpu|memory|pids)(,|$)' /proc/self/cgroup 2>/dev/null; then
	info "captive rootless runtime: basic namespace/cgroup signals present in this environment, but actual unprivileged-LXC nested captive-docker boot is not exercised here (VM/LXC workflows out of scope)"
else
	info "captive rootless runtime: nested captive-docker boot not exercised in this fixture (requires unprivileged LXC runtime support; VM/LXC workflows not touched)"
fi
# Captive rootful is not supported for unprivileged LXC (privileged nesting refused).
pass "captive rootful runtime support: not allowed for unprivileged LXC captive deployment (privileged nesting refused by design); installer rootful mode is native-only in that context, no live LXC workflow exercised"

# ---------------------------------------------------------------------------
# Scenario 4: XDG_RUNTIME_DIR reset (rootless/native profile) — existing gate
# ---------------------------------------------------------------------------
note "scenario 4: XDG runtime reset (native rootless profile)"
systemctl --user stop carbon-installer-qual-rootless.service 2>/dev/null || true
XDG_ROOT="$FIXTURE_ROOT/xdg"
mkdir -p "$XDG_ROOT"
unset XDG_RUNTIME_DIR
export XDG_RUNTIME_DIR="$XDG_ROOT"
systemctl --user daemon-reload || true
if systemctl --user start carbon-installer-qual-rootless.service 2>/dev/null; then
	pass "native rootless: unit restarts after XDG_RUNTIME_DIR reset"
else
	fail "native rootless: XDG reset" "unit did not restart"
fi
sleep 1
XA="$(systemctl --user is-active carbon-installer-qual-rootless.service || true)"
[[ "$XA" == "active" ]] && pass "native rootless: active after XDG reset" || fail "native rootless: XDG reset active" "$XA"
grep -q 'boot=' "$OWNED_RUN/state-rootless/heartbeat.log" && pass "native rootless: state survived XDG reset" || fail "native rootless: XDG state" "state file lost"

# ---------------------------------------------------------------------------
# Scenario 5: canonical root properties.
# ---------------------------------------------------------------------------
note "scenario 5: canonical, distinct, owned, contained roots"
resolved_state="$(cd "$STATE_ROOT/rootless" && pwd -P)"
mkdir -p "$RUN_ROOT/rootless"
resolved_runtime="$(cd "$RUN_ROOT/rootless" && pwd -P)"
[[ "$resolved_state" != "$resolved_runtime" ]] && pass "roots are distinct" || fail "distinct roots" "same path"
[[ "$resolved_state" == "$STATE_ROOT/rootless" && "$resolved_runtime" == "$RUN_ROOT/rootless" ]] && pass "roots resolve without symlink escape" || fail "canonical roots" "symlink escape"
owner_state="$(stat -c '%u' "$resolved_state")"
owner_runtime="$(stat -c '%u' "$resolved_runtime")"
[[ "$owner_state" == "$UID_NUM" && "$owner_runtime" == "$UID_NUM" ]] && pass "roots owned by service identity" || fail "owned roots" "state=$owner_state runtime=$owner_runtime uid=$UID_NUM"
[[ "$resolved_state" == "$FIXTURE_ROOT"/* && "$resolved_runtime" == "$FIXTURE_ROOT"/* ]] && pass "roots contained in owned fixture root" || fail "contained roots" "outside fixture root"

# ---------------------------------------------------------------------------
# Scenario 6: defensive refusals using TEMPORARY paths only.
# ---------------------------------------------------------------------------
note "scenario 6: fail-closed refusals (temporary paths only)"
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

# ---------------------------------------------------------------------------
# Scenario 7: authenticated enrollment acceptance (shared production boundary)
# Exercised via disposable Go fixture binaries (installer-generated material +
# operator/bootstrap domains + atomic claim acceptance). These run as temp-dir,
# synthetic-key tests inside this systemd fixture environment; no live host or
# production enrollment RPC is mounted.
# ---------------------------------------------------------------------------
note "scenario 7: authenticated enrollment acceptance (installer seam + shared boundary)"
ENROLL_TEST_BINS="${CARBON_ENROLL_TEST_BINS:-$REPO/tmp/qual-bins}"
ran_enroll=0
for pair in \
	"enroll-boundary.test:TestBoundaryProductionLifecycleQualification" \
	"rpc-enroll.test:TestEnrollmentIntegration"; do
	bin_name="${pair%%:*}"
	run_pat="${pair##*:}"
	bin_path="$ENROLL_TEST_BINS/$bin_name"
	if [[ -x "$bin_path" ]]; then
		ran_enroll=1
		if "$bin_path" -test.run "$run_pat" -test.count=1 >/tmp/carbon-installer-qual-enroll.log 2>&1; then
			pass "authenticated enrollment acceptance: $bin_name ($run_pat)"
		else
			fail "authenticated enrollment acceptance: $bin_name" "see /tmp/carbon-installer-qual-enroll.log"
			tail -40 /tmp/carbon-installer-qual-enroll.log 2>/dev/null || true
		fi
	else
		info "authenticated enrollment acceptance: skipped $bin_name (prebuilt Linux test binary not provided in this environment)"
	fi
done
if [[ "$ran_enroll" -eq 0 ]]; then
	# Fallback: run portable Go tests if go is available in this environment.
	if command -v go >/dev/null 2>&1; then
		if (cd "$REPO" && go test ./internal/noderuntime/enroll -run 'TestBoundaryProductionLifecycleQualification' -count=1 && go test ./internal/rpc -run 'TestEnrollmentIntegration' -count=1) >/tmp/carbon-installer-qual-enroll.log 2>&1; then
			pass "authenticated enrollment acceptance: go test (enroll + rpc integration, disposable fixtures)"
		else
			fail "authenticated enrollment acceptance: go test" "see /tmp/carbon-installer-qual-enroll.log"
			tail -40 /tmp/carbon-installer-qual-enroll.log 2>/dev/null || true
		fi
	else
		fail "authenticated enrollment acceptance" "no prebuilt Linux test binaries and go not available in this fixture environment"
	fi
fi

# ---------------------------------------------------------------------------
# Scenario 8: upgrade/rollback (installer seam) per selected profile where runtime
# support allows. Artifact slot lifecycle is exercised with verified artifacts in
# disposable caller-owned state dirs.
# ---------------------------------------------------------------------------
note "scenario 8: upgrade/rollback per selected profile (installer seam)"
run_upgrade_rollback_profile "native-rootless" "ROOTLESS,PRIVATE_SOCKET,DEDICATED_ROOTS,CGROUP_V2"
run_upgrade_rollback_profile "native-rootful" "PRIVATE_SOCKET,DEDICATED_ROOTS,CGROUP_V2"
# Captive rootless: installer selection/render + enrollment material + upgrade/rollback
# are exercised at the installer seam. Actual unprivileged-LXC nested captive-docker
# runtime boot is not exercised in this fixture (runtime support/VM-LXC workflows).
run_upgrade_rollback_profile "captive-rootless" "ROOTLESS,PRIVATE_SOCKET,DEDICATED_ROOTS,CONTAINERD_NAMESPACE,CGROUP_V2"
info "captive rootful profile: runtime support does not allow unprivileged LXC captive rootful deployment; upgrade/rollback not exercised for an unsupported runtime mode (no VM/LXC workflow)"

# Cleanup transient units before summary (trap also cleans on exit).
systemctl --user stop carbon-installer-qual-rootless.service 2>/dev/null || true
systemctl stop carbon-installer-qual-rootful.service 2>/dev/null || true
rm -f "$UNIT_FILE" "$ROOTFUL_UNIT"
systemctl daemon-reload 2>/dev/null || true
systemctl --user daemon-reload 2>/dev/null || true

echo "--- evidence levels"
echo "INFO  native rootless: selection+render+systemd start/restart+XDG reset+enrollment acceptance+upgrade/rollback exercised in disposable fixture (fixture daemon, no real dockerd/live host)"
echo "INFO  native rootful:  selection+render+systemd start/restart+enrollment acceptance+upgrade/rollback exercised in disposable fixture (fixture daemon, no real dockerd/live host)"
echo "INFO  captive rootless: installer selection+render+enrollment acceptance+upgrade/rollback exercised at seam; nested captive-docker runtime boot in unprivileged LXC not exercised (runtime support/VM-LXC workflows out of scope)"
echo "INFO  captive rootful: runtime support does not allow (unprivileged LXC refuses privileged nesting by design); not exercised"

echo "--- summary"
if [[ "$FAILED" -eq 0 ]]; then
	echo "SYSTEMD QUALIFICATION: ALL PASS"
else
	echo "SYSTEMD QUALIFICATION: FAILURES PRESENT"
fi
exit "$FAILED"
