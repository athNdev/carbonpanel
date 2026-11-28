# Disposable owned-VM qualification design (fixtures plus disposable guest runs)

Status: **DESIGN + OFFLINE FIXTURES + DISPOSABLE QEMU GUEST RUNS + A LINUX
CONTROLLER/AGENT FIXTURE, plus directly retrieved upstream base-image
provenance.** No live host VM has been created, altered, provisioned or deleted
by this lane. No global host configuration was changed. No live enrollment,
daemon, game server, registry push or release was run. Disposable amd64 and
arm64 overlays booted under UEFI/TCG on QEMU; the arm64 run includes a clean
reboot and a second instance identity. A separate Linux-only fixture covers
challenge, scoped mTLS, heartbeat and a fake-daemon health probe against the
real shared enrollment authority and agent; effective isolation, a real managed
daemon and guest-to-controller enrollment remain unqualified. Exact run digests
and limits are in [`VM-BOOT-QUALIFICATION.md`](VM-BOOT-QUALIFICATION.md).

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
| Controller mTLS, heartbeat and health | Linux-only controller/agent fixture: real production enrollment authority + real `agent.Run`, TLS 1.3 channel binding, scoped certificate, preflight/heartbeat, restart reuse, anonymous/unenrolled/cross-node/revoked refusals, fake-daemon pin | Guest-to-controller claim over a real VM network, a production-mounted control RPC and a real dockerd |
| Clean boot and identity persistence | Disposable amd64 UEFI/TCG boots on both the base 3.5 GiB i440fx disk and a 20 GiB q35 overlay. The expanded profile resized ext4 and retained the enrollment bundle across a clean second boot. Disposable arm64 QEMU `virt`/UEFI boots wrote root-only enrollment files, retained them across a QMP-driven clean reboot, and produced a different node ID for a second instance-id. | arm64 20 GiB expansion (GPT normalization corrected offline; no hosted boot yet), Proxmox/libvirt host deployment and live standby recovery |
| Isolation | Documented model (`docs/isolation.md`) | Effective policy/breakout testing |

## Current disposable-run evidence (2026-11-19 through 2026-11-26)

Summarised from [`VM-BOOT-QUALIFICATION.md`](VM-BOOT-QUALIFICATION.md) sections
12–16. That file records serial-log, seed, script and firmware digests.

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
- A fresh 20 GiB q35 overlay booted twice with UEFI/TCG. The first boot grew
  the root partition and ext4. The second retained the byte-identical bundle
  and private file modes, emitted no new enrollment identity, and shut down
  cleanly through ACPI.
- The pinned arm64 image booted on QEMU `virt` with AAVMF UEFI and TCG at the
  base 3.5 GiB virtual size. The first boot completed cloud-init and emitted
  node ID `node-78805a9750fa9efee1fb830643f1b893`; `debugfs` showed a `0700`
  root-owned enrollment directory with `0600` key, bundle and secret files. A
  clean second boot completed cloud-init, was powered down through QMP, emitted
  no new identity, and retained the byte-identical bundle. A second disposable
  instance with a distinct instance-id emitted the different node ID
  `node-8bd7872baf90ee073c7238d87d4f8730`. The 20 GiB arm64 expansion is not
  qualified because the guest did not find its BOOT/UEFI labels before
  `growpart`; the amd64 20 GiB evidence is separate. The harness now expands a
  read-only flattened copy, sizes the protective MBR to the new last LBA and
  relocates the GPT backup header to the new end before boot; that correction
  is offline-fixture-tested only and still needs a
  hosted expanded boot (sections 19.3-19.4 of the boot report).
- A Linux-only controller/agent fixture ran the real shared enrollment
  authority and `agent.Run` over loopback TLS 1.3. It passed challenge,
  TLS-bound claim, scoped mTLS, preflight/heartbeat, persisted-credential reuse
  and refusal cases. Its daemon is a fake Docker HTTP API on a test Unix socket.
  This is controller/agent protocol evidence, not a VM-variant qualification.
- Deployed guest enrollment still depends on the product starting its dedicated
  TLS 1.3 enrollment listener for `nodeEnrollmentHandler`; that handler is
  test-only in the current tree and the general mux deliberately does not mount
  `NodeControlService`. This lane did not mount broad production RPCs to close
  that gap.
- Live guest-to-controller registration over a real VM network, a real managed
  daemon, a production-mounted control RPC and the Proxmox/libvirt deployment
  matrix remain open. The QEMU arm64 guest path is covered separately from any
  production controller listener.
- The helper now selects `-cpu max` (TCG) or `-cpu host` (KVM) for aarch64,
  because QEMU's `virt` default is a 32-bit ARM CPU. A checked hosted path
  (`scripts/node_vm_arm64_acceptance.py` and
  `.github/workflows/node-vm-arm64-acceptance.yml`) runs two distinct
  instance-ids on an ephemeral `ubuntu-24.04-arm` runner, checks out
  `github.sha`, and uploads only an allowlisted receipt. It was prepared but not
  dispatched for this evidence.
- `scripts/node_vm_boot_qual.py` now resolves OVMF/edk2 for UEFI (failing closed
  instead of falling back to BIOS), copies a fresh private variable store per
  run, and prepares an expanded disk by flattening the base read-only and
  relocating the GPT backup header before boot. Each attempt requires a fresh
  work directory: the helper reserves its outputs before writing a seed, overlay
  or report, refuses reuse without changing prior bytes, rejects unsupported
  target combinations before outputs, and persists launch-failure evidence.
  Unit tests cover those command paths. Section 16 records the separate
  expanded-disk guest evidence.

## Qualification scenarios (remaining runs on owned disposable VMs only)

Each requires a freshly owned, disposable VM that is torn down after the run;
worlds/backups from other installations are never touched.

1. **Clean boot.** Import QCOW2 + NoCloud seed, boot, verify cloud-init applies
   and first boot writes the enrollment bundle to a root-only local path.
   Confirm the bundle carries only a digest of the enrollment secret. Verify
   no secret appears in seed, serial console output or VM metadata. Verify a
   unique `node_id` for two VMs with different instance-ids. *Disposable amd64
   tests cover identity persistence across second boots in both disk profiles.
   Disposable arm64 tests cover a first boot, a clean reboot and a distinct
   second instance-id at the base disk size. The arm64 20 GiB expansion has an
   offline GPT correction and a prepared hosted profile, but no expanded boot
   yet. Concurrent VMs remain open.*
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
