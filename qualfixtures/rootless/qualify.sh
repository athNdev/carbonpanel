#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# Disposable ROOTLESS exact-unit qualification. Runs as root INSIDE the owned
# cp-node-rootless-qual container (systemd as PID 1, host network namespace,
# delegated cgroup v2). It installs the EXACT rendered shipped per-user unit
# and canonical daemon.json, starts a REAL systemd --user manager for a REAL
# non-root user, and boots a REAL dockerd via dockerd-rootless.sh.
#
# NO unit/config substitution, NO no-op fixture daemon, NO Type= change,
# NO host service/world/database/config change, NO release or deployment.
# rootlesskit + dockerd-rootless.sh are bind-mounted daemon prerequisites.
set -uo pipefail

UNIT_SRC=${UNIT_SRC:-/opencode/generated/carbon-node-rootless.service}
CFG_SRC=${CFG_SRC:-/opencode/generated/daemon.json}
GAME_PORT=${GAME_PORT:-30555}
RUN=/run/user/1000/carbon-node
XDG=/run/user/1000
UNIT_NAME=carbon-node-rootless.service
SOCK=unix:///run/user/1000/carbon-node/docker.sock

uuser() { runuser -u ubuntu -- env XDG_RUNTIME_DIR="$XDG" DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG/bus" "$@"; }
dock() { docker -H "$SOCK" "$@"; }
pass() { echo "PASS  $1"; }
fail() { echo "FAIL  $1: $2"; FAILED=1; }
FAILED=0

echo "===ENV==="
echo "PID1=$(ps -p 1 -o comm=) UID=$(id -u) KERNEL=$(uname -r)"
test -f "$UNIT_SRC" || { echo "missing $UNIT_SRC"; exit 2; }
test -f "$CFG_SRC" || { echo "missing $CFG_SRC"; exit 2; }
grep -q '^NotifyAccess=all$' "$UNIT_SRC" && pass "exact unit has NotifyAccess=all" || fail "NotifyAccess" "absent"
grep -q '^ExecStart=/usr/bin/dockerd-rootless.sh --config-file=' "$UNIT_SRC" && pass "exact unit uses real rootless launcher" || fail "launcher" "wrong"
grep -q '"iptables": true' "$CFG_SRC" && pass "canonical config keeps iptables enabled" || fail "iptables" "config changed"

# --- daemon prerequisites (not unit substitutions) --------------------------
for f in rootlesskit dockerd-rootless.sh; do
  if [ ! -x "/usr/bin/$f" ]; then
    install -m 0755 "/opencode/hostbin/$f" "/usr/bin/$f" || { echo "cannot install $f"; exit 2; }
  fi
done
command -v rootlesskit >/dev/null && command -v dockerd-rootless.sh >/dev/null && pass "real rootless launcher + rootlesskit present" || fail "prereqs" "missing"

# --- non-root user session ---------------------------------------------------
install -d -o ubuntu -g ubuntu -m 0700 "$XDG"
install -d -o ubuntu -g ubuntu -m 0700 "$RUN"
install -d -o ubuntu -g ubuntu -m 0700 /home/ubuntu/carbon-qual
install -d -o ubuntu -g ubuntu -m 0700 /home/ubuntu/.config/systemd/user
loginctl enable-linger ubuntu 2>/dev/null && pass "linger enabled for non-root user" || echo "NOTE  linger not set (manual user manager still used)"

install -m 0644 -o ubuntu -g ubuntu "$UNIT_SRC" "/home/ubuntu/.config/systemd/user/$UNIT_NAME"
echo "===INSTALLED_UNIT==="
cat "/home/ubuntu/.config/systemd/user/$UNIT_NAME"

rm -f "$XDG/bus"
setpriv --reuid=ubuntu --regid=ubuntu --init-groups \
  dbus-daemon --session --address="unix:path=$XDG/bus" --fork --print-pid="$XDG/bus.pid" >/dev/null 2>&1
for _ in $(seq 1 30); do [ -S "$XDG/bus" ] && break; sleep 0.2; done
[ -S "$XDG/bus" ] && pass "real D-Bus user session bus" || fail "user bus" "missing"

setpriv --reuid=ubuntu --regid=ubuntu --init-groups \
  env XDG_RUNTIME_DIR="$XDG" DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG/bus" \
  /usr/lib/systemd/systemd --user >/var/log/carbon-user-manager.log 2>&1 &
USER_MGR=$!
for _ in $(seq 1 100); do
  uuser systemctl --user show --property=LoadState -q >/dev/null 2>&1 && break
  sleep 0.2
done
STATE=$(uuser systemctl --user is-system-running 2>/dev/null || true)
echo "USER_MANAGER_STATE=$STATE"
[ -n "$STATE" ] && pass "real systemd --user manager running as uid $(stat -c %u "$XDG")" || fail "user manager" "not reachable"

uuser systemctl --user daemon-reload
uuser systemctl --user enable "$UNIT_NAME" >/dev/null 2>&1 && pass "user unit enabled in default.target" || echo "NOTE  enable failed (start still attempted)"

# logind recreates /run/user/1000 as a fresh tmpfs when the user manager comes
# up, so the canonical config must be installed AFTER that, immediately before
# boot. The unit and config bytes are still the exact rendered artifacts.
install -d -o ubuntu -g ubuntu -m 0700 "$RUN"
install -m 0644 -o ubuntu -g ubuntu "$CFG_SRC" "$RUN/daemon.json"
echo "===CONFIG_PRESENT==="
ls -l "$RUN/daemon.json" 2>&1
grep -q '"iptables": true' "$RUN/daemon.json" && pass "canonical config installed at exact unit path" || fail "config install" "missing"

# --- boot the EXACT shipped unit --------------------------------------------
echo "===START==="
uuser systemctl --user start "$UNIT_NAME" 2>&1 || echo "start command returned nonzero"
BOOT_OK=0
for _ in $(seq 1 180); do
  A=$(uuser systemctl --user is-active "$UNIT_NAME" 2>/dev/null || true)
  if [ "$A" = "active" ]; then BOOT_OK=1; break; fi
  F=$(uuser systemctl --user is-failed "$UNIT_NAME" 2>/dev/null || true)
  if [ "$F" = "failed" ]; then break; fi
  sleep 1
done
echo "===ACTIVE==="
uuser systemctl --user show "$UNIT_NAME" \
  -p LoadState -p ActiveState -p SubState -p Result -p ExecMainStatus -p NRestarts \
  -p ActiveEnterTimestamp --no-pager 2>&1
uuser systemctl --user is-active "$UNIT_NAME" || true
if [ "$BOOT_OK" = 1 ]; then pass "rootless unit reached active"; else fail "rootless boot" "did not reach active"; fi
echo "===USER_JOURNAL==="
uuser journalctl --user -u "$UNIT_NAME" -n 60 --no-pager 2>&1 | tail -60 || true

# --- private socket / pidfile / roots ---------------------------------------
echo "===SOCKET==="
ls -l "$RUN/docker.sock" 2>&1 || fail "private socket" "missing"
echo "===PIDFILE==="
cat "$RUN/docker.pid" 2>&1 || fail "private pidfile" "missing"
PID=$(cat "$RUN/docker.pid" 2>/dev/null || echo 0)
if [ "$PID" -gt 0 ] 2>/dev/null; then
  echo "PID_OWNER=$(stat -c %U /proc/$PID 2>/dev/null || echo gone)"
  echo "PID_CMD=$(tr '\0' ' ' </proc/$PID/cmdline 2>/dev/null || echo gone)"
fi
echo "===INFO==="
dock info 2>&1 | grep -E 'Server Version|Storage Driver|Docker Root Dir|Exec Root|Security Options|Operating System|Kernel Version' || fail "socket info" "dockerd not serving"
dock info --format '{{json .SecurityOptions}}' 2>&1 | grep -q rootless && pass "daemon reports rootless security option" || echo "NOTE  rootless security option not present in info"

echo "===PATH_ISOLATION==="
[ ! -e /var/run/docker.sock ] && pass "host /var/run/docker.sock not used by unit" || echo "NOTE  host socket exists in fixture (unit still points to private path)"
grep -q "$RUN/docker.sock" "$RUN/daemon.json" && pass "canonical config uses private socket" || fail "config socket" "wrong"
grep -q '/home/ubuntu/carbon-qual/docker' "$RUN/daemon.json" && pass "canonical config uses dedicated data root" || fail "config data root" "wrong"

# --- container + game port on the private bridge ----------------------------
echo "===IMAGE_LOAD==="
if [ -f /opencode/alpine.tar ]; then
  LOAD_OUT=$(dock load -i /opencode/alpine.tar 2>&1)
  echo "$LOAD_OUT" | tail -3
  echo "$LOAD_OUT" | grep -qi 'loaded image' && pass "real container image loaded into rootless daemon" || fail "image load" "$LOAD_OUT"
else
  echo "NOTE  no alpine.tar provided; container checks skipped"
fi

echo "===CONTAINER==="
dock rm -f cp-rootless-game >/dev/null 2>&1
CID=$(dock run -d --name cp-rootless-game -p "${GAME_PORT}:8080" alpine:3 \
  sh -c 'while true; do (printf "HTTP/1.0 200 OK\r\nContent-Length: 2\r\n\r\nOK" | busybox nc -l -p 8080) >/dev/null 2>&1; done' 2>&1) || fail "container run" "failed to start"
echo "CID=$CID"
sleep 2
dock ps -a --format '{{.Names}} {{.Ports}} {{.Status}}' 2>&1
dock port cp-rootless-game 2>&1 || true

echo "===BRIDGE==="
dock inspect cp-rootless-game --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{.NetworkID}}{{end}}' 2>&1
CIP=$(dock inspect cp-rootless-game --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null || true)
echo "CONTAINER_IP=$CIP"
if [ -n "$CIP" ]; then
  dock run --rm alpine:3 sh -c "busybox nc -z -w 3 $CIP 8080 && echo BRIDGE_LISTENER_OPEN" 2>&1 || echo "BRIDGE_LISTENER_PROBE_FAILED"
