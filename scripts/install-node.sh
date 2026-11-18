#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

# Portable Linux guest installer shared by ALL managed Carbon Panel modes:
# host-native private daemon, QCOW2 VM and unprivileged LXC. There is no
# provider-specific integration here; this is also the supported fallback
# wherever an image format is unqualified.
#
# The installer mutates ONLY directories named by the caller (--state-dir,
# --data-dir, --artifact, --backup-dir). It never edits global host config,
# never starts/stops services, never touches the host Docker socket and never
# writes a secret into provisioning metadata. S stays in a 0600 file inside
# the state directory and is sent only once inside a claim request.
#
# Default is DRY RUN: mutating verbs print the command and exit. Pass --apply
# on an owned guest to execute. Read-only verbs (verify, status) run directly.
#
# Usage:
#   scripts/install-node.sh <verb> [flags] [--apply]
#
# Verbs:
#   init|bundle|claim|verify|import|backup|recover|upgrade|rollback|status|render-daemon
#
# Flags (all paths are caller-owned; none is a global host path):
#   --state-dir DIR      guest state directory
#   --data-dir DIR       persistent world/DB directory
#   --artifact DIR       versioned artifact bundle
#   --backup-dir DIR     new snapshot dir (backup) / existing one (recover)
#   --challenge FILE     BeginEnrollmentChallenge response JSON
#   --public-key FILE    Ed25519 PKIX public key verifying artifact signatures
#   --label NAME         node label for the enrollment bundle
#   --capabilities CSV   scoped capability set (default ROOTLESS,PRIVATE_SOCKET,DEDICATED_ROOTS,CGROUP_V2)
#   --protocol-version N protocol version (default 1)
#   --ttl DURATION       enrollment TTL (default 10m)
#   --target-os OS       artifact OS override (offline fixtures only)
#   --target-arch ARCH   artifact arch override (offline fixtures only)
#   --daemon-spec FILE   disposable daemon spec JSON for render-daemon
#
# The shipped artifact provides a compiled binary at
# $CARBON_NODE_INSTALLER_BIN. Without it this script falls back to
# `go run ./cmd/carbon-node-installer` for source-tree fixtures.
#
# This is an OFFLINE, scoped installer: it does not download artifacts, does
# not provision a live host and is not a Carbon Panel app release.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="${CARBON_NODE_INSTALLER_BIN:-}"
VERB=""
APPLY=false
STATE_DIR=""
DATA_DIR=""
ARTIFACT_DIR=""
BACKUP_DIR=""
CHALLENGE=""
PUBLIC_KEY=""
LABEL=""
CAPABILITIES=""
PROTOCOL_VERSION=""
TTL=""
TARGET_OS=""
TARGET_ARCH=""
DAEMON_SPEC=""

usage() {
	sed -n '2,46p' "$0" | sed 's/^# \?//'
}

need_value() {
	if [[ $# -lt 2 || -z "$2" ]]; then
		echo "missing value for $1" >&2
		exit 2
	fi
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--state-dir)
		need_value "$@"
		STATE_DIR="$2"
		shift 2
		;;
	--data-dir)
		need_value "$@"
		DATA_DIR="$2"
		shift 2
		;;
	--artifact)
		need_value "$@"
		ARTIFACT_DIR="$2"
		shift 2
		;;
	--backup-dir)
		need_value "$@"
		BACKUP_DIR="$2"
		shift 2
		;;
	--challenge)
		need_value "$@"
		CHALLENGE="$2"
		shift 2
		;;
	--public-key)
		need_value "$@"
		PUBLIC_KEY="$2"
		shift 2
		;;
	--label)
		need_value "$@"
		LABEL="$2"
		shift 2
		;;
	--capabilities)
		need_value "$@"
		CAPABILITIES="$2"
		shift 2
		;;
	--protocol-version)
		need_value "$@"
		PROTOCOL_VERSION="$2"
		shift 2
		;;
	--ttl)
		need_value "$@"
		TTL="$2"
		shift 2
		;;
	--target-os)
		need_value "$@"
		TARGET_OS="$2"
		shift 2
		;;
	--target-arch)
		need_value "$@"
		TARGET_ARCH="$2"
		shift 2
		;;
	--daemon-spec)
		need_value "$@"
		DAEMON_SPEC="$2"
		shift 2
		;;
	--apply)
		APPLY=true
		shift
		;;
	--dry-run)
		APPLY=false
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	-*)
		echo "unknown flag: $1" >&2
		usage >&2
		exit 2
		;;
	*)
		if [[ -z "$VERB" ]]; then
			VERB="$1"
			shift
		else
			echo "unexpected argument: $1" >&2
			exit 2
		fi
		;;
	esac
done

if [[ -z "$VERB" ]]; then
	echo "a verb is required" >&2
	usage >&2
	exit 2
fi

CMD=()
if [[ -n "$BIN" ]]; then
	CMD+=("$BIN")
else
	CMD=(go run "$ROOT/cmd/carbon-node-installer")
fi
CMD+=("$VERB")
[[ -n "$STATE_DIR" ]] && CMD+=(-state-dir "$STATE_DIR")
[[ -n "$DATA_DIR" ]] && CMD+=(-data-dir "$DATA_DIR")
[[ -n "$ARTIFACT_DIR" ]] && CMD+=(-artifact "$ARTIFACT_DIR")
[[ -n "$BACKUP_DIR" ]] && CMD+=(-backup-dir "$BACKUP_DIR")
[[ -n "$CHALLENGE" ]] && CMD+=(-challenge "$CHALLENGE")
[[ -n "$PUBLIC_KEY" ]] && CMD+=(-public-key "$PUBLIC_KEY")
[[ -n "$LABEL" ]] && CMD+=(-label "$LABEL")
[[ -n "$CAPABILITIES" ]] && CMD+=(-capabilities "$CAPABILITIES")
[[ -n "$PROTOCOL_VERSION" ]] && CMD+=(-protocol-version "$PROTOCOL_VERSION")
[[ -n "$TTL" ]] && CMD+=(-ttl "$TTL")
[[ -n "$TARGET_OS" ]] && CMD+=(-target-os "$TARGET_OS")
[[ -n "$TARGET_ARCH" ]] && CMD+=(-target-arch "$TARGET_ARCH")
[[ -n "$DAEMON_SPEC" ]] && CMD+=(-daemon-spec "$DAEMON_SPEC")

case "$VERB" in
verify | status | render-daemon)
	# Read-only: run without --apply.
	;;
init | bundle | claim | import | backup | recover | upgrade | rollback)
	if [[ "$APPLY" != true ]]; then
		echo "Portable node installer: DRY RUN. Pass --apply on the owned guest to execute."
		echo "Command: ${CMD[*]}"
		echo "No files were changed."
		exit 0
	fi
	;;
*)
	echo "unknown verb: $VERB" >&2
	usage >&2
	exit 2
	;;
esac

"${CMD[@]}"
