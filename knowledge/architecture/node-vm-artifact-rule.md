---
type: Architecture
title: "Node VM artifact — Rule"
description: "The node VM artifact (packaging/node-vm/, drivers in scripts/node_vm_*.py)"
status: draft
owners: [oss-runtime]
code_refs: [packaging/node-vm, scripts/node_vm_build.py, scripts/node_vm_integrity.py, scripts/node_vm_upgrade.py, scripts/node_vm_verify_image.py, scripts/test_node_vm_build.py, scripts/test_node_vm_integrity.py, scripts/test_node_vm_upgrade.py, scripts/test_node_vm_enroll.py, scripts/test_node_vm_verify_image.py, packaging/node-vm/qualification/VM-BOOT-QUALIFICATION.md]
sources:
  - title: Parent concept
    resource: node-vm-artifact.md
---
# Rule

The node VM artifact (`packaging/node-vm/`, drivers in `scripts/node_vm_*.py`)
is the QCOW2 + cloud-init build definition for the VM-isolation option of the
managed-node runtime. It is packaging only: no live provisioning, no host
config change, no registry push and no release.

- One owned VM per node. Each VM has a unique instance-id, domain UUID and MAC;
  identities are never shared. The boundary is a separate kernel, not a proven
  isolation claim ([isolation](../../packaging/node-vm/docs/isolation.md)).
- Base images are pinned to **immutable dated** Ubuntu 24.04 `release-20261017`
  URLs (amd64/arm64) with **real verified SHA256** values from the upstream
  GPG-signed `SHA256SUMS` and the official signing key fingerprint
  `D2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81`; retrieval evidence is in
  `packaging/node-vm/provenance/RETRIEVAL-EVIDENCE.md`. Synthetic placeholders
  exist only in `base-images.fixture.json` for explicit fixture-mode tests.
  Artifact mode fails closed on placeholder/synthetic digests, moving
  `/release/` or `/current/` URLs, or missing signature evidence; a real build
  must still download and rehash the image. No host or other-image fallback.
  The target matrix is `packaging/node-vm/MATRIX.md`.
- Seed files, checksum manifest, CycloneDX 1.5 SBOM and build plan are
  deterministic from pinned inputs and `SOURCE_DATE_EPOCH`; `--check` asserts
  byte-identical rebuilds. Raw QCOW2 bytes are verified by checksum, not claimed
  byte-reproducible across unpinned qemu-img versions.
- **No enrollment input is baked into the image, cloud-init seed or provisioning
  metadata.** First boot generates the secret, salt and Ed25519 keypair locally
  and exports only a digest-only bundle under the **SHARED** ADR0019 contract.
  This lane does not define, fork or mount a node-control RPC; bootstrap scopes
  are limited to `BeginEnrollmentChallenge` and `ClaimEnrollment`. SIG_CLAIM
  uses the shared `ClaimContextRaw` NUL-delimited byte encoding (server-stored
  nonce, enrollment expiry, proto capability names, channel context; Ed25519
  only; canonical JSON refused), proven byte-for-byte against a golden from
  PR532 `fc998875`. Guest Python mirrors the encoder for fixtures only; a
  production mount must delegate to the shared package. See
  [node control protocol](node-control-protocol.md).
- Upgrades are staged: write and verify a checksummed undo manifest before
  promotion, recheck health before completion. Rollback restores only a
  verified undo, keeps the current artifact for forensics and never deletes a
  volume. World/backup/DB and daemon data-root paths are preserved; `UNCERTAIN`
  blocks retry. Volume import/backup/recovery are copy-only and hash-verified.
- Signing is an operator action on an owned host with the key identity supplied
  via environment. No private key, passphrase or token is in the build config
  or any artifact. ADR 0010/0033 publication gates are unchanged.
- Runtime fixtures are synthetic and offline tests prove determinism, refusal
  and planner semantics only. The base-image pin is real and signature-backed;
  `scripts/node_vm_verify_image.py` now verifies that an already-downloaded
  image's real bytes match the pin and the signed `SHA256SUMS` (amd64/arm64
  bytes verified 2026-11-19, see `VM-BOOT-QUALIFICATION.md`).
