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
  - packaging/node-lxc/config/provenance.fixture.json
  - packaging/node-lxc/qualification/QUALIFICATION.md
  - packaging/node-lxc/qualification/SIGNED-ARCHIVE-EVIDENCE.md
  - packaging/node-lxc/tests/test_node_lxc.py
  - docs/node-lxc-mode.md
sources:
  - title: ADR 0036 unprivileged LXC node template
    resource: ../../docs/adr/0036-node-lxc-template.md
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
- The artifact profile (`config/provenance.json`) pins real, immutable, dated
  Ubuntu 24.04 root tarballs with SHA-256 values from a GPG-signed
  `SHA256SUMS`; retrieval evidence is in `qualification/`. Synthetic pins exist
  only in `config/provenance.fixture.json`. Artifact mode refuses synthetic
  profiles, placeholder/unverified digests, missing signature evidence and
  moving URLs. Artifacts also bind a platform/version matrix, `SHA256SUMS`, an
  operator detached signature and a CycloneDX SBOM; verification fails closed.
- `verify-signed-archive` checks the real dated release in an owned disposable
  path: it refuses synthetic/placeholder/moving-URL pins, verifies the detached
  GPG signature of `SHA256SUMS` against the pinned key fingerprint, matches the
  signed sum to the pin, rehashes the **full downloaded** root and LXD tarballs,
  checks content length and inspects usrmerged `os-release` and LXD
  `metadata.yaml` for Ubuntu 24.04 and the requested architecture. Byte
  verification is artifact evidence, never guest-boot evidence
  ([signed-archive evidence](../../packaging/node-lxc/qualification/SIGNED-ARCHIVE-EVIDENCE.md)).
- First-boot identity and the ADR0019 digest-only enrollment bundle are created
  on the persistent volume; no secret, private key or credential appears in the
  template or provisioning metadata.
- Persistent worlds and identity are imported, backed up and restored with
  integrity checks and data-preserving refusals. Upgrade/rollback uses verified
  staged slots.
- Runtime behavior is qualified only by disposable offline fixtures. Real guest
  boot is externally blocked on the authoring host (no `/dev/kvm`, no LXC
  tooling; the owned WSL 2 distribution has neither and installing would need
  elevation/host configuration); live import/boot, effective kernel enforcement
  and gameplay compatibility are not claimed. Pin and signed-archive byte
  verification is evidence about upstream artifacts, not boot evidence.
