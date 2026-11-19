---
type: Architecture
title: Node VM artifact
description: Reproducible, secret-free QCOW2/cloud-init node VM builds with pinned provenance, scoped first-boot enrollment and staged rollback; fixture-only, not published.
status: draft
owners: [oss-runtime]
code_refs:
  - packaging/node-vm
  - scripts/node_vm_build.py
  - scripts/node_vm_integrity.py
  - scripts/node_vm_upgrade.py
  - scripts/node_vm_verify_image.py
  - scripts/node_vm_boot_qual.py
  - scripts/test_node_vm_build.py
  - scripts/test_node_vm_integrity.py
  - scripts/test_node_vm_upgrade.py
  - scripts/test_node_vm_enroll.py
  - scripts/test_node_vm_verify_image.py
  - scripts/test_node_vm_boot_qual.py
  - packaging/node-vm/qualification/VM-BOOT-QUALIFICATION.md
sources:
  - title: ADR 0035 node VM artifact
    resource: ../../docs/adr/0035-node-vm-artifact.md
  - title: OSS-377 VM boot qualification evidence
    resource: ../../packaging/node-vm/qualification/VM-BOOT-QUALIFICATION.md
  - title: ADR 0019 node control contract (shared, not forked)
    resource: ../../docs/adr/0019-node-control-contract.md
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
  bytes verified 2026-11-19, see `VM-BOOT-QUALIFICATION.md`). No QCOW2 was built
  or published; no live VM, enrollment, daemon or deployment success is claimed.
  A real clean boot is blocked where no `qemu-system` binary or `/dev/kvm`
  exists; record that exact blocker rather than claiming boot evidence.
  Disposable owned-VM acceptance is designed in
  `packaging/node-vm/qualification/QUALIFICATION.md`.

# Why

A shared image with baked credentials or an unpinned base would defeat the
fail-closed enrollment boundary and make two nodes share identity. Independent
build inputs and a secret-free first boot keep the VM option compatible with
the shared node-control contract without duplicating it.

# How to check

- `python -m py_compile scripts/node_vm_*.py scripts/test_node_vm_*.py
  packaging/node-vm/cloud-init/firstboot/enroll.py`
- `python -m unittest discover -s scripts -p 'test_node_vm_*.py'` (offline,
  synthetic keys; fixture provenance profile)
- `python scripts/node_vm_build.py --profile artifact --check` (real dated pins;
  refuses placeholder digests) and `--profile fixture --check` (offline plan)
- `python scripts/node_vm_integrity.py --profile fixture --verify` (checksum +
  CycloneDX SBOM + key-free signing plan)
- `python scripts/node_vm_upgrade.py --from-revision A --to-revision B
  --batch-id ID` (staged undo plan)
- `python3 scripts/node_vm_verify_image.py --image <downloaded.img> --ref
  ubuntu-24.04-amd64 --sha256sums <SHA256SUMS>` (real-byte check, offline and
  read-only; requires a caller-supplied image file)
- `python3 scripts/node_vm_boot_qual.py --work-dir <owned-tmp> --generate-seed`
  prepares deterministic seed/ISO inputs and reports qemu availability; with
  `--image <verified.qcow2>` on a capable disposable runner (qemu-system with
  TCG or KVM) it attempts a boot, captures serial-console evidence and can
  perform offline disk inspection. Report blocked/unobserved gates honestly;
  never claim live boot, enrollment or network isolation without evidence.
- Never substitute a real key, endpoint or world path for a fixture value, and
  never run these drivers against a live VM from this lane.

# Read next

- [ADR 0035](../../docs/adr/0035-node-vm-artifact.md)
- [Node control protocol](node-control-protocol.md)
- [Runtime seams](runtime-seams.md)
