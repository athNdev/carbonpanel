# Supported platform and version matrix (node VM artifact)

Design targets only. No live host VM has been built or qualified in this lane.
"Supported" below means "declared build target in the artifact definition";
actual qualification requires the disposable owned-VM runs in
`qualification/QUALIFICATION.md`. Disposable QEMU UEFI/TCG guest evidence exists
for amd64 and for arm64 at the base 3.5 GiB disk size; the arm64 20 GiB
expansion (offline GPT normalization added, hosted boot pending) and every
Proxmox/libvirt host remain unqualified. Upstream documentation claims are
dated; this matrix does not assert tested compatibility.

| Axis | Declared build target | Not supported / unknown |
| --- | --- | --- |
| Virtualisation host | Proxmox VE 8.x (KVM/qemu), generic KVM/libvirt with qemu >= 8.2 | VirtualBox, VMware, cloud hypervisors, nested container-hosted engines |
| Guest distro | Ubuntu 24.04 LTS (Noble) released 20261017 cloud image, amd64 + arm64 (immutable dated URL) | Other distros and the moving `/release/` or `/current/` paths (never pins) |
| Arch | amd64, arm64 (release targets per ADR0010) | s390x, riscv64, others |
| Firmware | UEFI (OVMF/EFI) | Legacy BIOS (without an explicit separate target) |
| Disposable QEMU guest | amd64 UEFI/TCG on i440fx and q35; arm64 UEFI/TCG on `virt` at the base 3.5 GiB size | arm64 20 GiB expansion (offline GPT correction only), Secure Boot, Proxmox/libvirt host qualification |
| Disk format | QCOW2, 20 GiB sparse default | Raw, VMDK (out of scope) |
| cloud-init datasource | NoCloud seed ISO (CIDATA) | ConfigDrive/other datasources until separately pinned |
| qemu-img toolchain | 8.2.0 (pinned in build config) | Unpinned host qemu-img output is not claimed reproducible |
| xorriso toolchain | 1.5.6 (pinned) | Unpinned ISO tooling |
| Guest Python | 3.12 (cloud images ship cloud-init/Python 3) | Images without Python 3 (first boot cannot run enroll.py) |
| Enrollment contract | ADR0019 `node_control.proto` protocol version N / N-1 | Forked or locally redefined enrollment RPCs (forbidden) |
| Node agent version | `0.1.0-fixture` placeholder, pinned in build config | Unpinned/"latest" agent in an image |
| Base-image digest | Real verified SHA256 from GPG-signed upstream SHA256SUMS; key fingerprint `D2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81`; evidence in `provenance/RETRIEVAL-EVIDENCE.md` | Synthetic fixture digests in artifact mode; unverified image bytes; silent fallback to another image |
| Persistent volume | Copy-only import/backup/recover into preserved paths | In-place mutation of world/backup/DB paths, volume deletion |
| Upgrade/rollback | Staged, checksum-verified undo manifest | Retry after `UNCERTAIN`; unverified rollback |

## Proxmox VE notes (design only)

- Use only the immutable dated image URLs recorded in `provenance/base-images.json`;
  verify the downloaded image SHA256 against the signed checksum before import.
- Import the QCOW2 as a VM disk, attach the NoCloud seed ISO as a second CD/DVD
  device, boot once, then detach. Do not reuse a VMID, disk serial or MAC across
  distinct nodes: first-boot identity derives from the unique instance-id.
- No Proxmox API token, root credential or cluster config is part of the image
  or seed. Provisioning metadata carries only public node label/instance-id.

## Generic KVM/libvirt notes (design only)

- Define a per-node domain with its own disk, unique UUID and unique MAC. Attach
  the NoCloud seed as an CDROM with volume label `CIDATA`.
- No libvirt URI, TLS cert, password or host path is embedded in the artifact.
  Host prerequisites and isolation are documented in `docs/host-prerequisites.md`
  and `docs/isolation.md`.
