#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

# Run the development Docker host in a VirtualBox VM.
#
# WSL2's host networking can wedge: its vEthernet adapter drops to "Media
# disconnected", the guest loses all egress while Windows keeps working, and
# because Docker lives in WSL every container loses the internet with it.
# Minecraft images then cannot download mods or loaders and servers crash on
# install. This VM is an independent Docker host that depends only on the
# Windows network stack.
#
#   scripts/dev/vbox.sh up       # start the VM and wait for the panel
#   scripts/dev/vbox.sh down     # power off
#   scripts/dev/vbox.sh status   # state, panel URL, containers
#   scripts/dev/vbox.sh ssh      # shell inside the VM
#
# Set CARBON_VM_NAME to the registered VirtualBox VM (default carbon-docker).
# Everything else is derived from that VM's registration, so no workstation
# path is baked in.

set -euo pipefail

VM="${CARBON_VM_NAME:-carbon-docker}"
PANEL_HOST_PORT="${CARBON_VM_PANEL_PORT:-8095}"

find_vbox() {
	if [ -n "${VBOX_MANAGE:-}" ]; then
		echo "${VBOX_MANAGE}"
		return
	fi
	if command -v VBoxManage >/dev/null 2>&1; then
		command -v VBoxManage
		return
	fi
	for candidate in "/c/Program Files/Oracle/VirtualBox/VBoxManage.exe" "/mnt/c/Program Files/Oracle/VirtualBox/VBoxManage.exe"; do
		if [ -x "${candidate}" ]; then
			echo "${candidate}"
			return
		fi
	done
	echo "VBoxManage not found; set VBOX_MANAGE" >&2
	exit 2
}

VBOX="$(find_vbox)"

vm_info() {
	"${VBOX}" showvminfo "${VM}" --machinereadable 2>/dev/null || true
}

vm_state() {
	vm_info | sed -n 's/^VMState="\(.*\)"$/\1/p'
}

# The VM's own settings file tells us where its key and seed live.
vm_dir() {
	local cfg
	cfg="$(vm_info | sed -n 's/^CfgFile="\(.*\)"$/\1/p' | tr '\\' '/')"
	dirname "${cfg}"
}

ssh_vm() {
	local key
	key="$(vm_dir)/id_ed25519"
	ssh -i "${key}" -p 2222 \
		-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
		-o ConnectTimeout=10 -o BatchMode=yes carbon@127.0.0.1 "$@"
}

wait_for_panel() {
	local deadline=$((SECONDS + 300))
	while ((SECONDS < deadline)); do
		if curl -fsS -o /dev/null "http://127.0.0.1:${PANEL_HOST_PORT}/healthz" 2>/dev/null; then
			return 0
		fi
		sleep 5
	done
	echo "panel did not answer on http://127.0.0.1:${PANEL_HOST_PORT} within 300s" >&2
	ssh_vm "sudo docker logs --tail 20 carbon-panel" 2>&1 | tail -20 || true
	return 1
}

case "${1:-status}" in
up)
	if [ "$(vm_state)" != "running" ]; then
		"${VBOX}" startvm "${VM}" --type headless
	fi
	# The panel container restarts with the VM; start it only if it is absent
	# or was stopped deliberately.
	ssh_vm "sudo docker start carbon-panel >/dev/null 2>&1 || true"
	wait_for_panel
	echo "panel: http://127.0.0.1:${PANEL_HOST_PORT}"
	;;
down)
	"${VBOX}" controlvm "${VM}" poweroff
	echo "powered off ${VM}"
	;;
status)
	echo "vm:    $(vm_state)"
	if [ "$(vm_state)" = "running" ]; then
		echo "panel: http://127.0.0.1:${PANEL_HOST_PORT}"
		ssh_vm "sudo docker ps --format '{{.Names}} {{.Status}}'" 2>/dev/null || echo "ssh: unreachable"
	fi
	;;
ssh)
	shift
	ssh_vm "${@:-bash}"
	;;
*)
	sed -n '5,22p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac
