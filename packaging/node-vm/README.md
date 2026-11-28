# Reproducible node VM artifact (QCOW2 + cloud-init)

Fixture/design lane for OSS-377 under the parent RSS epic OSS-368. This lane
produces a **build definition** for a dedicated, disposable VM per managed node
for Proxmox VE and generic KVM/libvirt. It is **not published, not a release and
not a live-provisioning tool**. The build entry point emits a plan and NoCloud
seed. See [QEMU boot qualification](qualification/VM-BOOT-QUALIFICATION.md)
for runs on disposable overlays of the pinned upstream QCOW2.

Layout:

| Path | Purpose |
| --- | --- |
| `config/build-config.json` | Pinned versions, base-image ref, cloud-init inputs, deterministic epoch |
| `config/enrollment-policy.json` | Scoped enrollment policy; consumes the SHARED ADR0019 contract, never forks it |
| `config/upgrade-policy.json` | Staged upgrade/rollback, preserved data paths, audit |
| `provenance/base-images.json` | REAL pinned immutable dated Ubuntu image URLs, verified SHA256s, signed-checksum and key evidence |
| `provenance/base-images.fixture.json` | SYNTHETIC fixture-only placeholders; never used for artifact claims |
| `provenance/RETRIEVAL-EVIDENCE.md` | Exact direct official-source retrieval evidence and evidence limits |
| `cloud-init/*.tpl` | NoCloud seed templates containing public metadata only |
| `cloud-init/firstboot/enroll.py` | Node-local first-boot identity + digest-only enrollment bundle (no secrets in image/seed) |
| `docs/` | Distinct VM isolation and host-prerequisite documentation |
| `qualification/` | Disposable owned-VM qualification design and synthetic fixtures |

The build logic lives in `scripts/node_vm_build.py`, `node_vm_integrity.py` and
`node_vm_upgrade.py`; tests are `scripts/test_node_vm_*.py`.

## What is reproducible

- Cloud-init NoCloud seed content is rendered from pinned templates and public
  metadata only, with `SOURCE_DATE_EPOCH` and sorted, canonical manifests. Two
  runs with the same inputs produce byte-identical seed files, checksum
  manifest, SBOM and build plan (asserted by `--check` and the fixture suite).
- The base image is pinned to the **immutable dated** Ubuntu 24.04 release
  `release-20261017` for amd64 and arm64, with real non-placeholder SHA256
  values read from the upstream GPG-signed `SHA256SUMS` (key fingerprint
  `D2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81`). Exact URLs, hashes and retrieval
  steps are in `provenance/RETRIEVAL-EVIDENCE.md`. The moving `/release/` and
  `/current/` paths are never used as pins.
- Artifact mode (`--profile artifact`, the default CLI profile) calls
  `verify_artifact_provenance()` and **fails closed** on placeholder/synthetic
  digests, moving URLs or missing signature evidence. Offline tests explicitly
  select `--profile fixture`, whose values are synthetic and never used for
  artifact claims. A real build must still download the image and recompute its
  SHA256; `--apply` (the guarded ownership boundary, **not implemented in this
  lane**) must verify the signed checksum before any install.

The raw QCOW2 byte image is **not** claimed byte-reproducible across unpinned
qemu-img versions: it is verified by the checksum manifest produced from a
pinned toolchain. This boundary is stated in `qualification/QUALIFICATION.md`.

## Enrollment (shared contract, scoped)

The image and its cloud-init seed contain **no** enrollment secret, private key,
approved key, digest bundle or token. At first boot the VM generates a CSPRNG
secret and Ed25519 keypair locally, keeps them in root-only files on the VM, and
exports only a digest-only bundle (salt, digest, `CSR_PRE`, SPKI, fingerprint)
for out-of-band operator registration under the SHARED ADR0019
`node_control.proto` contract. This lane does not define, fork or mount any
node-control RPC. The bootstrap scopes are limited to
`BeginEnrollmentChallenge` and `ClaimEnrollment`.

The Ed25519 key (`node.key`), opaque secret (`enrollment-secret.bin`) and bundle
(`enrollment-bundle.json`) are published together under
`/var/lib/carbon-node/enrollment/`. Repeating first boot with the same instance
identity reuses the existing files only after every required preregistration
field decodes within strict base64 bounds and the saved `CSR_PRE` passes the
same assertions as the shared authority: one self-signed PKCS#10 DER request,
Ed25519-only, whose public key equals the approved SPKI. The DER framing is
canonical (no redundant leading zero in a long-form length) and the Ed25519
signature algorithm has no parameters, as the shared Go authority requires. No
reserialized CSR bytes are compared. Its detached `csr_pre_signature` must also
verify with the stored `node.key`, and `contract_ref`, `object_id_binding` and
`bootstrap_scopes` must equal the checked-in enrollment policy. Missing,
malformed or inconsistent state requires operator review and is preserved on
failure; an existing identity is never overwritten or regenerated. A
pre-hardening record (raw token, missing `node.key` or signed-JSON pseudo-CSR)
is refused read-only and requires explicit operator re-provisioning; there is
no automatic migration. First-boot generation applies the same strict
secret/salt size bounds as reuse before consuming entropy, so an invalid policy
is refused without writing an identity. The secret encodes the policy-defined
random bytes as unpadded base64url so it can cross the shared protobuf string
field. `CSR_PRE` is a signed PKCS#10 DER request for the same key.

