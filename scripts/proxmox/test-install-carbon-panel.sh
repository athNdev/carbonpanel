#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALLER="$SCRIPT_DIR/install-carbon-panel.sh"
TMPROOT="$(mktemp -d)"
REAL_MKTEMP="$(command -v mktemp)"
cleanup() { rm -rf "$TMPROOT"; }
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }

[ -f "$INSTALLER" ] || fail "installer not found: $INSTALLER"

run_expect_fail() {
  local name=$1; shift
  if "$@" >"$TMPROOT/last.out" 2>&1; then
    fail "$name should fail"
  fi
  pass "$name"
}

# Help/argument behavior (no docker required)
bash "$INSTALLER" --help >/dev/null
pass "help exits successfully"

run_expect_fail "unknown argument" bash "$INSTALLER" --bogus
run_expect_fail "--dir without value" bash "$INSTALLER" install --dir
run_expect_fail "--image without value" bash "$INSTALLER" install --image
run_expect_fail "image with whitespace rejected" bash "$INSTALLER" install --image "bad image"
run_expect_fail "image with quote rejected" bash "$INSTALLER" install --image 'bad"image'

# Stub docker/docker compose for inert behavior
STUBBIN="$TMPROOT/bin"
mkdir -p "$STUBBIN"
cat >"$STUBBIN/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
log="${DOCKER_STUB_LOG:?}"
ctx="${DOCKER_STUB_CONTEXT:-default}"
endpoint="${DOCKER_STUB_ENDPOINT:-unix:///var/run/docker.sock}"
fail_pull="${DOCKER_STUB_FAIL_PULL:-0}"
fail_up="${DOCKER_STUB_FAIL_UP:-0}"
if [ "${1:-}" = "context" ]; then
  case "${2:-}" in
    show) printf '%s\n' "$ctx"; exit 0 ;;
    inspect) printf '%s\n' "$endpoint"; exit 0 ;;
  esac
fi
if [ "${1:-}" = "info" ]; then exit 0; fi
if [ "${1:-}" = "compose" ]; then
  shift
  if [ "${1:-}" = "version" ]; then exit 0; fi
  echo "compose $*" >>"$log"
  # Skip global options (for example -f FILE) to find the real subcommand.
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -f|--file)
        shift
        [ "$#" -gt 0 ] && shift
        ;;
      -*) shift ;;
      *) break ;;
    esac
  done
  sub="${1:-}"
  if [ "$sub" = "config" ]; then
    if [ "${DOCKER_STUB_FAIL_CONFIG:-0}" = "1" ]; then exit 1; fi
    printf '%s\n' "${DOCKER_STUB_COMPOSE_SERVICES:-carbon-panel}"
    exit 0
  fi
  if [ "$sub" = "ps" ]; then
    # -aq service: print id only when DOCKER_STUB_SERVICE_EXISTS=1
    if [ "${DOCKER_STUB_SERVICE_EXISTS:-0}" = "1" ]; then
      printf 'cid123\n'
    fi
    exit 0
  fi
  if [ "$sub" = "pull" ] && [ "$fail_pull" = "1" ]; then exit 1; fi
  if [ "$sub" = "up" ] && [ "$fail_up" = "1" ]; then exit 1; fi
  exit 0
fi
echo "docker $*" >>"$log"
exit 0
EOF
chmod +x "$STUBBIN/docker"

# Stub systemctl: native evidence is opt-in via DOCKER_STUB_SYSTEMD_UNIT=1.
cat >"$STUBBIN/systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${DOCKER_STUB_SYSTEMD_UNIT:-0}" = "1" ]; then
  printf 'carbon-panel.service enabled\n'
fi
exit 0
EOF
chmod +x "$STUBBIN/systemctl"

# mktemp wrapper: when COMPOSE_RACE_TARGET is set, a foreign compose file is
# created in the window between the installer's existence check and publish.
cat >"$STUBBIN/mktemp" <<EOF
#!/usr/bin/env bash
set -euo pipefail
out="\$("$REAL_MKTEMP" "\$@")"
if [ -n "\${COMPOSE_RACE_TARGET:-}" ] && [ ! -e "\$COMPOSE_RACE_TARGET" ]; then
  printf 'raced-in-compose: keep-me\n' >"\$COMPOSE_RACE_TARGET"
