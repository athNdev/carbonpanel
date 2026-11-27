# OSS-377 VM artifact qualification evidence

Sections 1–10 record the original provenance run before a QEMU runner was
available. Sections 12 onward record the later QEMU results. The runtime
publication entry point still emits an offline plan. These boots use disposable
overlays of the pinned upstream image and a generated NoCloud seed. Section 18
adds the arm64 `virt`/UEFI run; it does not replace the amd64 evidence. Section
18.7 records the exact-PR-head hosted path prepared for this evidence. Section
19 records the later offline correction for the expanded arm64 disk and the
prepared expanded acceptance profile; it adds no boot evidence.

The shipped `carbon-node` command is a preflight/doctor CLI, and the Panel's
`NodeControlService` is deliberately unmounted. Managed-node mTLS enrollment,
heartbeat, and daemon health rely on those runtime entry points. Guest proof
accepted through the shared Go boundary establishes protocol compatibility
only.

## Original provenance run

Status: **artifact provenance VERIFIED including real image bytes; real clean
boot BLOCKED by the absence of a system emulator on this host.** Runtime and
enrollment claims below are offline-fixture evidence only. No QCOW2 was built or
published, no host VM was created, and no live host, deployment, registry or
release was written.

Run date: 2026-11-19 (UTC). Branch `codex/node-vm-qemu`, based on
`9e81679b346a3e1acbc50f8283b5b3c365769771` (`origin/main`). The final head SHA
of the commit carrying this report is recorded in the PR and Jira OSS-377.

## Claim boundary (read this first)

| # | Claim | Status | Evidence |
| --- | --- | --- | --- |
| 1 | Immutable dated pins, signed checksums, key fingerprint | **VERIFIED (provenance)** | GPG good signature, exit 0, exact key fingerprint; checksum/signature/key file SHA256 all match the checked-in pin (section 2) |
| 2 | Real image bytes match the signed pin | **VERIFIED (bytes)** | Both amd64/arm64 images downloaded and streamed through SHA256; digests and byte counts match the pin and the published `SHA256SUMS` (section 3) |
| 3 | Downloaded files are valid QCOW2 | **VERIFIED (format)** | `qemu-img 8.2.2 info`: format `qcow2`, virtual size 3.5 GiB, `corrupt: false` (section 4) |
| 4 | Deterministic, secret-free NoCloud seed | **VERIFIED (offline)** | Two artifact-profile builds with a task-owned instance-id are byte-identical; no PEM/secret assignment in rendered seed (section 5) |
| 5 | Unique first-boot identity + digest-only enrollment bundle under shared ADR0019 | **VERIFIED (offline fixtures)** | `node_id` derivation, digest-only bundle, NUL-delimited `ClaimContextRaw` golden bytes, Ed25519-only and canonical-JSON refusal (section 6) |
| 6 | Real clean boot of the QCOW2 with cloud-init | **BLOCKED** | No `qemu-system-*` binary and no `/dev/kvm` on the only available Linux environment; TCG software emulation is not available (section 7) |
| 7 | Live VM first-boot run, mTLS enrollment, effective private-network isolation | **NOT VERIFIED** | Depends on #6; no VM was created. Network-config is DHCP-only with no baked controller address; isolation remains the documented model in `docs/isolation.md` |
| 8 | Artifact byte-reproducibility across qemu-img versions, signed/published artifact | **NOT CLAIMED** | Out of scope; signing is an operator action described in `scripts/node_vm_integrity.py` |

