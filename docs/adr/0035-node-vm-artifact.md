# ADR 0035: Reproducible node-VM artifact (QCOW2 + cloud-init)

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

2. **Pinned, verified provenance.** The base image is pinned to an **immutable
   dated** upstream release entry, never a moving path: Ubuntu 24.04 LTS
   `release-20261017` for amd64 and arm64. `packaging/node-vm/provenance/base-images.json`
   records the exact image URLs, real non-placeholder SHA256 values from the
   upstream GPG-signed `SHA256SUMS`, the signing key fingerprint
   `D2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81`, and direct-retrieval evidence in
   `provenance/RETRIEVAL-EVIDENCE.md`. Synthetic placeholder digests are confined
   to `provenance/base-images.fixture.json` and are valid only in explicit
   `--profile fixture` offline test mode. Artifact mode calls
   `verify_artifact_provenance()` and **fails closed** on placeholder/synthetic
   digests, moving `/release/` or `/current/` URLs, or missing signature
   evidence. A real build must still download the image and recompute its
   SHA256 against the signed checksum before the guarded apply boundary acts;
   no fallback to another image or a host image is permitted. Supported
   platform/version targets are recorded in `packaging/node-vm/MATRIX.md`.

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

   SIG_CLAIM follows the **shared** ADR0019/OSS-385 `ClaimContextRaw` encoding
   (`internal/noderuntime/enroll`, PR532 head `fc998875`): NUL-delimited raw
   bytes over the server-stored nonce, enrollment `expires_at` in RFC3339Nano,
   comma-joined proto capability names and the channel context, with no
   trailing separator, no client nonce and no canonical-JSON step. Signing is
   Ed25519-only. A golden produced from the shared package is checked in at
   `packaging/node-vm/qualification/fixtures/claim-context-shared.json`; VM
   fixtures assert byte-for-byte equality and that a canonical-JSON-signed
   claim is refused. Guest Python cannot import a Go package, so the VM
   generator reproduces the single encoder for fixtures only; any production
   mount must delegate the claim to the shared package rather than add a
   second encoder.

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
- Runtime fixture inputs are synthetic; no real key, token, endpoint, host path
  or world data is checked in. `digest_status: fixture-placeholder` is a hard
  stop for artifact mode, not a permission. The **base-image digests and URLs
  are real** (direct official-source retrieval, signature-verified); this is the
  one non-fixture input and is reported with its own evidence file.
- No real QCOW2 has been built. The verified hash is the upstream published
  value; recomputing it over downloaded image bytes remains a future real-build
  step, as do live VM, host, enrollment and deployment qualification.
- The shared node-control contract, per-node auth scopes, auth middleware and
  daemon lifecycle are unchanged; this lane only consumes the contract.
- ADR number 0035 is used under the orchestrator's central allocation. 0014 is
  the merged baseline; 0015-0033 are allocated to other tracks and 0034 is the
  merged MCP server distribution ADR. Numbers 0036 (node LXC template), 0037
  (portable installer) and 0038 (host-native private daemon) are reserved for
  sibling lanes and are not used here. The orchestrator owns the single
  `knowledge/decisions/adrs.md` row; this lane supplies that row text in its
  report rather than editing the shared index.

Suggested index row (for the orchestrator-owned index):

```
| [0035](../../docs/adr/0035-node-vm-artifact.md) | Reproducible, secret-free QCOW2/cloud-init node VM artifact with pinned provenance and staged rollback | [Node VM artifact](../architecture/node-vm-artifact.md) |
```

## Verification (real provenance retrieval + offline evidence)

- **Base-image pin (real, not fixture):** direct HTTPS GET/HEAD to
  `cloud-images.ubuntu.com` for the immutable `release-20261017` directory;
  SHA256SUMS retrieved (HTTP 200, 7843 bytes) and its GPG signature verified
  with gpg 2.4.9 against the official `UEC Image Automatic Signing Key`
  (fingerprint `D2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81`, fetched from the
  Ubuntu keyserver by the signature's own issuer fingerprint). Both image URLs
  returned HTTP 200 on HEAD. Exact evidence and limits:
  `packaging/node-vm/provenance/RETRIEVAL-EVIDENCE.md`.
- `python scripts/node_vm_build.py --profile artifact --check`: accepted only
  with the real verified pins; `verify_artifact_provenance()` refuses fixture
  digests and moving URLs. Fixture mode is used by the offline tests below.

- **Shared claim context (real conformance):** a golden context was generated
  by running `internal/noderuntime/enroll.ClaimContextRaw` from PR532 head
  `fc998875` in an owned temporary worktree; the VM first boot reproduces those
  exact bytes and refuses the previous canonical-JSON form. Golden inputs and
  bytes are in `packaging/node-vm/qualification/fixtures/claim-context-shared.json`.
  No shared writer worktree was edited.
- `python -m py_compile` on all `scripts/node_vm_*.py`, `scripts/test_node_vm_*.py`
  and the guest first-boot module: clean.
- `python -m unittest discover -s scripts -p 'test_node_vm_*.py'`: 30 tests pass
  (identity uniqueness, digest-only bundle, baked-secret refusal, deterministic
  seed/manifest, CycloneDX SBOM, signing plan, staged upgrade/rollback,
  copy-only volume safety, byte-exact shared context, JSON refusal and
  Ed25519-only).
- `python scripts/node_vm_build.py --profile fixture --check`: deterministic;
  emitted plan is `fixture_only` with provenance status `fixture-placeholder`.
- `python scripts/node_vm_integrity.py --profile fixture --verify`: checksum
  manifest verifies;
  SBOM is CycloneDX 1.5; signing plan has no baked key.
- `python scripts/node_vm_upgrade.py ...`: staged undo manifest emitted.
- No command invoked qemu-img, xorriso, virt-customize, a hypervisor, a registry
  or a signing service.