fi
printf '%s\n' "\$out"
EOF
chmod +x "$STUBBIN/mktemp"

export DOCKER_STUB_LOG="$TMPROOT/docker.log"
export DOCKER_STUB_SERVICE_EXISTS=0
export PATH="$STUBBIN:$PATH"

# Refuse non-default/remote Docker context or incompatible endpoint/env overrides
DOCKER_STUB_CONTEXT=remote-ctx run_expect_fail "non-default docker context refused" bash "$INSTALLER" install --dir "$TMPROOT/ctx1"
DOCKER_STUB_CONTEXT=default DOCKER_STUB_ENDPOINT=tcp://10.0.0.2:2375 run_expect_fail "remote docker endpoint refused" bash "$INSTALLER" install --dir "$TMPROOT/ctx2"
DOCKER_CONTEXT=remote-ctx run_expect_fail "DOCKER_CONTEXT override refused" bash "$INSTALLER" install --dir "$TMPROOT/ctx3"
DOCKER_HOST=tcp://10.0.0.2:2375 run_expect_fail "DOCKER_HOST override refused" bash "$INSTALLER" install --dir "$TMPROOT/ctx4"
unset DOCKER_STUB_CONTEXT DOCKER_STUB_ENDPOINT DOCKER_CONTEXT DOCKER_HOST

# Fresh install (absolute path with spaces): creates dirs/compose with fixed relative mounts, pulls and starts
INSTDIR="$TMPROOT/fresh install"
: >"$DOCKER_STUB_LOG"
bash "$INSTALLER" install --dir "$INSTDIR" --image example.invalid/carbonpanel:test >/dev/null
[ -f "$INSTDIR/compose.yml" ] || fail "compose.yml should be created"
[ -d "$INSTDIR/data" ] && [ -d "$INSTDIR/backups" ] && [ -d "$INSTDIR/tmp" ] || fail "data dirs should be created"
grep -Fq 'image: "example.invalid/carbonpanel:test"' "$INSTDIR/compose.yml" || fail "compose should contain selected image"
grep -Fq './data:/app/data' "$INSTDIR/compose.yml" || fail "compose should keep fixed data bind mount"
grep -Fq './backups:/app/backups' "$INSTDIR/compose.yml" || fail "compose should keep fixed backups bind mount"
grep -Fq './tmp:/app/tmp' "$INSTDIR/compose.yml" || fail "compose should keep fixed tmp bind mount"
grep -Fq './config.yaml:/app/config.yaml:ro' "$INSTDIR/compose.yml" || fail "compose should include config example mount"
grep -q 'pull carbon-panel' "$DOCKER_STUB_LOG" || fail "fresh install should pull carbon-panel"
grep -q 'up -d carbon-panel' "$DOCKER_STUB_LOG" || fail "fresh install should start carbon-panel"
[ ! -f "$INSTDIR/.carbon-panel-install-owned" ] || fail "marker should be removed on successful install"
[ ! -d "$INSTDIR/.carbon-panel-install.lock" ] || fail "install lock should be released"
if find "$INSTDIR" -maxdepth 1 -name 'compose.yml.tmp.*' | grep -q .; then
  fail "temporary compose file should not be left behind"
fi
pass "fresh install creates dirs/compose with relative mounts and starts service"

# Existing deployment with running service: install must not overwrite or pull/recreate
printf 'custom-compose: keep-me\n' >"$INSTDIR/compose.yml"
printf 'keep-data\n' >"$INSTDIR/data/persist.txt"
printf 'keep-backup\n' >"$INSTDIR/backups/persist.txt"
: >"$DOCKER_STUB_LOG"
DOCKER_STUB_SERVICE_EXISTS=1 bash "$INSTALLER" install --dir "$INSTDIR" >/dev/null
grep -Fq 'custom-compose: keep-me' "$INSTDIR/compose.yml" || fail "existing compose must not be overwritten"
grep -Fq 'keep-data' "$INSTDIR/data/persist.txt" || fail "existing data must be preserved"
grep -Fq 'keep-backup' "$INSTDIR/backups/persist.txt" || fail "existing backups must be preserved"
grep -q 'ps -aq carbon-panel' "$DOCKER_STUB_LOG" || fail "existing deployment check should use read-only compose ps"
if grep -Eq 'compose .*( pull | up )' "$DOCKER_STUB_LOG"; then
  fail "install over running existing deployment must not pull or recreate"
