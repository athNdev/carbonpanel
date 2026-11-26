# Disposable owned-VM qualification design (fixtures plus disposable guest runs)

Status: **DESIGN + OFFLINE FIXTURES + DISPOSABLE QEMU GUEST RUNS, plus directly
retrieved upstream base-image provenance.** No live host VM has been created,
altered, provisioned or deleted by this lane. No global host configuration was
changed. No live enrollment, daemon, game server, registry push or release was
run. Disposable overlays booted twice under UEFI/TCG; effective isolation,
controller mTLS, heartbeat and daemon health remain unqualified. Exact run
digests and limits are in
[`VM-BOOT-QUALIFICATION.md`](VM-BOOT-QUALIFICATION.md).

## Real base-image provenance (direct official-source retrieval, 2026-11-18)

The checked-in artifact profile pins immutable dated Ubuntu 24.04 `release-20261017`
images for amd64 and arm64 with real SHA256 values from the upstream
GPG-signed `SHA256SUMS`, verified locally against the official Ubuntu image
signing key. Exact URLs, digests, HTTP statuses, checksum-file SHA256 and GPG
output are in [`provenance/RETRIEVAL-EVIDENCE.md`](../provenance/RETRIEVAL-EVIDENCE.md).
Synthetic placeholders are confined to `provenance/base-images.fixture.json`,
used only by offline tests; artifact mode fails closed on them.

## Evidence boundary

| Claim | Evidence in this lane | Not claimed here |
| --- | --- | --- |
| Base-image pin | Direct GET/HEAD from cloud-images.ubuntu.com + GPG verify of SHA256SUMS (real hashes, immutable dated URLs) | QCOW2 byte-reproducibility across qemu-img versions; signed/published artifact |
| Real image bytes | Full amd64 and arm64 download, SHA256 rehash and `qemu-img info` against the pin | A rebuilt or published QCOW2 built from those bytes |
| Seed/build determinism | Offline fixture tests + `node_vm_build.py --check` | Byte-identical QCOW2 across machines |
| No secret in image/seed/metadata | Static invariants + fixture assertions with synthetic sentinels + serial/ISO scans of the disposable boots | Exhaustive proof that no secret exists anywhere in the guest |
| Checksum/SBOM generation | Deterministic unit fixtures | Signed/published artifacts |
| Upgrade/rollback logic | Pure planner fixtures with synthetic bytes | Applied upgrade on a live VM |
| Persistent volume safety | Copy-only planner fixtures | Real volume import/recovery |
| First-boot uniqueness | Derived `node_id` fixture for distinct instance-ids + observed first-boot bundle for the qualified instance | Two concurrently live VMs with completed out-of-band registration |
| Shared SIG_CLAIM context | Byte-for-byte golden from shared `ClaimContextRaw` at PR532 `fc998875`; JSON-signed claim refused; server-stored nonce, Ed25519-only | Production mount delegating to the shared Go package; live claim verification |
| Clean boot and identity persistence | Two disposable UEFI/TCG boots; cloud-init completion, DHCP lease, root-only bundle/key/secret and a non-repeated second boot | Live host deployment, arm64 boot, 20 GiB q35 boot, second clean shutdown receipt |
| Isolation | Documented model (`docs/isolation.md`) | Effective policy/breakout testing |

## Current disposable-run evidence (2026-11-19/20)

Summarised from [`VM-BOOT-QUALIFICATION.md`](VM-BOOT-QUALIFICATION.md) sections
12–14; that file owns the exact serial-log, seed, script and firmware digests.

- The verified amd64 base booted twice under UEFI/TCG on the same disposable
  3.5 GiB overlay. The first boot completed NoCloud cloud-init, acquired IPv4
  DHCP on `ens3`, wrote `node.key`, `enrollment-bundle.json` and
  `enrollment-secret.bin` as `0600 root:root` in a `0700` directory, and emitted
  the public node ID/fingerprint marker.
- The second boot reused the overlay, completed cloud-init without re-emitting
  the identity marker, and an explicit rerun returned the original node ID and
  fingerprint. Neither serial capture nor the CIDATA ISO contained the
  enrollment token or a private-key PEM marker.
- A separate WSL fixture submitted the corrected guest script's DER CSR and
  fresh signature to the checkout's shared Go enrollment authority. Valid proof
  was accepted with a matching certificate; changed signature, replay and wrong
  node scope were refused.
- The 20 GiB q35 overlay entered emergency mode. The successful UEFI retry used
  i440fx at the base image's 3.5 GiB virtual size, so the declared 20 GiB disk
  profile remains unqualified. Live guest-to-authority registration over SSH,
  controller mTLS, heartbeat, daemon health, arm64 and the Proxmox/libvirt
  deployment matrix also remain open.
- `scripts/node_vm_boot_qual.py` now resolves OVMF/edk2 for UEFI (failing closed
  instead of falling back to BIOS), copies a fresh private variable store per
  run, and can request an expanded qcow2 overlay. Those command paths are
  unit-tested only; they do not close the 20 GiB boot gate.

## Qualification scenarios (remaining runs on owned disposable VMs only)

Each requires a freshly owned, disposable VM that is torn down after the run;
worlds/backups from other installations are never touched.

1. **Clean boot.** Import QCOW2 + NoCloud seed, boot, verify cloud-init applies
   and first boot writes a digest-only bundle to a root-only local path. Verify
   no secret appears in seed, serial console output or VM metadata. Verify a
   unique `node_id` for two VMs with different instance-ids. *Partially observed
   on the amd64 3.5 GiB overlay (identity persistence, second boot); the 20 GiB
   profile, arm64 and two concurrent VMs remain to run.*
2. **Enrollment refusal.** With the SHARED ADR0019 contract, verify a valid
   operator-approved key + fresh post-challenge signature enrolls; a mismatched
   key, replayed nonce, caller-declared fingerprint or CSR_PRE-as-proof is
   refused. This scenario consumes the node-principal contract owned with the
   auth lane; no RPC is mounted by this artifact lane.
3. **Coexistence / isolation.** On a disposable host with another engine
   running, verify no socket/root/namespace/port collision and no host socket
   bind inside the VM. Inspect effective config, do not assert proven isolation.
4. **Upgrade + rollback.** Stage v1→v2, verify checksum before promotion,
   confirm health recheck, then force an undo-verification failure and confirm
   rollback is refused and retry blocked (`UNCERTAIN`). Confirm worlds/backups
   and daemon data root are unchanged.
5. **Persistent volume import/backup/recover.** Copy-only import into a
   preserved path; backup hashes the volume; recovery refuses a hash mismatch
   and never deletes the source.
6. **Artifact integrity.** Regenerate SHA256SUMS and CycloneDX SBOM from pinned
   inputs and confirm byte-identical output; confirm a corrupted checksum is
   detected. Confirm signing is described (detached signature on an owned host)
   with no key material in the artifact.

## Fixture inventory (checked in)

- `fixtures/first-boot-instance-a.json`, `first-boot-instance-b.json` — two
  distinct synthetic instance-ids with their derived node_ids (public data).
- `fixtures/enrollment-bundle.json` — synthetic digest-only bundle; contains no
  secret or private key, clearly labeled `SYNTHETIC`.
- `fixtures/upgrade-sequence.json` — synthetic v1→v2 staged upgrade with an
  undo manifest and expected refusal cases.
- `fixtures/fixture-config.json` — pinned config used by the offline tests.

All fixture values are synthetic placeholders. No real key, token, password,
hostname, endpoint or world data is stored here.

## Running the offline fixtures

```sh
python -m unittest discover -s scripts -p 'test_node_vm_*.py' -v
```