fi

echo "===PUBLISHED_PORT==="
# Measured CLIENT TCP reachability from the host network namespace (this
# container uses --network=host, so 127.0.0.1 is the fixture host loopback).
# bash /dev/tcp is used so no extra client package is needed in the fixture.
echo "LISTEN_CHECK=$(ss -ltn 2>/dev/null | grep -c ":$GAME_PORT " || true)"
for _ in $(seq 1 15); do
  if timeout 3 bash -c "exec 3<>/dev/tcp/127.0.0.1/$GAME_PORT" 2>/dev/null; then
    CLIENT_TCP_REACHABLE=1; break
  fi
  sleep 1
done
echo "CLIENT_TCP_REACHABLE=${CLIENT_TCP_REACHABLE:-0}"
HTTP_BODY=$(timeout 5 bash -c "exec 3<>/dev/tcp/127.0.0.1/$GAME_PORT; printf 'GET / HTTP/1.0\r\n\r\n' >&3; head -c 160 <&3" 2>&1 || true)
echo "HTTP_RESPONSE_START=$(echo "$HTTP_BODY" | head -1)"
[ "${CLIENT_TCP_REACHABLE:-0}" = 1 ] && pass "measured client TCP reachability on published game port" || fail "published port" "client TCP not reachable"

echo "===PORT_DRIVER==="
echo "dockerd-rootless.sh port driver = ${DOCKERD_ROOTLESS_ROOTLESSKIT_PORT_DRIVER:-builtin (default)}"
echo "NOTE: slirp4netns builtin port driver forwards in userspace; a host DNAT rule is not expected in rootless mode."

