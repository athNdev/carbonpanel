# Disposable owned-VM qualification design (fixtures only)

Status: **DESIGN + OFFLINE FIXTURES, plus directly retrieved upstream base-image
provenance.** No live host VM has been created, altered, provisioned or deleted
by this lane. No global host configuration was changed. No live enrollment,
daemon, game server, registry push or release was run. Runtime claims remain
fixture-based; the base-image pins are real and are reported separately below.

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
| Base-image pin | Direct GET/HEAD from cloud-images.ubuntu.com + GPG verify of SHA256SUMS (real hashes, immutable dated URLs) | Full image download/rehash; any real QCOW2 build |
| Seed/build determinism | Offline fixture tests + `node_vm_build.py --check` | Byte-identical QCOW2 across machines |
| No secret in image/seed/metadata | Static invariants + fixture assertions with synthetic sentinels | Real secret handling on a live VM |
| Checksum/SBOM generation | Deterministic unit fixtures | Signed/published artifacts |
| Upgrade/rollback logic | Pure planner fixtures with synthetic bytes | Applied upgrade on a live VM |
| Persistent volume safety | Copy-only planner fixtures | Real volume import/recovery |
| First-boot uniqueness | Derived `node_id` fixture for distinct instance-ids | Real VM boot and mTLS enrollment |
| Isolation | Documented model (`docs/isolation.md`) | Effective policy/breakout testing |

## Qualification scenarios (future, owned disposable VMs only)

Each requires a freshly owned, disposable VM that is torn down after the run;
worlds/backups from other installations are never touched.

1. **Clean boot.** Import QCOW2 + NoCloud seed, boot, verify cloud-init applies
   and first boot writes a digest-only bundle to a root-only local path. Verify
   no secret appears in seed, serial console output or VM metadata. Verify a
   unique `node_id` for two VMs with different instance-ids.
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
