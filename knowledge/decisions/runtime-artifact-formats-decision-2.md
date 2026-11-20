---
type: Decision
title: "Runtime artifact format support — Decision (part 2)"
description: "A filename suffix or byte count is never a deployable-artifact claim."
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: runtime-artifact-formats.md
---
A filename suffix or byte count is never a deployable-artifact claim.
`scripts/artifact_kind_verifiers.py` qualifies each payload with its REAL
verifier, reusing existing code: QCOW2 header plus `qemu-img info` tool
inspection with digest/size bound by `scripts/node_vm_verify_image.py`; the
`packaging/node-lxc/node_lxc_artifact.py` archive/layout inspector for rootfs;
and `internal/nodeinstall.VerifyArtifact` via the installer `verify` CLI for the
expected package structure. Provenance is bound to the EXACT payload SHA-256,
and symlinks or non-regular files are refused. If a native validator is
unavailable (for example no `qemu-img` on the runner), publication STOPS and the
lane stays REHEARSAL-ONLY; these are preconditions, not a substitute for the
consumer's cryptographic verification. The predicate does not gate the offline
rehearsal reachability seam. The trusted workflow checkout must also provide
one reviewed qualification record for the version, kind and source commit.
The publish job compares the generated `SHA256SUMS` digest with that record
before signing. Green CI and valid payload structure alone cannot enable
publication. The release tag must resolve to the qualified source commit. Publication requires an explicit dispatch with `publish=true`.
