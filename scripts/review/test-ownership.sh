#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
test_root="$(mktemp -d)"
name="carbon-review-fixture-$(basename "$test_root")"
other="${name}-other"
root="$(realpath -m "$HOME/.cache/carbon-panel-review/sessions")"
target="$root/$name"
neighbor="$root/$other"
auth_session="$root/${name}-auth"
node_session="$root/${name}-node"
empty_session="$root/${name}-empty"
legacy_sha="$(printf '%s' "$name" | sha256sum | cut -c1-40)"
legacy="$HOME/.cache/carbon-review/${legacy_sha:0:12}"
mkdir -p "$root" "$test_root/bin"
[ ! -e "$target" ] && [ ! -e "$neighbor" ] && [ ! -e "$auth_session" ] && [ ! -e "$node_session" ] && [ ! -e "$empty_session" ] && [ ! -e "$legacy" ] || { echo "fixture name collision" >&2; exit 1; }
cleanup() {
  # These exact names were derived from a fresh mktemp token and checked above.
  local path resolved
  for path in "$target" "$neighbor" "$auth_session" "$node_session" "$empty_session"; do
    resolved="$(realpath -m "$path")"
    case "$resolved" in "$root"/*) ;; *) echo "refusing fixture cleanup outside session root" >&2; return 1 ;; esac
  done
  case "$(realpath -m "$legacy")" in "$(realpath -m "$HOME/.cache/carbon-review")"/*) ;; *) return 1 ;; esac
  case "$(realpath -m "$test_root")" in "$(realpath -m "${TMPDIR:-/tmp}")"/*) ;; *) return 1 ;; esac
  rm -rf -- "$target" "$neighbor" "$auth_session" "$node_session" "$empty_session" "$legacy" "$test_root"
}
trap cleanup EXIT

cat > "$test_root/bin/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  info) exit 0 ;;
  ps) [ "${FAKE_DOCKER_MODE:-absent}" = absent ] || printf '%s\n' "${FAKE_DOCKER_NAMES:-$REVIEW_STACK_NAME}" ;;
  inspect)
    [ "${FAKE_DOCKER_MODE:-absent}" != absent ] || exit 1
    if [ "${2:-}" = --format ]; then
      owner="${FAKE_OWNER:-}"
      case "${4:-}" in *-b) owner="${FAKE_OWNER_B:-$owner}" ;; esac
      printf '%s %s\n' 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' "$owner"
    fi ;;
  rm) printf '%s\n' "$*" >> "$FAKE_DOCKER_LOG" ;;
  build|run) exit 0 ;;
  *) echo "unexpected Docker call" >&2; exit 1 ;;
esac
EOF
chmod +x "$test_root/bin/docker"
cat > "$test_root/bin/curl" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  *AuthService/Login*)
    if [ "${FAKE_CURL_MODE:-}" = auth-failure ]; then printf '{}'; else printf '{"token":"fixture"}'; fi ;;
  *NodeService/ListNodes*) printf '{"nodes":[]}' ;;
  *NodeService/CreateNode*) exit 1 ;;
  *) printf '{}' ;;
esac
EOF
chmod +x "$test_root/bin/curl"
export PATH="$test_root/bin:$PATH" FAKE_DOCKER_LOG="$test_root/docker.log"
export REVIEW_STACK_NAME="$name"

# A configurable path outside the owned session must be rejected before Docker.
if REVIEW_DATA_DIR=/ bash "$script_dir/devstack.sh" clean >/dev/null 2>&1; then
  echo "root directory accepted" >&2; exit 1
fi
mkdir -p "$target" "$neighbor"
printf 'preserve\n' > "$target/world.dat"
printf 'neighbor\n' > "$neighbor/world.dat"
if bash "$script_dir/devstack.sh" clean >/dev/null 2>&1; then
  echo "unmarked data accepted" >&2; exit 1
fi
if bash "$script_dir/devstack.sh" up >/dev/null 2>&1; then
  echo "unmarked data adopted by up" >&2; exit 1
fi
[ -f "$target/world.dat" ] && [ -f "$neighbor/world.dat" ]
mkdir "$empty_session"
if REVIEW_STACK_NAME="${name}-empty" bash "$script_dir/devstack.sh" up >/dev/null 2>&1; then
  echo "empty unmarked directory adopted" >&2; exit 1
fi
[ -d "$empty_session" ] && [ ! -e "$empty_session/.carbon-review-owner" ]

printf 'carbon-review-v1\n%s\n%s\n' "$name" "$target" > "$target/.carbon-review-owner"
export FAKE_DOCKER_MODE=present FAKE_OWNER="$neighbor"
if bash "$script_dir/devstack.sh" clean >/dev/null 2>&1; then
  echo "unowned container accepted" >&2; exit 1
fi
[ -f "$target/world.dat" ] && [ ! -e "$FAKE_DOCKER_LOG" ]

export FAKE_OWNER="$target"
bash "$script_dir/devstack.sh" down >/dev/null
[ -f "$target/world.dat" ] && [ -f "$neighbor/world.dat" ]
bash "$script_dir/devstack.sh" clean >/dev/null
[ ! -e "$target" ] && [ -f "$neighbor/world.dat" ]

ln -s "$neighbor" "$target"
if bash "$script_dir/devstack.sh" clean >/dev/null 2>&1; then
  echo "symlinked session accepted" >&2; exit 1
fi
[ -f "$neighbor/world.dat" ]

# Failed login and failed node creation must not print a ready result.
export FAKE_DOCKER_MODE=absent REVIEW_PORT=65000
for failure in auth node; do
  export REVIEW_STACK_NAME="${name}-${failure}" FAKE_CURL_MODE="${failure}-failure"
  if output="$(bash "$script_dir/devstack.sh" up 2>&1)"; then
    echo "failed $failure fixture reported success" >&2; exit 1
  fi
  case "$output" in *"review panel:"*) echo "failed $failure fixture reported ready" >&2; exit 1 ;; esac
done
export REVIEW_STACK_NAME="$name" FAKE_DOCKER_MODE=present
if bash "$script_dir/../manual-review/review.sh" reset >/dev/null 2>&1; then
  echo "legacy reset unexpectedly enabled" >&2; exit 1
fi

# The dual profile must validate both containers before removing either one.
mkdir -p "$legacy"
repo_root="$(cd "$script_dir/../.." && pwd)"
printf 'carbon-dual-review-v1\n%s\n%s\n%s' "$legacy_sha" "$legacy" "$repo_root" > "$legacy/.carbon-review-owner"
printf 'preserve\n' > "$legacy/world.dat"
export REVIEW_SESSION_SHA="$legacy_sha"
export FAKE_DOCKER_NAMES="$(printf 'carbon-review-%s-a\ncarbon-review-%s-b' "${legacy_sha:0:12}" "${legacy_sha:0:12}")"
export FAKE_OWNER="$legacy" FAKE_OWNER_B="$neighbor"
: > "$FAKE_DOCKER_LOG"
if bash "$script_dir/../manual-review/review.sh" down >/dev/null 2>&1; then
  echo "unowned dual containers accepted" >&2; exit 1
fi
[ ! -s "$FAKE_DOCKER_LOG" ] && [ -f "$legacy/world.dat" ]
export FAKE_OWNER_B="$legacy"
bash "$script_dir/../manual-review/review.sh" down >/dev/null
[ "$(wc -l < "$FAKE_DOCKER_LOG")" -eq 2 ] && [ -f "$legacy/world.dat" ]
echo "PASS: review teardown preserves unowned and neighboring data"