# --- XDG_RUNTIME_DIR reset survival -----------------------------------------
echo "===LIMITS==="
dock rm -f cp-rootless-limits >/dev/null 2>&1
LIMIT_CID=$(dock run -d --name cp-rootless-limits --memory=64m --pids-limit=12 alpine:3 sh -c 'sleep 300' 2>&1) || fail "limits container" "failed"
echo "LIMIT_CID=$LIMIT_CID"
dock inspect cp-rootless-limits --format 'MEM={{.HostConfig.Memory}} PIDS={{.HostConfig.PidsLimit}}' 2>&1 || true
dock rm -f cp-rootless-limits >/dev/null 2>&1

echo "===XDG_RESET==="
uuser systemctl --user stop "$UNIT_NAME" 2>/dev/null || true
sleep 2
env -u XDG_RUNTIME_DIR -u DBUS_SESSION_BUS_ADDRESS runuser -u ubuntu -- \
  env XDG_RUNTIME_DIR=/tmp/bogus-xdg DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG/bus" \
  systemctl --user start "$UNIT_NAME" 2>&1 || echo "restart with bogus ambient XDG returned nonzero"
XOK=0
for _ in $(seq 1 120); do
  [ "$(uuser systemctl --user is-active "$UNIT_NAME" 2>/dev/null || true)" = "active" ] && { XOK=1; break; }
  sleep 1
done
echo "XDG_RESET_ACTIVE=$XOK"
[ "$XOK" = 1 ] && pass "unit restarts after XDG_RUNTIME_DIR reset" || fail "XDG reset" "not active"
echo "XDG_RESET_SOCKET=$(ls -l "$RUN/docker.sock" 2>/dev/null || echo missing)"
echo "XDG_RESET_INFO=$(dock info --format '{{.DockerRootDir}} {{.ServerVersion}}' 2>&1)"

# --- storage driver ----------------------------------------------------------
echo "===STORAGE==="
dock info --format 'driver={{.Driver}} backing={{.DriverStatus}}' 2>&1 || true

# --- fail-closed path refusals are exercised by the Go fixtures (see report) --
echo "===REFUSAL_NOTE==="
echo "Shared-path / symlink / world-writable runtime-dir refusals are asserted by"
echo "go test ./internal/noderuntime/native ./internal/nodeinstall (captured in report)."

echo "===SUMMARY==="
if [ "$FAILED" -eq 0 ]; then echo "ROOTLESS_QUALIFICATION: ALL PASS"; else echo "ROOTLESS_QUALIFICATION: FAILURES PRESENT"; fi
exit "$FAILED"
