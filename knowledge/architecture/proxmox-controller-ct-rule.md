---
type: Architecture
title: "Proxmox controller CT — Rule"
description: "Invariants for the controller CT artifact and publication"
status: stable
owners: [oss-runtime]
code_refs: [packaging/proxmox-controller-ct, scripts/proxmox_controller_ct.py]
sources:
  - title: Parent concept
    resource: proxmox-controller-ct.md
---
# Rule

- The CT is unprivileged amd64 with only `nesting=1,keyctl=1`; AppArmor and
  default profiles stay unchanged. No host Docker socket, privileged flag,
  device passthrough, live host write or guest provisioning is part of this lane.
- Rootfs bytes come from signature-verified Debian 13 apt. Upstream copyright
  metadata is retained; the build receipt records exact package versions,
  per-file hashes and the signed base Release digest.
- GNU tar preserves numeric ownership, modes, links, xattrs and ACLs. Python
  never rewrites tar metadata. The archive has no `rootfs/` wrapper.
- First boot never marks a failed pull as success; the installer owns resume,
  refusals and preservation of operator state.
- Publication uses only the same-run verified artifact, exact `main` API SHA,
  successful exact-commit main CI and `publish=true`. It creates a draft
  prerelease in the `controller-ct-v*` domain, never a product release or alias.