The default NoCloud network seed requests IPv4 DHCP on predictable Ubuntu
Ethernet interface names (`en*`). Attach the VM only to the intended private
network. Override the network seed per VM when static addresses or different
interface matching are required.

`SIG_CLAIM` uses the **shared** ADR0019/OSS-385 `ClaimContextRaw` byte encoding:
NUL-delimited `carbon-panel-enroll-v1`, node/enrollment/challenge IDs, the
**server-stored** nonce, enrollment `expires_at` (RFC3339Nano), comma-joined
proto capability names and the channel context, with no trailing separator. It
is Ed25519-only; canonical-JSON contexts are refused. Byte-for-byte conformance
is checked against a golden produced by `internal/noderuntime/enroll` at PR532
head `fc998875` (`qualification/fixtures/claim-context-shared.json`). The guest
Python module reproduces the encoder because a cloud image cannot import the Go
package; any future production mount must delegate the claim to the shared
package rather than add a second encoder.

## Controller and managed agent fixture

`internal/rpc/oss377_managed_fixture_test.go` exercises the shared enrollment
authority and managed agent over loopback TLS 1.3. The Linux fixture covers
enrollment, scoped mTLS, preflight, a periodic heartbeat, credential reuse
after an agent restart and daemon health against a test-owned Docker HTTP API.
See the [fixture results and remaining VM checks](qualification/VM-BOOT-QUALIFICATION.md#17-controlleragent-fixture-for-managed-enrollment-and-health-2026-11-26).

```sh
go test ./internal/rpc -run '^TestOSS377ManagedControllerAgentFixture$' -count=1 -v
```

## Build / verify (offline only)

```sh
python scripts/node_vm_build.py --profile fixture --out-dir <owned-tmp>/seed --check
python scripts/node_vm_integrity.py --profile fixture --out-dir <owned-tmp>/integrity --verify
# Real-pin check (no image download, no QCOW2 build):
python scripts/node_vm_build.py --profile artifact --check
python scripts/node_vm_upgrade.py --from-revision 0.1.0 --to-revision 0.2.0 --batch-id b1
python -m unittest discover -s scripts -p 'test_node_vm_*.py' -v
```

UEFI is the declared target and the qualification helper's default. It resolves
OVMF/edk2 or fails closed (use `CARBON_OVMF_CODE`/`CARBON_OVMF_VARS` for a
non-standard host) rather than falling back to BIOS. On a capable disposable
runner, `--firmware uefi --machine q35 --disk-size 20G` exercises the declared
amd64 profile; `--arch aarch64 --machine virt --firmware uefi --disk-size 20G`
prepares the same expanded profile for arm64 by sizing the protective MBR to
the disk's last LBA and relocating the GPT backup header before boot (no
expanded arm64 boot is claimed yet). `--arch aarch64 --machine
virt --firmware uefi` exercises the declared arm64 target at the base disk size
and selects `-cpu max` (TCG) or `-cpu host` (KVM). The isolated amd64 QEMU/TCG
run covers filesystem expansion, a second boot and saved identity persistence.
The arm64 run covers a root-only first boot, a clean reboot and a distinct
second instance-id. The helper copies a fresh private UEFI variable store per
run and never writes system firmware. See
[QEMU boot qualification](qualification/VM-BOOT-QUALIFICATION.md#16-expanded-20-gib-q35-boot-and-identity-persistence-2026-11-26)
and the
[arm64 run](qualification/VM-BOOT-QUALIFICATION.md#18-arm64-qemu-virtuefi-boot-clean-reboot-and-second-identity-2026-11-26).

Use a fresh `--work-dir` for every attempt. The helper reserves the attempt
output set before writing a seed, overlay or report and refuses to reuse a prior
run without changing its bytes. Invalid architecture/firmware/machine
combinations (for example aarch64 with BIOS) are rejected before outputs, and a
QEMU launch failure persists a failure report; both return non-zero.

All commands are local-file operations. They do not call qemu-img, xorriso,
virt-customize, a hypervisor API, a package registry or a signing service.
The integrity `signing-plan.json` describes operator-run detached signing only.
