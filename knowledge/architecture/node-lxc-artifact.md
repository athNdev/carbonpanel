---
type: Architecture
title: Unprivileged LXC node artifact lane
description: Separate rootfs artifact lane for unprivileged LXC managed nodes; shares the ADR0019 contract and gates on nested-runtime capabilities.
status: draft
owners: [oss-runtime]
code_refs:
  - packaging/node-lxc
  - packaging/node-lxc/node_lxc_artifact.py
  - packaging/node-lxc/config/provenance.json
  - packaging/node-lxc/tests/test_node_lxc.py
sources:
  - title: ADR 0034 unprivileged LXC node artifact
    resource: ../../docs/adr/0034-unprivileged-lxc-node-artifact.md
  - title: Node LXC mode operations
    resource: ../../docs/node-lxc-mode.md
---

# Rule

The unprivileged LXC / Proxmox rootfs template is a separate artifact lane at
`packaging/node-lxc/`. It reuses the shared versioned node-control contract
([node control protocol](node-control-protocol.md), ADR 0019) and adds no RPC,
credential type, TLS stack or publication workflow.

- The isolation boundary is a shared host kernel, not a VM. Isolation and host
  prerequisites are documented separately in `docs/node-lxc-mode.md`; no
  VM-grade isolation claim is made.
- A read-only nested-runtime capability gate refuses unsupported hosts with
  exact reason codes and never changes global host configuration. Refused hosts
  use the portable Linux guest installer.
- Artifacts bind pinned provenance, a platform/version matrix, `SHA256SUMS`, an
  operator detached signature and a CycloneDX SBOM. Verification fails closed.
- First-boot identity and the ADR0019 digest-only enrollment bundle are created
  on the persistent volume; no secret, private key or credential appears in the
  template or provisioning metadata.
- Persistent worlds and identity are imported, backed up and restored with
  integrity checks and data-preserving refusals. Upgrade/rollback uses verified
  staged slots.
- Only disposable offline fixture qualification is claimed; live import/boot,
  effective enforcement and gameplay compatibility are not.
