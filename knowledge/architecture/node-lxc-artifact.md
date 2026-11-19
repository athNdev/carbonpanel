---
type: Architecture
title: Unprivileged LXC node artifact lane
description: Separate rootfs artifact lane for unprivileged LXC managed nodes; shares the ADR0019 contract and gates on nested-runtime capabilities.
status: draft
owners: [oss-runtime]
code_refs: [packaging/node-lxc, packaging/node-lxc/node_lxc_artifact.py, packaging/node-lxc/config/provenance.json, packaging/node-lxc/config/provenance.fixture.json, packaging/node-lxc/qualification/QUALIFICATION.md, packaging/node-lxc/qualification/SIGNED-ARCHIVE-EVIDENCE.md, packaging/node-lxc/tests/test_node_lxc.py, docs/node-lxc-mode.md]
sources:
  - title: ADR 0036 unprivileged LXC node template
    resource: ../../docs/adr/0036-node-lxc-template.md
  - title: Node LXC mode operations
    resource: ../../docs/node-lxc-mode.md
---

# Parts

- [Unprivileged LXC node artifact lane — Rule](node-lxc-artifact-rule.md) — The unprivileged LXC / Proxmox rootfs template is a separate artifact lane at
