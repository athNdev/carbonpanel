---
type: Architecture
title: "Unprivileged LXC node artifact lane — Qualification and boot evidence"
description: "Fail-closed boot qualification and runtime evidence boundaries for the LXC artifact lane."
status: draft
owners: [oss-runtime]
code_refs: [packaging/node-lxc, packaging/node-lxc/node_lxc_artifact.py, packaging/node-lxc/qualification/QUALIFICATION.md, packaging/node-lxc/qualification/SIGNED-ARCHIVE-EVIDENCE.md, packaging/node-lxc/tests/test_node_lxc.py, docs/node-lxc-mode.md, .github/workflows/lxc-boot-qualification.yml]
sources:
  - title: Parent concept
    resource: node-lxc-artifact.md
---
# Qualification and boot evidence

- `boot-qualify` verifies signed upstream archive bytes in an owned disposable
  path and records read-only Proxmox prerequisite checks. Finding guest
  management tools never authorizes provisioning. The command retains
  `BOOT_QUALIFICATION_BLOCKED` and `attempted: false` after archive verification.
  The manual `.github/workflows/lxc-boot-qualification.yml` workflow uploads
  the report.
- Live qualification requires a separately authorized disposable Proxmox
  fixture and observed unprivileged import, boot and reboot, effective
  confinement, shared enrollment and nested runtime evidence. See
  [Disposable Proxmox qualification](../../docs/node-lxc-mode.md#disposable-proxmox-qualification).
