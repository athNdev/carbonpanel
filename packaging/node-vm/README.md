# Reproducible node VM artifact (QCOW2 + cloud-init)

Fixture/design lane for OSS-377 under the parent RSS epic OSS-368. This lane
produces a **build definition** for a dedicated, disposable VM per managed node
for Proxmox VE and generic KVM/libvirt. It is **not published, not a release and
not a live-provisioning tool**. No QCOW2 is built here and no host VM is created,
changed or deleted.

Layout:

| Path | Purpose |
| --- | --- |
| `config/build-config.json` | Pinned versions, base-image ref, cloud-init inputs, deterministic epoch |
| `config/enrollment-policy.json` | Scoped enrollment policy; consumes the SHARED ADR0019 contract, never forks it |
| `config/upgrade-policy.json` | Staged upgrade/rollback, preserved data paths, audit |
| `provenance/base-images.json` | Pinned upstream base-image URLs, versions and signing-key fingerprint |
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
- The base image is pinned by distro/version/arch/URL, and `--apply` (the
  guarded ownership boundary, **not implemented in this lane**) must verify the
  upstream GPG-signed checksum file and require a `verified` digest before any
  install. Checked-in digests are `fixture-placeholder` and are refused for apply.

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

## Build / verify (offline only)

```sh
python scripts/node_vm_build.py --out-dir <owned-tmp>/seed --check
python scripts/node_vm_integrity.py --out-dir <owned-tmp>/integrity --verify
python scripts/node_vm_upgrade.py --from-revision 0.1.0 --to-revision 0.2.0 --batch-id b1
python -m unittest discover -s scripts -p 'test_node_vm_*.py' -v
```

All commands are local-file operations. They do not call qemu-img, xorriso,
virt-customize, a hypervisor API, a package registry or a signing service.
The integrity `signing-plan.json` describes operator-run detached signing only.
