---
type: Architecture
title: "Node VM artifact — Rule (part 2)"
description: "or published; no live VM, enrollment, daemon or deployment success is claimed."
status: draft
owners: [oss-runtime]
code_refs: [packaging/node-vm, scripts/node_vm_build.py, scripts/node_vm_integrity.py, scripts/node_vm_upgrade.py, scripts/node_vm_verify_image.py, scripts/test_node_vm_build.py, scripts/test_node_vm_integrity.py, scripts/test_node_vm_upgrade.py, scripts/test_node_vm_enroll.py, scripts/test_node_vm_verify_image.py, packaging/node-vm/qualification/VM-BOOT-QUALIFICATION.md]
sources:
  - title: Parent concept
    resource: node-vm-artifact.md
---
  or published; no live VM, enrollment, daemon or deployment success is claimed.
  A real clean boot is blocked where no `qemu-system` binary or `/dev/kvm`
  exists; record that exact blocker rather than claiming boot evidence.
  Disposable owned-VM acceptance is designed in
  `packaging/node-vm/qualification/QUALIFICATION.md`.
