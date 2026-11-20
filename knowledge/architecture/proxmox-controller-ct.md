---
type: Architecture
title: "Proxmox controller CT"
description: "Importable Debian 13 system CT rootfs for the Carbon Panel controller"
status: stable
owners: [oss-runtime]
code_refs: [packaging/proxmox-controller-ct, scripts/proxmox_controller_ct.py, scripts/test_proxmox_controller_ct.py, .github/workflows/proxmox-controller-ct.yml]
sources:
  - title: Artifact runbook
    resource: ../../packaging/proxmox-controller-ct/README.md
---
# Shape

OSS-415 ships one traditional amd64 **unprivileged system CT** rootfs for the
controller. It is distinct from the managed-node LXC lane and from OCI app
CTs. The base is signature-verified Debian 13 `trixie` from `debootstrap`;
Docker/Compose come from the signed Docker apt repo; the controller image is
digest-pinned from the canonical Stable channel contract.

The archive root is a real rootfs (`./etc`, `./usr`, `./sbin`). Build metadata
lives in `/usr/share/carbon-panel`. First boot is
`carbon-panel-ct-firstboot.service`, ordered after guest network and Docker,
retrying on failure, and delegating all existing-state decisions to the
bundled installer. No host socket, privileged CT, baked identity or secret is
shipped. Publication is an artifact-only `controller-ct-v*` draft prerelease.