fi
DOCKER_STUB_SERVICE_EXISTS=0
pass "install preserves running existing deployment"

# Existing compose without service and without marker: refuse to guess
NOMARK="$TMPROOT/nomark"
mkdir -p "$NOMARK/data" "$NOMARK/backups" "$NOMARK/tmp"
printf 'custom-compose\n' >"$NOMARK/compose.yml"
run_expect_fail "existing compose without service/marker refused" bash "$INSTALLER" install --dir "$NOMARK"
grep -Fq 'custom-compose' "$NOMARK/compose.yml" || fail "refusal must preserve compose"
pass "refuses ambiguous existing compose without marker"

# Preserve contract: a compose that appears between the existence check and
# publish must never be overwritten or adopted as this installer's partial.
RACEDIR="$TMPROOT/raced"
: >"$DOCKER_STUB_LOG"
export COMPOSE_RACE_TARGET="$RACEDIR/compose.yml"
run_expect_fail "raced-in compose refused" bash "$INSTALLER" install --dir "$RACEDIR"
unset COMPOSE_RACE_TARGET
grep -Fq 'raced-in-compose: keep-me' "$RACEDIR/compose.yml" || fail "raced-in compose must not be overwritten"
grep -Fq 'refusing to overwrite' "$TMPROOT/last.out" || fail "raced-in compose refusal should be explicit"
[ ! -f "$RACEDIR/.carbon-panel-install-owned" ] || fail "raced-in compose must not be adopted via the ownership marker"
if find "$RACEDIR" -maxdepth 1 -name 'compose.yml.tmp.*' | grep -q .; then
  fail "no temporary compose file after raced-in refusal"
fi
if grep -q 'pull carbon-panel' "$DOCKER_STUB_LOG"; then
  fail "raced-in compose must not be pulled or started"
fi
pass "raced-in compose is preserved, not adopted"

# Lock: an existing lock is fail-closed (no stale takeover, no deletion).
LOCKDIR="$TMPROOT/lockheld"
mkdir -p "$LOCKDIR/.carbon-panel-install.lock"
printf '424242\n' >"$LOCKDIR/.carbon-panel-install.lock/pid"
: >"$DOCKER_STUB_LOG"
run_expect_fail "held install lock refused" bash "$INSTALLER" install --dir "$LOCKDIR"
grep -Fq 'already running' "$TMPROOT/last.out" || fail "held lock refusal should be explicit"
[ -d "$LOCKDIR/.carbon-panel-install.lock" ] || fail "fail-closed lock must not be removed"
[ -f "$LOCKDIR/.carbon-panel-install.lock/pid" ] || fail "lock pid must not be removed"
[ ! -f "$LOCKDIR/compose.yml" ] || fail "held lock must not publish compose"
if grep -q 'pull carbon-panel' "$DOCKER_STUB_LOG"; then
  fail "held lock must not run docker compose"
fi
rm -f "$LOCKDIR/.carbon-panel-install.lock/pid"
rmdir "$LOCKDIR/.carbon-panel-install.lock"
pass "held install lock is refused without takeover"

# Failure/retry: pull failure leaves compose and marker; rerun resumes with pull+up
FAILDIR="$TMPROOT/failretry"
: >"$DOCKER_STUB_LOG"
DOCKER_STUB_FAIL_PULL=1 run_expect_fail "initial install pull failure" bash "$INSTALLER" install --dir "$FAILDIR"
grep -Fq 'pull failed; compose/config preserved' "$TMPROOT/last.out" || fail "pull failure should report preserved config and resume guidance"
grep -q 'pull carbon-panel' "$DOCKER_STUB_LOG" || fail "failed install should have attempted the pull"
if grep -q 'up -d carbon-panel' "$DOCKER_STUB_LOG"; then
  fail "failed pull must not start the service"