Provenance verified (#1–#3) must not be read as boot verified (#6). This run
does **not** claim a booted VM.

## 1. Environment and scope

- Host OS: Windows; no Windows QEMU install on `PATH` or in `C:\Program Files\qemu` / `C:\qemu`.
- Linux environment: WSL `ubuntu_general` (x86_64), only `qemu-utils`
  (`qemu-img`, `qemu-io`, `qemu-nbd`, `qemu-storage-daemon`) installed;
  `qemu-img 8.2.2 (Debian 1:8.2.2+ds-0ubuntu1.18)`.
- Direct HTTPS GET to the pinned immutable Ubuntu directory and the official
  Ubuntu keyserver succeeded (HTTP 200). No web search and no third-party
  mirror were used. Downloads were confined to `$HOME/oss377-artifacts` inside
  the WSL instance, which is task-owned and outside the repository.
- No Proxmox/live hypervisor, deployment target, release pipeline or package
  registry was contacted or written. (A Proxmox MCP is present in this session
  but is explicitly out of scope for this task and was not used.)

## 2. Checksum-file and signature provenance (re-verified here)

Fresh direct GET from `https://cloud-images.ubuntu.com/releases/noble/release-20261017/`:

| File | Bytes | SHA256 computed here | Matches pin |
| --- | --- | --- | --- |
| `SHA256SUMS` | 7843 | `0449a33b78dddb301a3ce2b80398984bc3e1f7880e27b86a31e58bdcbd3d3816` | yes |
| `SHA256SUMS.gpg` | 833 | `68aae716a548e466e58afddd8b767c54c791f40f1d20d1d12e27c471edb535e5` | yes |
| Signing key (keyserver) | 3325 | `927138de292fac4d861105a052cf56e9c5921987da385c844d88f34d19b39ab0` | yes |

`gpg --verify SHA256SUMS.gpg SHA256SUMS` in an isolated `GNUPGHOME`:
`Good signature from "UEC Image Automatic Signing Key <cdimage@ubuntu.com>"`,
exit 0, primary key fingerprint
`D2EB 4462 6FDD C30B 513D 5BB7 1A5D 6C4C 7DB8 7C81`. GPG warned the key is
not locally certified; the fingerprint matched both the signature issuer and
the key fetched from the official keyserver, so this is fingerprint-based trust,
as recorded in `provenance/RETRIEVAL-EVIDENCE.md`.

The fresh byte chain above is reproducible offline with
`verify_checksum_file()` in `scripts/node_vm_verify_image.py`.

## 3. Real image bytes (new evidence; closes the documented gap)

Both immutable dated images were downloaded in full and hashed from their
actual bytes (not from the published text alone):

| Arch | Bytes | SHA256 of actual bytes | Pin / published hash |
| --- | --- | --- | --- |
| amd64 | 621284352 | `04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94` | equal |
| arm64 | 616522752 | `50e24e1fb79e89116e939d90aa7738f7af48c442fa1bc2510131e0b39c50348e` | equal |

Byte counts equal the pinned `image_content_length` values. The new
`scripts/node_vm_verify_image.py` verified both files against the pin **and**
against the freshly downloaded published `SHA256SUMS`, returning
`"bytes_verified": true` for each. `RETRIEVAL-EVIDENCE.md` previously stated
"the image bytes were not downloaded"; that statement is now superseded by
this run for these two pinned images only.

## 4. Real QCOW2 validation

`qemu-img info --output=json` on both downloaded files returned:

- `format: qcow2`, `compat: 1.1`, `cluster-size: 65536`,
  `virtual-size: 3758096384` (3.5 GiB), `corrupt: false`.

This confirms the bytes are parseable QCOW2 images of the expected size. It is
a format/integrity check, **not** a boot.

## 5. Deterministic, secret-free NoCloud seed (task-owned VM)

Artifact-profile seed generated twice for task-owned instance
`oss377-qemu-qual-0001`, label `oss377vm`:

- Both runs: `manifest_sha256 = 088da6a0aabb0e9e5ef0980dafcb9786bda26af2eb1e23971ff8d9cf864c8ecd`.
- `nocloud/user-data` SHA256 identical across runs
  (`765B21CB7ABD1AEFE4E0E1DFE4791742427CB22A3211447847845E838B75F1BC`);
  `seed-manifest.json` identical across runs
  (`22CD9E5790A1D240F849640BB3F50D797E6430275CBE1E92262212D40997CF84`).
- Secret scan of rendered seed: no `BEGIN PRIVATE KEY`, no `BEGIN CERTIFICATE`,
  no `enrollment_secret:`, `password:` or `token:` assignment. The first-boot
  script is embedded as base64; the policy is public metadata only.
- Derived first-boot identity for this instance-id:
  `node-a29539e5302ed2b2b4f28b8cb651a57e`.
- `network-config.yml.tpl` is DHCP-only (`dhcp4: true`, `dhcp6: false`) with no
  baked controller address or credential; static addressing would be supplied
  per VM as public metadata. No live VM network was created, so effective
  isolation is not asserted here (see `docs/isolation.md`).

The seed lives in untracked `tmp/oss377-seed-a` and `tmp/oss377-seed-b` in the
worktree and is not part of the commit.

## 6. First-boot identity and digest-only enrollment bundle (shared ADR0019)

Verified by the offline guest-module fixtures, not by a live VM:

- `derive_node_id(instance_id)` yields distinct, deterministic `node_id`s for
  distinct instance-ids; empty instance-id is refused.
- `generate_secret` enforces >= 256 bits; `compute_digest = SHA-256(salt || S)`.
- The exported bundle is digest-only: salt, digest, `CSR_PRE`, SPKI, fingerprint,
  scoped bootstrap fields. It contains no secret, private key or token; a
  leaking field or value is refused by `assert_bundle_has_no_secret`.
- `write_identity` separates the `0600` bundle from the `0600` secret file in a
  `0700` directory; stdout of `first-boot` carries only public metadata.
- Saved-identity reuse re-validates the complete record: strict bounded base64,
  node/key/fingerprint/digest matches, policy-equal `contract_ref`,
  `object_id_binding` and bootstrap scopes, and a self-signed Ed25519 PKCS#10
  `CSR_PRE` whose SPKI equals the approved key plus a detached proof made by
  that key. An internally inconsistent CSR is refused even when the detached
  proof is valid. A pre-hardening record (raw token, missing `node.key` or
  signed-JSON pseudo-CSR) is preserved and refused read-only; no automatic
  migration is performed and operator re-provisioning is required.
- `SIG_CLAIM` uses the shared ADR0019/OSS-385 `ClaimContextRaw` NUL-delimited
  byte encoding over the **server-stored** nonce, enrollment `expires_at`,
  canonical proto capability names and channel context. Byte-for-byte
  conformance is asserted against the golden from `internal/noderuntime/enroll`
  at PR532 head `fc998875` (`qualification/fixtures/claim-context-shared.json`).
  Ed25519-only; canonical-JSON contexts and unknown/out-of-order capability
  names are refused. This lane does not fork or mount a node-control RPC.

These prove the offline contract shape and refusals only. A real first-boot
execution on a booted VM, and mTLS enrollment against a live controller, are
**not** verified in this run.

## 7. Real clean boot — exact external blocker

A real clean boot requires a system emulator. On every environment available
to this task:

- Windows: no `qemu-system-x86_64`, `qemu-system-aarch64` or `qemu-img` on
  `PATH`; no QEMU installation in the usual program directories.
- WSL `ubuntu_general`: `dpkg -l | grep qemu` shows only `qemu-utils` and
  `qemu-block-extra`; `/usr/bin/qemu-*` contains only `qemu-img`, `qemu-io`,
  `qemu-nbd`, `qemu-storage-daemon`. No `qemu-system-*` anywhere under
  `/usr`, `/opt` or `/snap`.
- `/dev/kvm` does not exist, so hardware acceleration is unavailable.

TCG (software) emulation would still be possible *if* a `qemu-system` binary
were present, because the host CPU exposes `vmx`. It is not present, and
installing system packages would be a host-level change outside this task's
authority ("no live host ... write"). Therefore **no boot was attempted and no
boot evidence is claimed**. The exact blocker to lift is: provide an isolated
`qemu-system-x86_64` (and `qemu-system-aarch64` for arm64) binary usable without
KVM, or an owned disposable VM facility, then boot the verified amd64 QCOW2 with
the deterministic NoCloud seed and capture serial-console evidence.

## 8. Offline fixtures

`python -m unittest discover -s scripts -p 'test_node_vm_*.py' -v`:

- The **existing 30 fixtures** pass (build 7, enroll 11, integrity 6, upgrade 6).
- With the new byte-verification fixtures the suite is **37 tests, all passing**.

Narrow checks also pass: `python -m py_compile` on all `node_vm_*.py`, the
guest `enroll.py` and the new verifier; `node_vm_build.py --profile artifact
--check` (`provenance_status: verified`); `node_vm_integrity.py --profile
fixture --verify`; `node_vm_upgrade.py --from-revision 0.1.0 --to-revision
0.2.0 --batch-id oss377-b1`.

## 9. Tooling added

`scripts/node_vm_verify_image.py` closes the gap between "published, signed
hash" and "actual image bytes": it streams a downloaded image, compares the
computed SHA256 and byte count to the checked-in pin and to the published
`SHA256SUMS`, verifies the checksum/signature file bytes against the pin, and
fails closed on fixture profiles, placeholder digests or any mismatch. It is
read-only and offline. Tests: `scripts/test_node_vm_verify_image.py`.

## 10. Cleanup and non-publication

- No host VM, container, daemon, socket or network was created. No enrollment
  was performed and no credential was issued.
- The downloaded images and GPG material are in `$HOME/oss377-artifacts` in the
  WSL instance and the generated seeds in untracked `tmp/oss377-*`. They are
  task-owned and are removed after evidence capture; they are not committed.
- No checksum/SBOM/signature was published. No registry, release, tag or
  deployment was written. This artifact is **not qualified for publication**
  until the clean-boot gate in section 7 is satisfied with real evidence.

## 11. Reproduce

Inside an owned Linux environment with direct HTTPS access:

```sh
# provenance + real bytes (writes only under $HOME/oss377-artifacts)
BASE=https://cloud-images.ubuntu.com/releases/noble/release-20261017
curl -fSL "$BASE/SHA256SUMS" -o SHA256SUMS
curl -fSL "$BASE/SHA256SUMS.gpg" -o SHA256SUMS.gpg
curl -fSL "$BASE/ubuntu-24.04-server-cloudimg-amd64.img" -o ubuntu-amd64.img
python3 scripts/node_vm_verify_image.py --image ubuntu-amd64.img \
  --ref ubuntu-24.04-amd64 --sha256sums SHA256SUMS
qemu-img info ubuntu-amd64.img

# offline fixtures (no network, synthetic keys)
python -m unittest discover -s scripts -p 'test_node_vm_*.py' -v
```

## 12. 2026-11-19 follow-up: disposable container TCG probe and boot attempt (this session)

Environment probe (Windows host, WSL `ubuntu_general`):
- `qemu-system-x86_64`/`qemu-system-aarch64`: not installed on host or in WSL.
- `/dev/kvm`: absent in WSL.
- Docker: available in WSL (`docker-ok`, Docker 29.3.0) and able to pull/run
  disposable containers (`alpine:3.20` smoke run succeeded). This provides a
  disposable, isolated execution environment on this machine, but not a host
  `qemu-system` binary or KVM acceleration.

Tooling improvement (VM packaging paths, build-only/offline): added
`scripts/node_vm_boot_qual.py` (+ `scripts/test_node_vm_boot_qual.py`) to
prepare deterministic seed/NoCloud ISO inputs, verify qemu availability, run a
disposable QEMU boot attempt with serial-console capture, scan for cloud-init
and first-boot enrollment markers, and optionally perform read-only offline disk
inspection for bundle/secret persistence when guestfs tools are available. The
driver reports gates conservatively and does not claim live enrollment, daemon
health or effective network isolation from serial markers alone.

Preparation (task-owned, WSL): deterministic NoCloud seed/ISO generated for
`instance-id oss377-boot-qual-0001`, `node-label oss377boot` (CIDATA ISO via
`genisoimage`). Verified amd64 `release-20261017` image re-downloaded in WSL
temp and hash-checked: `04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94`,
621284352 B (matches pinned provenance; see section 3).

Disposable QEMU TCG boot attempt (boot-attempt-3, 2026-11-19, container
`oss377-qemu:local` / Debian bookworm `qemu-system-x86_64` 7.2, `--accel tcg`,
`-m 2048 -smp 2`, user-mode networking, `-no-reboot` to capture first
boot-to-reboot boundary, serial console to file, offline inspection skipped):

| Gate | Status | Evidence |
| --- | --- | --- |
| qemu available / seed / seed ISO / boot disk overlay | PASS | qemu-system-x86_64 found; deterministic nocloud seed and prebuilt CIDATA ISO used; qcow2 backing overlay created |
| boot attempt | **COMPLETED** | qemu exited `returncode 0`, `timed_out false`, elapsed ~504.1s (2400s timeout) |
| serial console | SCANNED | `serial-console.log` captured (88272 bytes) |
| cloud-init finished | **PASS (marker)** | `cloud_init_finished: true` in serial scan |
| first-boot identity marker (enroll stdout JSON) | **INCONCLUSIVE** | `enroll_stdout_json: false`; cloud-init finished marker seen but enroll stdout JSON not found in serial log (runcmd stdout is typically recorded in guest `cloud-init-output.log`, not serial console). On-disk bundle/secret creation not proven by this scan. |
| reboot/poweroff boundary | **OBSERVED** | `reboot_or_poweroff_signal: true`; clean qemu exit with `-no-reboot` at boundary |
| serial secret scan | NO_HIGH_SIGNAL_MARKERS | no PEM/assignment markers found in captured serial log; absence of all secret material not proven |
| network config | CONFIGURED (not proven) | seed DHCP-only; qemu user-mode netdev configured; effective connectivity/health not proven |
| offline disk inspection / bundle persistence | NOT_RUN | inspection skipped (no guestfs tools in runtime container); bundle/secret on-disk presence and reboot persistence not verified |
| service health / reboot persistence (second boot) | NOT_RUN | no in-guest health probe; second-boot persistence not automatically verified |

**Interpretation:** this provides real disposable-runner evidence of **clean VM
boot and cloud-init completion** for the verified amd64 image with the
deterministic NoCloud seed, and a clean reboot/poweroff boundary under TCG
(~8.4 min). It does **not** by itself prove first-boot enrollment bundle/secret
creation on disk, effective user-setup completion beyond cloud-init markers,
network connectivity/daemon health, or post-reboot persistence (second boot not
performed, guest disk not inspected). Prior claims remain unchanged: whole-image
byte provenance is VERIFIED (section 3). Full OSS-377 boot acceptance still
requires evidence for user setup/bundle persistence and reboot persistence (and
network/health where possible) on a capable disposable runner; this run reduces
but does not close that gap. No live mTLS enrollment, registry/release,
deployment or live host write was performed. Task-owned downloaded image and run
artifacts in WSL temp remain local evidence unless explicitly retained/cleaned
per task policy.

## 13. 2026-11-20 follow-up: scanner fix, second boot and offline persistence inspection

No downloads, installs, live guests, enrollment RPC, release or registry writes
were performed. All inputs were the task-owned artifacts already present in the
owned WSL instance: Docker image `oss377-qemu:local` (`qemu-system-x86_64`
7.2.22), the pinned amd64 image (621284352 B, SHA256
`04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94`), the
deterministic NoCloud seed/CIDATA ISO and the boot-attempt-3 qcow2 overlay.

### Genuine harness defect fixed (grounded in captured output)

The attempt-3 serial log contains this real first-boot success line at line 798:

```
[  377.903394] cloud-init[964]: {"approved_key_fingerprint": "244f267e2a63caed203d3218001f38cef356622a47b10f78fb8bdce9b90ca167", "bundle_path": "/var/lib/carbon-node/enrollment/enrollment-bundle.json", "contract_ref": "ADR0019 / proto/carbonpanel/v1/node_control.proto", "node_id": "node-94ed0fec32c64d9fb5236be6d57ec629"}
```

`ENROLL_STDOUT_RE` required compact JSON separators, so a successful boot whose
stdout used `json.dumps` defaults (spaces after `:` and `,`) was reported
`enroll_stdout_json: false` and `first_boot_identity_marker: INCONCLUSIVE` in
section 12. The regex now tolerates that whitespace, and a regression test uses
the exact captured line. Re-scanning the **unchanged** 88,272-byte attempt-3 log
now reports `cloud_init_finished: true`, `enroll_stdout_json: true`,
`node_id: node-94ed0fec32c64d9fb5236be6d57ec629`,
`fingerprint: 244f267e2a63caed203d3218001f38cef356622a47b10f78fb8bdce9b90ca167`,
`reboot_or_poweroff_signal: true`, and no high-signal secret markers.
All 43 `test_node_vm_*` fixtures pass.

### Second boot (boot-attempt-4)

Started 2026-11-19T23:33:07Z from the **same** boot-attempt-3 overlay and seed
ISO under TCG, in a disposable task-owned container. It booted to a login prompt
and emitted cloud-init `init-local`, `init`, `modules:config`, `modules:final`
and `finished` (84.32 s), reaching `network-online.target`; no enrollment JSON
was re-emitted and no second clean reboot boundary occurred before the container
was stopped with a bounded 30 s SIGTERM at 2026-11-20T00:02:10Z. The preserved
serial log is 57,138 B, SHA256
`4b0beafd53b52a558d1e8f44c0689897a04c0842d684edbb98d30bdd7230ec07`.

### Read-only offline disk inspection

After stopping the container, the booted overlay was converted to raw and its
ext4 root partition (GPT start LBA 2099200) read with `debugfs` only. Observed:
`/var/lib/carbon-node/enrollment/` mode `0700`, containing
`enrollment-bundle.json` (1341 B, mode `0600`) and `enrollment-secret.bin`
(32 B, mode `0600`). The bundle's `node_id` and fingerprint match the serial
markers, `bootstrap_scopes` are the two expected enrollment scopes, and the
exported shape is digest-only (salt, secret digest, CSR-pre, SPKI, fingerprint)
with no secret/private-key/token field. `cloud-init-output.log` contains the
first-boot enrollment JSON and both boots' cloud-init stages, confirming the
first-boot identity step was not repeated after reboot.

### What this does and does not establish

| Gate | Value | Basis |
| --- | --- | --- |
| Cloud-init completion | PASS (first and second boot) | serial + `cloud-init-output.log` markers |
| First-boot identity marker | PASS (after scanner fix) | real serial JSON + on-disk bundle identity |
| Bundle/secret on disk, root-only modes | PASS | read-only ext4 `debugfs` inspection |
| State survival across the first reboot | PASS (bounded) | second boot from post-reboot disk retained bundle/secret and did not re-enroll; not a second clean reboot cycle |
| Effective network connectivity / daemon health | NOT TESTED | no in-guest health probe; `ci-info` at init showed `ens3` without an address; `network-online.target` reached but no connectivity assertion |
| Live mTLS enrollment | NOT TESTED | no controller endpoint or enrollment traffic in this fixture |

Whole-image byte provenance remains VERIFIED (section 3). This update does not
claim live enrollment, network health, host provisioning, image publication or
any release/registry mutation.

## 14. 2026-11-20 disposable UEFI rerun

Rehashed the 621,284,352-byte amd64 base against the pinned and published
digests. The digest was
`04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94`.
The matching release-20261017 SHA256SUMS and signature file correspond
to the signature verification in section 2. The base was mounted read-only
in a disposable container. The only writes went to a fresh QCOW2 overlay
and a fresh OVMF variable store.

| Input | Exact version or SHA256 |
| --- | --- |
| QEMU | 7.2.22 under TCG, amd64 |
| Runner container image ID | `4d9be18bc8fc6a7a4708e9ab62691e601b7778cde624f52357e7b0050b4f8c50` |
| OVMF 4M code | Ubuntu package `2024.02-2ubuntu0.10`, `ad23261d43116abc3747d59a7837fbf578c9586a8993f4eeba064cd07eccb083` |
| Deterministic artifact-profile seed manifest | `f6be9a6c887350ea9d16f476c1fe8906355d8c50621399d7bdf0712db1b26d1f` |
| Guest enrollment script in seed | `bab23eec024dc02f966a34fb576fd3e2acaa608734695522fc2543c61846c4c9` |
| DHCP network config in seed | `7cf6fffd684b1bbd3b21a8d22fb704ad764f616d2c7ad73ccd8cbf17cbc25a6b` |
| CIDATA ISO used in rerun | `c0b47b2d7171ae5b4e7b5d54fd63ea7806165c6b9f6d87799f6235d62a6aae97` |

Generated the seed with `node_vm_build.py --profile artifact --check` and
instance ID `oss377-efi-qual-20261120-a`. Commit
`d451c8de386b69a416934800de57565fbe2a9944` contains the enrollment script
and network template with the digests above. The boot fixture appended only an
ephemeral SSH **public** key to inspect the guest and exercise enrollment.
QEMU ran with two vCPUs, 2048 MiB RAM, virtio disk and Ethernet,
readonly OVMF code, and `-no-reboot`. Docker networking was disabled.
QEMU's restricted user-mode network exposed SSH only on loopback inside
the container network namespace.

The initial q35 run with an expanded 20 GiB overlay entered emergency mode.
Linux reported an alternate GPT header at the original disk end, and
systemd timed out waiting for `BOOT` and `UEFI` device labels. This run's
serial log SHA256 was
`6e4d11cb9b420a4f0a8e85c93212297107072cc276816c8770926266db5c023d`.
The retry used OVMF with the i440fx machine and an overlay at the base image's
original 3.5 GiB virtual size. Section 16 records the later expanded-profile
run.

The retry mounted `/boot` and `/boot/efi`, obtained an IPv4 DHCP lease on
`ens3`, and loaded the NoCloud seed from `/dev/vdb`. At 07:07:38 UTC
cloud-init reported completion. The first-boot command emitted node ID
`node-a55c4391379f14e5630dbbec8b3efe68` and approved key fingerprint
`9291419073ec0c86b3ca058f058c3d2d6b6c8ea4ef7d66f075667fcd9e0ac92f`.
The guest requested a reboot and QEMU exited successfully at 07:09:26 UTC.
The first-boot serial log contains 98,295 bytes, SHA256
`3faebc211bee64253a8abe2520e496c4250462e4daabd4c36905944a4ef42701`.

The second boot reused the same overlay and ISO. At 07:18:52 UTC cloud-init
completed without errors. An authenticated SSH probe confirmed the UEFI
runtime, `systemd-networkd` and SSH active, `ens3` with an IPv4 DHCP lease,
and cgroup v2. The enrollment directory was `0700 root:root` and contained
`node.key`, `enrollment-bundle.json` and `enrollment-secret.bin`, each
`0600 root:root`. Neither serial capture nor the CIDATA ISO contained the
actual enrollment token or a private-key PEM marker.
The installed guest enrollment script matched the seed SHA256 above. The
cloud-init boot did not emit a second identity marker. An explicit rerun
of the first-boot command loaded and validated the persisted key and token
digest and returned the exact original node ID and public key fingerprint.
The guest had no Carbon node systemd unit.
The guest accepted a power-off request. The disposable container had exited
when checked after its timeout boundary. A clean second shutdown receipt
was unavailable. The second-boot
serial log contains 72,562 bytes, SHA256
`2966f59e1851dbd4c8edaf0ffa6061df595873db79d95201e67634930adc4f41`.

A separate WSL fixture ran the same corrected first-boot script and submitted
its registration and proof to the checkout's shared Go enrollment authority.
The authority accepted the DER CSR and fresh signature, issued a certificate
for the approved public key, and refused a changed signature, replay, and
wrong node scope. Its stored snapshot did not contain the plaintext token.
The attempt to feed proof from the rebooted VM to that authority timed out
while retrieving the registration bundle over SSH. Live guest enrollment,
controller mTLS, heartbeat transmission and node daemon health remain open.

## 15. 2026-11-26 harness-only follow-up (no guest run)

No guest, host, registry or release was touched. The qualification helper now
models the declared UEFI target instead of silently booting SeaBIOS:

- `find_uefi_firmware()` resolves OVMF/edk2 code and variable templates from
  standard paths or `CARBON_OVMF_CODE`/`CARBON_OVMF_VARS`, and reports
  `BLOCKED` (never a BIOS fallback) when firmware is absent.
- `prepare_uefi_vars()` copies the variable template to a fresh private file in
  the owned work directory; the system firmware file is never written.
- `qemu_command()` accepts a q35/virt machine and adds readonly code plus
  writable vars pflash drives. `prepare_overlay(..., size=...)` requests an
  expanded qcow2 overlay and refuses to fall back to an unresized copy.
- `--firmware {uefi,bios}`, `--machine` and `--disk-size` expose these paths.
  UEFI is the CLI default because it matches `config/build-config.json`.
- Each attempt requires a fresh `--work-dir`. The helper atomically reserves
  the attempt output set (`boot-qual-attempt.json`) before generating a seed,
  overlay or report, and refuses to reuse a prior directory without changing
  its disk, seed or report bytes.
- Unsupported architecture/firmware/machine combinations are rejected before
  any output is created and exit non-zero. BIOS on aarch64 is the explicit
  example: it is no longer reported as a firmware PASS.
- Blocked/inconclusive attempts now persist `boot-qual-report.json` before an
  early return, so firmware-missing and prior-disk refusals keep their evidence.
  A QEMU launch failure also persists that report with a `FAIL` boot-attempt
  gate and returns non-zero instead of escaping with no JSON.

All 72 node-VM fixtures at this checkpoint passed on Windows with two
Linux-only OpenSSL guest fixtures skipped. Section 16 records the later
guest-level evidence.

## 16. Expanded 20 GiB q35 boot and identity persistence (2026-11-26)

The disposable amd64 run exercised commit
`f7524831d56114caf28a852bdee5ae3cab97c659` with QEMU 7.2.22, q35, OVMF UEFI
and software acceleration. It used the pinned Ubuntu base read-only, a
distinct sparse 20 GiB overlay, a NoCloud seed and a private UEFI variable
store. The container runner used an isolated network namespace with networking
disabled.

- The first guest completed cloud-init and reached its reboot request.
  `growpart` expanded the root partition to 20,400,029,184 bytes, and
  `resize2fs` expanded ext4 to 4,980,475 blocks of 4,096 bytes. The initial
  GPT size warning cleared after the expansion. The first serial log SHA-256
  was `fda744c3d71b4dcc2475bba8ee08ea96e9a0d9922509211b96f663431f3ee721`.
- The second guest booted from verified byte-identical copies of the first
  run's overlay, seed and UEFI variables. Cloud-init completed, a private QMP
  socket sent the ACPI power button, and QEMU exited normally after guest
  shutdown. The second serial log SHA-256 was
  `618da070f22b22fc408c600cebda7507ae70cf9f52ef2d2ff59448317aaabc54`.
- Offline inspection found byte-identical enrollment bundles and the original
  node ID and public-key fingerprint. The enrollment directory remained
  root-owned with mode `0700`. The bundle, key and secret files retained
  mode `0600`. The second serial log contained no new enrollment identity
  marker. Read-only ext4 and QCOW2 integrity checks passed after shutdown.

This qualifies these two boots on the disposable amd64 QEMU/TCG fixture.
Managed controller enrollment, mTLS, heartbeat, node daemon health, arm64
and the Proxmox/libvirt host matrix still need separate evidence.

## 17. Controller/agent fixture for managed enrollment and health (2026-11-26)

This Linux fixture exercises the shared ADR 0019 enrollment authority and the
managed node agent over loopback TLS. It mounts the shared enrollment service
and a fixture-owned control service in its own listener. The preceding QEMU
boot results cover the VM image separately.

Fixture: `internal/rpc/oss377_managed_fixture_test.go`
(`//go:build linux`, `TestOSS377ManagedControllerAgentFixture`).

What is real in the fixture:

- `enroll.NewProductionAuthority` with a revision-checked in-memory store and an
  owned CA. The fixture serves `begin` and `claim` over its loopback TLS listener.
- The node's bundle and live claim from `internal/nodeinstall`; TLS 1.3 exporter
  channel binding; Ed25519 proof over the shared `ClaimContextRaw`.
- The real `agent.Run` performs challenge, claim, public credential storage,
  scoped mTLS, preflight and the pinned daemon probe through a Unix socket. The
  fixture waits for a second health report to verify the periodic heartbeat.
- `NodeCredentialForCertificateContext` plus `Boundary.AuthorizeNodeOperation`
  for `node:report` and `node:heartbeat`; anonymous, unenrolled, cross-node and
  revoked certificates are refused.
- Controller-owned endpoint generation is supplied by the fixture controller's
  node record; the enrollment authority itself does not own that field.

Exact isolated run used for this evidence (Windows host, static Linux test
binary, disposable container with networking disabled):

```sh
# compile from the checkout (C scratch output, not committed)
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/rpc -o oss377-fixture.test

# run with a local alpine:3.20 image and /tmp as the only writable path
docker run --pull never --rm --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges --user 65534:65534 \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,mode=1777,size=64m \
  -v "$PWD/oss377-fixture.test:/fixture.test:ro" \
  alpine:3.20 /fixture.test \
  -test.run='^TestOSS377ManagedControllerAgentFixture$' -test.v -test.count=3 -test.timeout=60s
```

The fixture passed three consecutive runs. On a disposable Linux runner it can
also run directly:

```sh
go test ./internal/rpc -run '^TestOSS377ManagedControllerAgentFixture$' -count=1 -v
```

Coverage limits, stated plainly:

| Aspect | Fixture result | Not proven |
| --- | --- | --- |
| Challenge + TLS-bound claim | PASS over loopback TLS 1.3 | The installed controller's enrollment listener and a claim from a booted VM guest |
| Scoped mTLS | PASS: issued cert maps to one active grant; anonymous, unenrolled, cross-node and revoked are refused | A production-mounted NodeControlService endpoint |
| Heartbeat / preflight | PASS with report-hash and envelope-generation checks and a distinct periodic health report | Controller persistence of heartbeats into the panel database |
| Daemon health | PASS against a fake Docker HTTP API on a test Unix socket, pinned by engine ID and data root | A real dockerd, rootless engine, cgroup limits or real host data |
| Restart | PASS with fresh preflight and health reports for the original node and a single claim across both runs | Full VM reboot (covered separately in section 16) |
| Architecture / host matrix | amd64 Linux container only | arm64, Proxmox VE, generic KVM/libvirt and bare-metal hosts |

End-to-end VM acceptance still requires guest enrollment through the installed
controller, a production-mounted control service, health checks against a real
daemon and isolation checks.

## 18. arm64 QEMU virt/UEFI boot, clean reboot and second identity (2026-11-26)

This run adds disposable arm64 guest evidence without touching a host service,
Proxmox, libvirt, Docker, a registry or a release. The host was a Windows
workstation with WSL2 Ubuntu 24.04 x86_64; it had no system
`qemu-system-aarch64`, no AAVMF firmware and no `/dev/kvm`. The arm64 emulator
and firmware were unpacked from signed Ubuntu 24.04 packages into a task-owned
directory and run from there. Nothing was installed system-wide:
`dpkg -l qemu-system-arm qemu-system-common` still reported `un`, and
`/usr/bin/qemu-system-aarch64` and `/usr/share/AAVMF` did not exist.

The task-owned inputs and toolchain were:

| Input | Exact version or SHA256 |
| --- | --- |
| Base image | pinned `ubuntu-24.04-arm64`, 616,522,752 bytes, `50e24e1fb79e89116e939d90aa7738f7af48c442fa1bc2510131e0b39c50348e` |
| Published `SHA256SUMS` | `0449a33b78dddb301a3ce2b80398984bc3e1f7880e27b86a31e58bdcbd3d3816`; actual image digest equal to the pin and published row |
| QEMU | Ubuntu `qemu-system-arm 1:8.2.2+ds-0ubuntu1.18`; extracted binary reports 8.2.2, TCG |
| QEMU packages | `qemu-system-arm` deb `06184a21b9c8dc573ab6d255e9826cc4732df90e56bd65c52e92ad640af4d60a`; `qemu-system-common` `0a0b744f31e87d72dd0465436b8dfbb1ef9f574ecd0d5a2e9dd8c5cb7d0fef1f`; `qemu-system-data` `a14b88d864859bd61c8a3274971da4ecb7da6cec15be6c265d0d411f783d5f2e`; `ipxe-qemu` `1f1612934b6ddaf4dd633760dfe5ef2a79ea2507341fd5efe394ff589a044707`; `libfdt1` `274d20dfab9d6b216b5de85446a93f6ce5b2cd82c847b8dfdc508577f76eb96a` |
| UEFI firmware | `qemu-efi-aarch64 2024.02-2ubuntu0.10`, deb `fad8e80e929cd00dae8c5c8e5dd3ec85f37d9a832eea776957e5e2cf86ef2ab5`; AAVMF code `545e2eda871a00dba51c0df48a34829fe3b0de6ec10ea760c75f1fc1c6a29dc5`; variable template `b3b855c5a80310168051164986855692d1bdb06e67619856177965cd87c6774f` |
| Seed ISO, instance A | `b5ba06c72c9eebfb07373a89577ceac6b06769cf4fa4faa037dfdc2df52232e0` |
| Seed ISO, instance B | `9a6b4a0ae7a7b864337b8281c8c8a7574dd8945a15f29c2379a914632091dcd9` |
| Overlay format | QCOW2, virtual size 3,758,096,384 bytes (3.5 GiB), base backing file read-only |

The first-boot command was the same for both instance IDs except for the work
directory, seed and overlay:

```sh
qemu-system-aarch64 -L "$TOOLS/usr/share/qemu" -m 2048 -smp 2 -cpu max \
  -machine virt -accel tcg \
  -drive "file=$WORK/boot-disk.qcow2,if=virtio,format=qcow2" \
  -drive "file=$WORK/seed.iso,if=virtio,format=raw,readonly=on,media=cdrom" \
  -drive "if=pflash,format=raw,readonly=on,file=$TOOLS/usr/share/AAVMF/AAVMF_CODE.fd" \
  -drive "if=pflash,format=raw,file=$WORK/uefi-vars.fd" \
  -device virtio-net-pci,netdev=net0 \
  -netdev user,id=net0,restrict=on \
  -nographic -serial "file:$WORK/serial-first.log" -monitor none -no-reboot
```

`$TOOLS` is the unpacked package tree and `$WORK` is the per-instance
task-owned work directory. The user-mode netdev used `restrict=on` with no
host-forward; the guest booted from the NoCloud CD-ROM at `/dev/vdb`.

### 18.1 Architecture and firmware checks

| Check | Result | Evidence |
| --- | --- | --- |
| Pinned arm64 bytes | PASS | `node_vm_verify_image.py --ref ubuntu-24.04-arm64` returned `bytes_verified: true`; actual, pinned and published SHA256 all equal |
| arm64 guest on the declared machine | PASS | QEMU `virt` with `-cpu max`; AAVMF code/vars pflash; serial reported the `armv8_pmuv3_0` PMU and ACPI `virt` devices |
| UEFI rather than BIOS | PASS | AAVMF pflash pair only; serial reported `secureboot: Secure Boot disabled` (the non-secure-boot AAVMF code variant), no SeaBIOS |
| Software acceleration | PASS | `-accel tcg`; `/dev/kvm` absent |
| Clean first boot to the declared reboot | PASS | guest logged `reboot: Restarting system`; QEMU exited `0` under `-no-reboot` |
| First boot serial | PASS, 86,274 bytes | SHA256 `f34f2b07b1faa1c0d19641233334b5e4abf2f3e2ae9885fcc99a375c9f8e2f5f` |

The expanded 20 GiB arm64 profile was attempted first and is **not qualified**.
The kernel reported the original GPT alternate header at the base image end
(`7340031 != 41943039`), then `/dev/disk/by-label/BOOT` and
`/dev/disk/by-label/UEFI` timed out and the guest entered emergency mode; no
enrollment completed. That attempt's serial SHA256 was
`b8f064df9164436cc3700713a11d15ba6b65e5a14c9e59e40da79770d8236681`. The
successful arm64 runs therefore use the base 3.5 GiB virtual size. The expanded
20 GiB path remains qualified on amd64 only (section 16), not on arm64. Section
19 records the later harness correction; this failure result stands as recorded.

### 18.2 First-boot identity and root-only enrollment files

Both instances completed cloud-init with the NoCloud datasource
(`DataSourceNoCloud [seed=/dev/vdb]`) and emitted the public first-boot JSON to
the serial console.

| Instance | Instance ID | Public node ID | Approved key fingerprint |
| --- | --- | --- | --- |
| A | `oss377-arm64-qual-20261126-a` | `node-78805a9750fa9efee1fb830643f1b893` | `b34e78ec614d5dc89c120e0fabe0ad7b03f103b460b9fc6ebac6e576d4d24748` |
| B | `oss377-arm64-qual-20261126-b` | `node-8bd7872baf90ee073c7238d87d4f8730` | `195f425da602f7e06d5b36da1f674af6a96104746f5afeba56becee33a3715a9` |

Instance A's node ID equals the deterministic
`node-` + first 32 hex characters of SHA-256 over its instance ID. Instance B's
node ID is different and equals the same derivation for its own instance ID, so
two distinct instance IDs produced two distinct public node IDs.

Read-only ext4 inspection through a read-only loop device and `debugfs` (no
mount, no write) found for both instances:

- `/var/lib/carbon-node/enrollment` inode mode `0700`, `root:root`.
- `node.key`, `enrollment-bundle.json`, `enrollment-secret.bin` each mode
  `0600`, `root:root`.
- Instance A bundle: 1,112 bytes, SHA256
  `906ada1a954e1ef60ee5415495478549bd38afad6a37c7b8e8ce96960d690322`.
  Instance B bundle: 1,112 bytes, SHA256
  `796aef4d4d3a234380e8d92ddb47668f2a585884aa4c4f23a7ed54be739a7c28`.
- Both bundles contain only public/derived enrollment material:
  `approved_key_fingerprint`, `approved_public_key_spki`, `bootstrap_scopes`,
  `contract_ref`, `csr_pre`, `csr_pre_signature`, `enrollment_salt`,
  `enrollment_secret_digest`, `node_id`, `node_label`, `object_id_binding`,
  contract flags. Neither bundle contained a secret, token, password or private
  key field. The scopes were `BeginEnrollmentChallenge` and `ClaimEnrollment`.
- The secret file was 43 bytes and the node key 119 bytes; neither was printed,
  copied into the repository or uploaded. Only inode metadata (mode, owner,
  size) was recorded for them.

The serial scans found no `BEGIN PRIVATE KEY`/`BEGIN CERTIFICATE`/token
assignment markers. This is a marker scan, not a proof that no secret exists
anywhere in the guest.

### 18.3 Clean reboot and identity persistence

The first-boot QEMU invocation above ran with `-no-reboot`; the guest itself
logged `reboot: Restarting system`, which QEMU treated as the end of that
invocation. The second boot was a **separate QEMU invocation**, not an
in-process restart, that reused the verified byte-identical instance-A overlay,
UEFI variable store and seed ISO. It omitted `-no-reboot` and added a private
QMP socket:

```sh
qemu-system-aarch64 ... -qmp "unix:$WORK/qmp.sock,server=on,wait=off"
```

After cloud-init reported `finished`, the host sent the QEMU monitor
`system_powerdown` command. The guest logged `reboot: Power down`, QEMU exited
`0`, and the serial log was 86,104 bytes with SHA256
`683560ce1973cb1e42b34bc30404e083c11e41037ff49da40e03f027e853d7b4`.

| Check | Result | Evidence |
| --- | --- | --- |
| Second boot cloud-init | PASS | `Cloud-init v. 26.1 ... finished ... Datasource DataSourceNoCloud` |
| Clean guest shutdown | PASS | QMP ACPI powerdown, guest `reboot: Power down`, QEMU exit `0` |
| No repeated enrollment/identity marker | PASS | second serial has no enroll stdout JSON and no node ID; the first-boot identity step did not rerun |
| Enrollment bundle persistence | PASS | bundle SHA256 remained `906ada1a...`; inode mode `0600`, directory `0700`, owner `root:root`, mtime unchanged |
| Disk integrity after shutdown | PASS | `qemu-img check` reported `No errors were found on the image` after the second boot |

This is a clean guest-initiated restart followed by a separate emulator launch
on the same disposable guest disk, not an in-process restart, not a second
fresh VM and not a host reboot. The base-sized arm64 result in this section was
independently confirmed against the recorded serial logs, bundle digests and
inode metadata; that review did not rerun QEMU.

### 18.4 Second identity

Instance B was a fresh overlay of the same verified base, with a fresh
task-owned UEFI variable store and a seed generated for
`oss377-arm64-qual-20261126-b`. It completed cloud-init and rebooted cleanly
(serial 92,886 bytes, SHA256
`853883c5607fe928a71a2d7f7b0b4b442985cf01e21dc8fa047d30ee484065a6`), wrote the
same root-only file layout, and emitted the different public node ID and
fingerprint in 18.2. Its bundle differs from A's and its independently generated
identity material produced a different fingerprint. This is a distinct
disposable first boot, not two concurrently live VMs.

### 18.5 Host boundary and non-claims

- No host, real VM, container, Proxmox/libvirt endpoint, Docker daemon, registry,
  release, package mirror write or controller was contacted or modified.
- The emulator, firmware and base image are task-owned files; the debs were
  extracted, not installed. Netdev was restricted user-mode with no host
  forward. No private key or secret material was printed, committed or
  uploaded.
- Disk use stayed bounded: the task directory was 8.3 GiB before removing the
  temporary raw inspection copies and 1.2 GiB afterwards, holding the downloaded
  image, small overlays, seeds, serial logs and text evidence.
- This is **QEMU arm64 virtual-machine boot/identity evidence only**. It is not
  Proxmox VE, generic KVM/libvirt, bare-metal, Secure Boot, a production
  enrollment or an effective network-isolation acceptance.
- The expanded 20 GiB arm64 disk profile remains open after the GPT alternate
  header failure recorded in 18.1. The deployed controller listener, a real
  private daemon, live guest-to-controller enrollment, mTLS, heartbeat, Proxmox
  VE and generic libvirt/KVM hosts also remain open. Concurrent VM identity is
  not claimed.

### 18.6 Current runtime-listener dependency for deployed enrollment

The arm64 guest wrote a digest-only enrollment bundle and secret, but it did not
contact a controller. Deployed enrollment still requires the product runtime to
start a dedicated TLS 1.3 listener that mounts `nodeEnrollmentHandler` with the
four enrollment procedures (`CreateEnrollment`,
`BeginEnrollmentChallenge`, `ClaimEnrollment`, `RevokeCredential`). In the
current tree that handler is referenced by tests only; the general panel RPC
mux deliberately does not mount `NodeControlService`. This VM boot task did not
mount broad production RPCs to work around that gap. The loopback
controller/agent fixture in section 17 exercises the shared authority and agent
separately; guest-to-controller enrollment remains blocked on the runtime
listener.

### 18.7 Hosted arm64 path prepared, not executed

`scripts/node_vm_arm64_acceptance.py`,
`scripts/test_node_vm_arm64_acceptance.py` and
`.github/workflows/node-vm-arm64-acceptance.yml` add a checked, read-only
hosted path for this same first-boot/two-identity check. The workflow runs on
`ubuntu-24.04-arm`, keeps `contents: read`, installs QEMU/AAVMF in the ephemeral
runner and verifies the pinned arm64 image. It also runs on a path-filtered
`pull_request` for a same-repository PR, checking out the exact PR head
(`github.event.pull_request.head.sha`) with `persist-credentials: false`; a job
guard refuses fork PRs. Manual dispatch remains for an explicit `expected_sha`.
No `pull_request_target` and no hosted secret is used.

The acceptance driver passes the helper's explicit `--restrict-user-network`
option. The helper renders `-netdev user,id=net0,restrict=on` with no host
forward, and the driver asserts that effective argv before accepting a boot.
The uploaded `receipt.json` is allowlisted to image/firmware/toolchain digests,
the seed-derived per-instance instance ID and its SHA-256, node ID and key
fingerprint, per-instance seed ISO SHA-256 and size, cloud-init completion,
terminal guest reboot/poweroff status, and serial-log size/SHA-256 plus scan
status. Failures record a fixed phase name and preserve completed verification
evidence. The driver checks node ID derivation from the seed and requires distinct
node IDs. It contains no serial logs, seed ISOs, keys, secrets or raw
subprocess/error output. The workflow was not dispatched or run for this report;
it is a prepared path, not hosted CI evidence.

The local base-sized arm64 evidence in 18.1-18.5 remains local evidence. The
expanded 20 GiB arm64 profile (18.1), the deployed controller listener, a real
private daemon, Proxmox VE and the generic libvirt/KVM host matrix remain open.

## 19. 2026-11-27 harness correction for the expanded arm64 disk (no guest run)

No image, guest, host, registry or release was touched. This section diagnoses
the failure recorded in 18.1 and narrows it to a reproducible input defect.

### 19.1 What the retained run shows

The expanded arm64 attempt presented a 20 GiB QCOW2 virtual size whose GPT still
described the pinned 3.5 GiB base image: the primary header kept
`alternate_lba=7340031` and the base image's usable-range end, and the backup
header stayed at LBA 7,340,031, while the disk end was LBA 41,943,039. The
serial log recorded that mismatch and then `/dev/disk/by-label/BOOT` and
`/dev/disk/by-label/UEFI` timeouts before `growpart`, ending in emergency mode.
`qemu-img create -b base.qcow2 overlay.qcow2 20G` changes only the virtual size;
it does not rewrite the guest partition table. Base-sized runs never present a
mismatch, and the previously qualified amd64 20 GiB run used the same
unnormalized input. The retained text does not fully explain why arm64 surfaced
the failure while amd64 tolerated the same table; the correction removes the
inconsistent geometry on both architectures instead of relying on that
tolerance.

### 19.2 Correction

`prepare_overlay(..., size=...)` in `scripts/node_vm_boot_qual.py` now:

1. reads the base virtual size and format with `qemu-img info`;
2. converts the pinned base **read-only** to a raw copy in the owned attempt
   directory;
3. validates the protective MBR (partition type 0xEE, start LBA 1 and
   `SizeInLBA` equal to the disk's last LBA), both GPT headers, entry CRCs and
   existing partition bounds, then rewrites only the protective MBR
   `SizeInLBA` (last-LBA sized, capped at 0xFFFFFFFF), the primary
   `alternate_lba`/`last_usable_lba`, the relocated backup entry array and the
   relocated backup header at the new end (`scripts/node_vm_gpt.py`); the
   read-back applies the same PMBR check and fails closed on any other size;
4. converts the raw copy to a standalone qcow2, runs `qemu-img check`, confirms
   the virtual size, and refuses if the base image's size or mtime changed;
5. deletes the raw intermediate and records the geometry in `boot_disk.gpt` in
   the attempt report.

The guest still runs `growpart`/`resize2fs`; the helper does not mount, inspect
or resize a host device. The base 3.5 GiB path is unchanged (same backing
overlay). The serial scan now records `gpt_backup_header_mismatch`,
`boot_label_failure` and `emergency_mode`; an expanded attempt whose log has any
of them fails the `expanded_disk_guest` gate and exits non-zero.

### 19.3 Offline evidence only

`python -m unittest discover -s scripts -p 'test_node_vm_*.py'` covers GPT
expansion and refusal cases (shrink, non-GPT, PMBR start/size, MBR/GPT CRC or
entry mismatch, header at the wrong LBA, unaligned size), base-image
preservation, the serial gates, and the expanded acceptance receipt. Those
fixtures are synthetic: **no boot was performed in this lane, and the arm64
20 GiB profile remains unqualified.** The new code path has not been exercised against a real qcow2.

### 19.4 Exact-head hosted experiment (prepared, not dispatched)

The arm64 workflow now takes an optional `disk_size` dispatch input and passes
it to the acceptance driver. The base profile stays the default, and an expanded
receipt is accepted only when the helper reports the normalization, a matching
virtual size, an unchanged base image and no stale-GPT/label/emergency marker:

```sh
python3 scripts/node_vm_arm64_acceptance.py \
  --image <pinned arm64 image> --sha256sums <pinned SHA256SUMS> \
  --workspace <owned dir> --head <full lowercase head> \
  --disk-size 20G --receipt <owned>/receipt.json
```

An accepted expanded receipt must show `disk.profile=expanded`,
`checks.expanded_disk_gpt=PASS`, `expanded_disk.new_last_lba=41943039`,
`base_image_unchanged=true` for both instances, plus the normal first-boot
identity checks. The same profile can be dispatched against the PR branch with
`-f disk_size=20G -f expected_sha=<head>`; the job already asserts that the
checked-out head is the event SHA. Until such a receipt exists at an exact head,
only the base 3.5 GiB arm64 evidence in 18.1-18.5 is qualified.
