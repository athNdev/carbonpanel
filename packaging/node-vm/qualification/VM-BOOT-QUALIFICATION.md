# OSS-377 VM artifact qualification evidence (real base-image bytes; boot blocked)

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

## 12. 2026-11-19 follow-up: disposable container TCG probe (this session)

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
driver reports gates conservatively (PASS/FAIL/BLOCKED/NOT_RUN/OBSERVED/NOT
OBSERVED) and does not claim live enrollment, daemon health or effective
network isolation from serial markers alone.

Prepare-only check in this session (Windows Python, no ISO tool/qemu on PATH):
deterministic seed generation succeeded for `oss377-boot-qual-0001` (`node-label
oss377boot`) but CIDATA ISO creation and qemu availability were correctly
reported as blocked/missing in that environment. In WSL, `genisoimage` and
deterministic seed/ISO creation are available for disposable runs.

TCG boot attempt status: a task-owned download of the verified amd64
`release-20261017` image and a disposable container-based QEMU TCG boot attempt
were initiated from WSL as part of this follow-up. At the time of this record,
no completed clean-boot evidence (first-boot bundle creation, network/health
probe, or reboot persistence) has been captured from that attempt in this
document. Any later evidence must be appended with exact command, log/report
artifacts, elapsed time and explicit gate outcomes; absence of evidence here is
**not** a boot pass. Prior claims remain unchanged: whole-image byte
provenance is VERIFIED (section 3); real clean boot on a capable runner
remains the acceptance gate.
