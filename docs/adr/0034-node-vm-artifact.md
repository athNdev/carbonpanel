# ADR 0034: Reproducible node-VM artifact (QCOW2 + cloud-init)

Status: Accepted (design and offline fixtures only; no live qualification)

Date: 2026-11-18

## Context

OSS-368's managed-node runtime offers a VM-isolation option for operators who
need a stronger boundary than shared-kernel containers. OSS-377 asks for a
reproducible QCOW2 + cloud-init image for Proxmox VE and generic KVM/libvirt.
The shared enrollment/control contract (ADR 0019, PRs 516/517/518) is already
merged and must not be forked. This artifact lane is an independent writer; it
does not own proto, auth middleware, the daemon runtime or publication.

No live host VM, global host configuration, registry push or release is in this
lane's scope. The checked-in result is therefore a build definition plus
offline fixtures, not a published image.

## Decision

1. **One owned VM per node.** The artifact is a dedicated QCOW2 guest image
   with a NoCloud cloud-init seed. Each VM has a unique instance-id, domain
   UUID and MAC; no identity is shared across VMs. Isolation properties and
   their limits are documented in `packaging/node-vm/docs/isolation.md`; no
   proven-isolation claim is made.

2. **Pinned, verified provenance.** The base image is pinned by distro, version,
   arch, URL and an upstream GPG signing-key fingerprint in
   `packaging/node-vm/provenance/base-images.json`. Checked-in digests are
   `fixture-placeholder`; the guarded apply boundary must fetch the upstream
   GPG-signed checksum file, require `digest_status: verified` and refuse any
   fallback to another image or a host image. Supported platform/version
   targets are recorded in `packaging/node-vm/MATRIX.md`.

3. **Deterministic content, honest byte boundary.** Cloud-init seed files,
   checksum manifest, CycloneDX 1.5 SBOM and build plan are produced from pinned
   templates, public metadata and `SOURCE_DATE_EPOCH`, with sorted canonical
   JSON and a determinism `--check`. The raw QCOW2 bytes are **not** claimed
   byte-reproducible across unpinned qemu-img versions; the image is verified by
   the checksum manifest from a pinned toolchain instead.

4. **No enrollment input in the image or metadata.** First boot generates a
   CSPRNG >=256-bit secret, salt and Ed25519 keypair locally. It exports only a
   digest-only bundle (salt, digest, `CSR_PRE`, approved SPKI, fingerprint) for
   out-of-band operator registration under the SHARED ADR0019 contract.
   Bootstrap scopes are limited to `BeginEnrollmentChallenge` and
   `ClaimEnrollment`; the private key and secret stay in root-only VM-local
   files and never enter the image, seed, SBOM, manifest or build plan. This
   lane defines no RPC and forks no contract.

5. **Data-preserving staged lifecycle.** Upgrades write a checksummed undo
   manifest before promotion and recheck health before completing. Rollback
   restores only after the undo manifest verifies, keeps the current artifact
   for forensics and never deletes a volume. World, backup, panel DB and daemon
   data-root paths are preserved; uncertain outcomes block retry. Volume
   import/backup/recovery are copy-only and hash-verified.

6. **Packaging, not publishing.** No registry push, release/tag or hosted
   signature is produced. Detached signing is described as an operator action
   on an owned host with the key identity supplied via environment; no key
   material is embedded in any artifact. ADR 0010/0033 publication gates remain
   untouched.

7. **Fixture-only qualification.** `packaging/node-vm/qualification/` records
   the disposable owned-VM acceptance design and synthetic fixtures. Success is
   not claimed from an unimplemented handler, an offline parse or a fixture.
   Live install/network/enrollment/upgrade evidence belongs to future owned
   disposable runs.

## Consequences

- `packaging/node-vm/` is a new top-level `packaging` area governed by the
  `node-vm-artifact` architecture concept; build logic stays in
  `scripts/node_vm_*.py` following the existing script/test convention.
- All fixture inputs are synthetic; no real key, token, endpoint, host path or
  world data is checked in. `digest_status: fixture-placeholder` is a hard stop
  for apply, not a permission.
- The shared node-control contract, per-node auth scopes, auth middleware and
  daemon lifecycle are unchanged; this lane only consumes the contract.
- ADR number 0034 is used under the orchestrator's central allocation: 0014 is
  the merged baseline, 0015-0033 are allocated by other tracks. The Track C
  index owner writes the single `knowledge/decisions/adrs.md` row; this lane
  supplies that row text in its report rather than editing the shared index.

Suggested index row (for the orchestrator-owned index):

```
| [0034](../../docs/adr/0034-node-vm-artifact.md) | Reproducible, secret-free QCOW2/cloud-init node VM artifact with pinned provenance and staged rollback | [Node VM artifact](../architecture/node-vm-artifact.md) |
```

## Verification (offline evidence in this commit)

- `python -m py_compile` on all `scripts/node_vm_*.py`, `scripts/test_node_vm_*.py`
  and the guest first-boot module: clean.
- `python -m unittest discover -s scripts -p 'test_node_vm_*.py'`: 27 tests pass
  (identity uniqueness, digest-only bundle, baked-secret refusal, deterministic
  seed/manifest, CycloneDX SBOM, signing plan, staged upgrade/rollback and
  copy-only volume safety).
- `python scripts/node_vm_build.py --check`: deterministic; emitted plan is
  `fixture_only` with provenance status `fixture-placeholder`.
- `python scripts/node_vm_integrity.py --verify`: checksum manifest verifies;
  SBOM is CycloneDX 1.5; signing plan has no baked key.
- `python scripts/node_vm_upgrade.py ...`: staged undo manifest emitted.
- No command invoked qemu-img, xorriso, virt-customize, a hypervisor, a registry
  or a signing service.