fi
[ -f "$FAILDIR/compose.yml" ] || fail "compose should remain after pull failure"
[ -f "$FAILDIR/.carbon-panel-install-owned" ] || fail "marker should remain after pull failure"
DOCKER_STUB_FAIL_PULL=0
: >"$DOCKER_STUB_LOG"
bash "$INSTALLER" install --dir "$FAILDIR" >/dev/null
grep -q 'pull carbon-panel' "$DOCKER_STUB_LOG" || fail "rerun should pull on resume"
grep -q 'up -d carbon-panel' "$DOCKER_STUB_LOG" || fail "rerun should start on resume"
[ ! -f "$FAILDIR/.carbon-panel-install-owned" ] || fail "marker removed after successful resume"

# Failed up may leave stopped container with marker; rerun must still resume pull+up
FAILUP="$TMPROOT/failup"
: >"$DOCKER_STUB_LOG"
DOCKER_STUB_FAIL_UP=1 run_expect_fail "initial install up failure" bash "$INSTALLER" install --dir "$FAILUP"
grep -Fq 'container start failed; compose/config preserved' "$TMPROOT/last.out" || fail "up failure should report preserved config and resume guidance"
[ -f "$FAILUP/.carbon-panel-install-owned" ] || fail "marker should remain after up failure"
DOCKER_STUB_FAIL_UP=0
DOCKER_STUB_SERVICE_EXISTS=1
: >"$DOCKER_STUB_LOG"
bash "$INSTALLER" install --dir "$FAILUP" >/dev/null
grep -q 'pull carbon-panel' "$DOCKER_STUB_LOG" || fail "resume after failed up should pull"
grep -q 'up -d carbon-panel' "$DOCKER_STUB_LOG" || fail "resume after failed up should start"
[ ! -f "$FAILUP/.carbon-panel-install-owned" ] || fail "marker removed after resume from failed up"
DOCKER_STUB_SERVICE_EXISTS=0
pass "failed fresh install can be resumed"

# Marker without compose (stale ownership marker): install must regenerate
# compose atomically and finish, not refuse.
INTERRUPTED="$TMPROOT/interrupted"
mkdir -p "$INTERRUPTED/data"
: >"$INTERRUPTED/.carbon-panel-install-owned"
: >"$DOCKER_STUB_LOG"
bash "$INSTALLER" install --dir "$INTERRUPTED" >/dev/null
[ -f "$INTERRUPTED/compose.yml" ] || fail "interrupted write should regenerate compose"
grep -q 'pull carbon-panel' "$DOCKER_STUB_LOG" || fail "interrupted write resume should pull"
grep -q 'up -d carbon-panel' "$DOCKER_STUB_LOG" || fail "interrupted write resume should start"
[ ! -f "$INTERRUPTED/.carbon-panel-install-owned" ] || fail "marker removed after interrupted-write resume"
if find "$INTERRUPTED" -maxdepth 1 -name 'compose.yml.tmp.*' | grep -q .; then
  fail "no temporary compose file after interrupted-write resume"
fi
pass "stale ownership marker without compose regenerates compose"

# Relative dir input is canonicalized (compose uses fixed relative mounts)
RELDIR_PARENT="$TMPROOT/reldir"
mkdir -p "$RELDIR_PARENT"
: >"$DOCKER_STUB_LOG"
(cd "$RELDIR_PARENT" && bash "$INSTALLER" install --dir relinst >/dev/null)
[ -f "$RELDIR_PARENT/relinst/compose.yml" ] || fail "relative dir install should create compose"
grep -Fq './data:/app/data' "$RELDIR_PARENT/relinst/compose.yml" || fail "compose should use fixed relative mounts"
pass "relative install dir supported with fixed relative mounts"

