---
type: Architecture
title: Node VM artifact
description: Reproducible, secret-free QCOW2/cloud-init node VM builds with pinned provenance, scoped first-boot enrollment and staged rollback; fixture-only, not published.
status: draft
owners: [oss-runtime]
code_refs: [packaging/node-vm, scripts/node_vm_build.py, scripts/node_vm_integrity.py, scripts/node_vm_upgrade.py, scripts/node_vm_verify_image.py, scripts/node_vm_boot_qual.py, scripts/test_node_vm_build.py, scripts/test_node_vm_integrity.py, scripts/test_node_vm_upgrade.py, scripts/test_node_vm_enroll.py, scripts/test_node_vm_verify_image.py, scripts/test_node_vm_boot_qual.py, internal/rpc/oss377_managed_fixture_test.go, packaging/node-vm/qualification/VM-BOOT-QUALIFICATION.md]
sources:
  - title: ADR 0035 node VM artifact
    resource: ../../docs/adr/0035-node-vm-artifact.md
  - title: OSS-377 VM boot qualification evidence
    resource: ../../packaging/node-vm/qualification/VM-BOOT-QUALIFICATION.md
  - title: ADR 0019 node control contract (shared, not forked)
    resource: ../../docs/adr/0019-node-control-contract.md
---

# Parts

- [Node VM artifact — Rule](node-vm-artifact-rule.md) — The node VM artifact (packaging/node-vm/, drivers in scripts/node_vm_*.py)
- [Node VM artifact — Rule (part 2)](node-vm-artifact-rule-2.md) — or published; no live VM, enrollment, daemon or deployment success is claimed.
- [Node VM artifact — Why + How to check + Read next](node-vm-artifact-why.md) — A shared image with baked credentials or an unpinned base would defeat the
