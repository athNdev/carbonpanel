---
type: Workflow
title: Existing Linux guest installer qualification
description: Exercise the packaged controller install and update on disposable hosted Linux with a guest-local Docker daemon.
status: stable
owners: [oss-runtime]
code_refs: [.github/workflows/compose-release-qualification.yml, scripts/proxmox/qualify-existing-guest.py, scripts/proxmox/install-carbon-panel.sh]
sources:
  - title: Proxmox existing-guest installer and update instructions
    resource: ../../docs/carbon-panel/src/content/docs/getting-started/proxmox.mdx
  - title: Controller database snapshot and rollback runbook
    resource: ../../docs/runbook-panel-db-restore.md
---

Run the installer from the exact candidate checkout with immutable image
references. The host-network job in Compose release qualification reuses its
verified staged image and runner-local registry for this probe. The installer
needs the guest-local default Docker context and uses a fixed container name
and port. Use a disposable hosted runner without an existing controller.

The probe checks a clean install, repeat install, an online snapshot, a
refused image pull, and a successful update. Compare authenticated identity,
bind mounts, SQLite integrity, persistent fixture hashes and the unrelated
service identity across the update. This exercises the existing-guest
installer. Native Proxmox template import and boot require their own host.