# Default install dir via env (without writing to /opt/carbon-panel)
DEF="$TMPROOT/defaultdir"
: >"$DOCKER_STUB_LOG"
CARBON_PANEL_DIR="$DEF" bash "$INSTALLER" install >/dev/null
[ -f "$DEF/compose.yml" ] || fail "default dir via CARBON_PANEL_DIR should work"
pass "default install directory honored via env"

# Update: requires existing dir/compose and rejects --image
run_expect_fail "update fails for missing install dir" bash "$INSTALLER" update --dir "$TMPROOT/missing"
mkdir -p "$TMPROOT/nocompose"
run_expect_fail "update fails without compose file" bash "$INSTALLER" update --dir "$TMPROOT/nocompose"
run_expect_fail "update rejects --image" bash "$INSTALLER" update --dir "$INSTDIR" --image example.invalid/other:1

# Update: pull + up service only, preserves compose and data
: >"$DOCKER_STUB_LOG"
bash "$INSTALLER" update --dir "$INSTDIR" >/dev/null
grep -q 'pull carbon-panel' "$DOCKER_STUB_LOG" || fail "update should pull carbon-panel"
grep -q 'up -d carbon-panel' "$DOCKER_STUB_LOG" || fail "update should recreate carbon-panel"
grep -Fq 'custom-compose: keep-me' "$INSTDIR/compose.yml" || fail "update must not replace compose"
grep -Fq 'keep-data' "$INSTDIR/data/persist.txt" || fail "update must preserve data"
grep -Fq 'keep-backup' "$INSTDIR/backups/persist.txt" || fail "update must preserve backups"
pass "update pulls/recreates service while preserving compose and data"

# Update failure: pull error must preserve compose/data and report recovery guidance
: >"$DOCKER_STUB_LOG"
DOCKER_STUB_FAIL_PULL=1 run_expect_fail "update pull failure" bash "$INSTALLER" update --dir "$INSTDIR"
grep -Fq 'update pull failed; existing container, compose/config and data preserved' "$TMPROOT/last.out" || fail "update pull failure should report preservation"
grep -Fq 'custom-compose: keep-me' "$INSTDIR/compose.yml" || fail "failed update must preserve compose"
grep -Fq 'keep-data' "$INSTDIR/data/persist.txt" || fail "failed update must preserve data"
grep -Fq 'keep-backup' "$INSTDIR/backups/persist.txt" || fail "failed update must preserve backups"
if grep -q 'up -d carbon-panel' "$DOCKER_STUB_LOG"; then
  fail "failed update pull must not recreate the service"
fi
DOCKER_STUB_FAIL_PULL=0
pass "failed update preserves compose and data"

# --- Regression fixtures: standard Compose names, explicit config, refusals ---

write_compose_fixture() {
  cat >"$1" <<'YAML'
services:
  carbon-panel:
    image: atharvnaphade/carbonpanel:latest
    volumes:
      - ./data:/app/data
YAML
}

# The original failing invocation (update with no --compose) now resolves every
# standard Compose name shipped by this repository and by Docker Compose.
for name in compose.yaml docker-compose.yml docker-compose.yaml; do
  ALT="$TMPROOT/alt_${name//./_}"
  mkdir -p "$ALT/data"
  printf 'keep-data\n' >"$ALT/data/persist.txt"
  write_compose_fixture "$ALT/$name"
  : >"$DOCKER_STUB_LOG"
  bash "$INSTALLER" update --dir "$ALT" >"$TMPROOT/alt.out" 2>&1
  grep -Fq "Using Compose file: $ALT/$name" "$TMPROOT/alt.out" || fail "update should resolve $name"
  grep -q 'pull carbon-panel' "$DOCKER_STUB_LOG" || fail "update with $name should pull"
  grep -q 'up -d carbon-panel' "$DOCKER_STUB_LOG" || fail "update with $name should recreate"
  grep -Fq 'keep-data' "$ALT/data/persist.txt" || fail "update with $name must preserve data"
  pass "original update invocation resolves $name"
done

# Ambiguous standard names must be refused before any operation.
AMB="$TMPROOT/ambiguous"
mkdir -p "$AMB"
write_compose_fixture "$AMB/compose.yml"
write_compose_fixture "$AMB/docker-compose.yml"
: >"$DOCKER_STUB_LOG"
run_expect_fail "ambiguous compose names refused" bash "$INSTALLER" update --dir "$AMB"
grep -Fq 'ambiguous Compose configuration' "$TMPROOT/last.out" || fail "ambiguity must be explicit"
grep -Fq -- '--compose' "$TMPROOT/last.out" || fail "ambiguity should suggest --compose"
if grep -Eq 'compose .*( pull | up )' "$DOCKER_STUB_LOG"; then fail "ambiguous must not pull or recreate"; fi
[ ! -e "$AMB/.carbon-panel-install.lock" ] || fail "ambiguity must not create a lock"
pass "ambiguous standard Compose names refused without operations"

# Explicit --compose outside the install directory. The lock is keyed to the
# compose project directory, so two --dir values cannot update the same file.
EXT="$TMPROOT/external-config"
mkdir -p "$EXT" "$TMPROOT/expdir/data"
write_compose_fixture "$EXT/panel-compose.yml"
: >"$DOCKER_STUB_LOG"
bash "$INSTALLER" update --dir "$TMPROOT/expdir" --compose "$EXT/panel-compose.yml" >"$TMPROOT/exp.out" 2>&1
grep -Fq "Using Compose file: $EXT/panel-compose.yml" "$TMPROOT/exp.out" || fail "external compose should be used"
grep -q 'pull carbon-panel' "$DOCKER_STUB_LOG" || fail "external compose update should pull"
[ ! -d "$EXT/.carbon-panel-install.lock" ] || fail "external lock must be released"
[ ! -d "$TMPROOT/expdir/.carbon-panel-install.lock" ] || fail "no --dir-keyed lock for external compose"
mkdir -p "$TMPROOT/expdir2"
mkdir -p "$EXT/.carbon-panel-install.lock"
printf '999999\n' >"$EXT/.carbon-panel-install.lock/pid"
: >"$DOCKER_STUB_LOG"
run_expect_fail "external compose lock shared across --dir values" bash "$INSTALLER" update --dir "$TMPROOT/expdir2" --compose "$EXT/panel-compose.yml"
grep -Fq 'already running' "$TMPROOT/last.out" || fail "shared lock refusal should be explicit"
if grep -q 'pull carbon-panel' "$DOCKER_STUB_LOG"; then fail "shared lock must block pull"; fi
rm -f "$EXT/.carbon-panel-install.lock/pid"
rmdir "$EXT/.carbon-panel-install.lock"
# Relative --compose resolves from --dir.
write_compose_fixture "$TMPROOT/expdir/rel-compose.yml"
: >"$DOCKER_STUB_LOG"
bash "$INSTALLER" update --dir "$TMPROOT/expdir" --compose rel-compose.yml >"$TMPROOT/rel.out" 2>&1
grep -Fq "Using Compose file: $TMPROOT/expdir/rel-compose.yml" "$TMPROOT/rel.out" || fail "relative --compose should resolve from --dir"
pass "explicit --compose supports external/relative paths with a shared lock"

# Missing Compose config: refusal must be explicit and leave no mutation.
MISS="$TMPROOT/missing-config"
mkdir -p "$MISS/data"
printf 'keep-data\n' >"$MISS/data/persist.txt"
: >"$DOCKER_STUB_LOG"
run_expect_fail "update refuses missing compose config" bash "$INSTALLER" update --dir "$MISS"
if grep -Fq 'run install first' "$TMPROOT/last.out"; then fail "misleading 'run install first' must be gone"; fi
grep -Fq 'no supported Compose configuration' "$TMPROOT/last.out" || fail "missing config must be explicit"
grep -Fq 'keep-data' "$MISS/data/persist.txt" || fail "data must be preserved on refusal"
if find "$MISS" -maxdepth 1 -type f | grep -q .; then fail "missing refusal must not write files in install dir"; fi
[ ! -e "$MISS/.carbon-panel-install-owned" ] || fail "no marker on refusal"
[ ! -e "$MISS/.carbon-panel-install.lock" ] || fail "no lock on refusal"
if [ -s "$DOCKER_STUB_LOG" ]; then fail "missing refusal must not reach Docker"; fi
pass "missing Compose config refused without writes or operations"

