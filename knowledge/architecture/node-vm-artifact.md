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
  - scripts/test_node_vm_build.py
  - scripts/test_node_vm_integrity.py
  - scripts/test_node_vm_upgrade.py
  - scripts/test_node_vm_enroll.py
sources:
  - title: ADR 0034 node VM artifact
    resource: ../../docs/adr/0034-node-vm-artifact.md
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
- Base images are pinned by version, arch, URL and upstream GPG key. Checked-in
  digests are `fixture-placeholder` and are a hard stop for apply until an
  operator records a `verified` digest from the signed upstream checksum file.
  No fallback to a host or other image. The target matrix is
  `packaging/node-vm/MATRIX.md`.
- Seed files, checksum manifest, CycloneDX 1.5 SBOM and build plan are
  deterministic from pinned inputs and `SOURCE_DATE_EPOCH`; `--check` asserts
  byte-identical rebuilds. Raw QCOW2 bytes are verified by checksum, not claimed
  byte-reproducible across unpinned qemu-img versions.
- **No enrollment input is baked into the image, cloud-init seed or provisioning
  metadata.** First boot generates the secret, salt and keypair locally and
  exports only a digest-only bundle under the **SHARED** ADR0019 contract. This
  lane does not define, fork or mount a node-control RPC; bootstrap scopes are
  limited to `BeginEnrollmentChallenge` and `ClaimEnrollment`. See
  [node control protocol](node-control-protocol.md).
- Upgrades are staged: write and verify a checksummed undo manifest before
  promotion, recheck health before completion. Rollback restores only a
  verified undo, keeps the current artifact for forensics and never deletes a
  volume. World/backup/DB and daemon data-root paths are preserved; `UNCERTAIN`
  blocks retry. Volume import/backup/recovery are copy-only and hash-verified.
- Signing is an operator action on an owned host with the key identity supplied
  via environment. No private key, passphrase or token is in the build config
  or any artifact. ADR 0010/0033 publication gates are unchanged.
- Fixtures are synthetic. Offline tests prove determinism, refusal and planner
  semantics only; no live VM, enrollment, daemon or deployment success is
  claimed. Disposable owned-VM acceptance is designed in
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
  synthetic keys only)
- `python scripts/node_vm_build.py --check` (deterministic, fixture-only plan)
- `python scripts/node_vm_integrity.py --verify` (checksum + CycloneDX SBOM +
  key-free signing plan)
- `python scripts/node_vm_upgrade.py --from-revision A --to-revision B
  --batch-id ID` (staged undo plan)
- Never substitute a real key, endpoint or world path for a fixture value, and
  never run these drivers against a live VM from this lane.

# Read next

- [ADR 0034](../../docs/adr/0034-node-vm-artifact.md)
- [Node control protocol](node-control-protocol.md)
- [Runtime seams](runtime-seams.md)