# Native systemd evidence: refusals on update and fresh install, no mutations.
NAT="$TMPROOT/native-unit"
mkdir -p "$NAT/data"
printf 'keep-data\n' >"$NAT/data/persist.txt"
: >"$DOCKER_STUB_LOG"
DOCKER_STUB_SYSTEMD_UNIT=1 run_expect_fail "native install refused on update" bash "$INSTALLER" update --dir "$NAT"
grep -Fq 'Native systemd evidence' "$TMPROOT/last.out" || fail "native evidence must be reported"
grep -Fq 'docs/getting-started/prebuilt-binaries/' "$TMPROOT/last.out" || fail "native controller guide must be referenced"
grep -Fq 'https://github.com/athNdev/carbonpanel/releases' "$TMPROOT/last.out" || fail "application Releases link must be referenced"
grep -Fq 'will not install, migrate' "$TMPROOT/last.out" || fail "no auto-install/migrate"
if grep -Fq 'run install first' "$TMPROOT/last.out"; then fail "misleading message"; fi
if [ -s "$DOCKER_STUB_LOG" ]; then fail "native refusal must not reach Docker"; fi
grep -Fq 'keep-data' "$NAT/data/persist.txt" || fail "data preserved"
if find "$NAT" -maxdepth 1 -type f | grep -q .; then fail "native refusal must not write files"; fi
NATINSTALL="$TMPROOT/native-install-refusal"
: >"$DOCKER_STUB_LOG"
DOCKER_STUB_SYSTEMD_UNIT=1 run_expect_fail "native install refused on fresh install" bash "$INSTALLER" install --dir "$NATINSTALL"
grep -Fq 'Native systemd evidence' "$TMPROOT/last.out" || fail "install native evidence"
grep -Fq 'will not install, migrate' "$TMPROOT/last.out" || fail "install must not migrate"
[ ! -e "$NATINSTALL" ] || fail "native refusal must not create the install directory"
if [ -s "$DOCKER_STUB_LOG" ]; then fail "native refusal must not reach Docker"; fi
pass "native systemd refused on update and fresh install without mutations"

# The carbon-panel service must be verified before an update operates.
BADSVC="$TMPROOT/bad-service"
mkdir -p "$BADSVC"
write_compose_fixture "$BADSVC/compose.yml"
: >"$DOCKER_STUB_LOG"
DOCKER_STUB_COMPOSE_SERVICES=web run_expect_fail "carbon-panel service verified before update" bash "$INSTALLER" update --dir "$BADSVC"
grep -Fq "does not define service 'carbon-panel'" "$TMPROOT/last.out" || fail "missing service must be explicit"
if grep -Eq 'compose .*( pull | up )' "$DOCKER_STUB_LOG"; then fail "missing service must not pull or recreate"; fi
DOCKER_STUB_FAIL_CONFIG=1 run_expect_fail "unreadable compose config refused" bash "$INSTALLER" update --dir "$BADSVC"
grep -Fq 'unable to read services' "$TMPROOT/last.out" || fail "unreadable config must be explicit"
pass "update verifies carbon-panel service before any operation"

# --compose argument validation.
run_expect_fail "--compose without value" bash "$INSTALLER" update --compose
run_expect_fail "--compose with missing file" bash "$INSTALLER" update --dir "$MISS" --compose "$MISS/nope.yml"
mkdir -p "$TMPROOT/compose-as-dir"
run_expect_fail "--compose directory refused" bash "$INSTALLER" update --dir "$MISS" --compose "$TMPROOT/compose-as-dir"
run_expect_fail "--compose rejected for install" bash "$INSTALLER" install --dir "$TMPROOT/install-with-compose" --compose "$EXT/panel-compose.yml"
pass "--compose argument validation"

echo "All proxmox installer tests passed."
