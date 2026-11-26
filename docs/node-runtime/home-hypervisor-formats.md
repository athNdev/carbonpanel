# Home-hypervisor disk formats: evidence and support decision

This page records the OSS-380 research on Hyper-V VHDX and other home-hypervisor
disk formats and states which formats this project supports. The controlling rule
is deliberately strict:

> Implement and publish a distinct disk-image artifact **only** after a real
> clean import/boot of *our* artifact into that hypervisor has been performed and
> its evidence recorded. Otherwise the documented path is the portable Linux
> guest installer.

The platform references establish which formats each hypervisor can read.
Carbon Panel qualifies its own artifacts using disposable fixtures and records
the exact platform, artifact digest and outcomes.

## Primary sources consulted

Only primary sources (vendor/upstream documentation and specifications) were
used. No search-engine summaries or third-party blogs were treated as evidence.

| # | Primary source | What it actually establishes |
| --- | --- | --- |
| P1 | Microsoft, *VHDX Format Specification v1.00* (published 2024-07-15, released under the Microsoft Open Source Promise) — <https://www.microsoft.com/en-us/download/details.aspx?id=34750> | VHDX is a documented Microsoft disk-in-a-file format. It says nothing about booting a Carbon Panel guest. |
| P2 | QEMU, *qemu-img manual* (upstream source) — <https://raw.githubusercontent.com/qemu/qemu/master/docs/tools/qemu-img.rst> | `qemu-img` creates/converts/checks `qcow2`, `vhdx`, `vmdk`, `vdi`, `raw` and others offline; it warns never to modify an image in use by a running VM. Conversion support is a tool capability, not a boot result. |
| P3 | Oracle, *VM VirtualBox Manual, ch. 5 Virtual Storage* — <https://www.virtualbox.org/manual/ch05.html> | VirtualBox supports VDI, VMDK, VHD, HDD; OVF import/export; disks carry a UUID so copies must be cloned; AHCI/SAS/NVMe/virtio-scsi and EFI boot require guest driver support. A successful disk import can still fail to boot. |
| P4 | cloud-init, *NoCloud datasource* — <https://docs.cloud-init.io/en/latest/reference/datasources/nocloud.html> | `instance-id` is the first-boot determinant; seed data can be a `CIDATA`-labelled vfat/iso9660 disk or `ds=nocloud;s=...` via kernel command line / DMI. This is the portable unique-identity mechanism used instead of baking secrets into images. |
| P5 | cloud-init, *OVF datasource* — <https://docs.cloud-init.io/en/latest/reference/datasources/ovf.html> | OVF transport is documented for GCP, OpenShift, Proxmox, vSphere, VirtualBox and Xen, but cloud-init recommends a platform-specific datasource where one exists; user-data/network-config travel as OVF XML properties. |
| P6 | FreeBSD Handbook, *Virtualization* — <https://docs.freebsd.org/en/books/handbook/virtualization/> | QEMU on FreeBSD uses `qemu-img` with raw/qcow2 images and virtio-blk; bhyve is a separate host feature. No Carbon Panel boot evidence. |

Microsoft lists Ubuntu 24.04 for Hyper-V Generation 1 and Generation 2.
The generation choice still determines the firmware, boot and Secure Boot
requirements for an imported appliance. See the
[Hyper-V VM generation guide](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/plan/should-i-create-a-generation-1-or-2-virtual-machine-in-hyper-v).

## Decision table

"Real clean import/boot evidence" means: our artifact imported into the named
hypervisor, the guest completed first boot, reached enrollment, and the commands
and results were recorded. A published format specification or a tool's
conversion flag does not meet that bar.

| Format / target | Documented import path (source) | Real clean import/boot evidence for our artifact | Decision |
| --- | --- | --- | --- |
| QCOW2 — Proxmox VE / KVM/libvirt | Native import and `qemu-img` inspection (P2) | The [image provenance report](../../packaging/node-vm/qualification/VM-BOOT-QUALIFICATION.md) verifies the upstream base. A [QEMU TCG probe](https://github.com/athNdev/carbonpanel/pull/589) exercised one Ubuntu first boot. | **Preview** for a dedicated VM with its own kernel. |
| Unprivileged LXC rootfs — Proxmox VE | Rootfs template import | [Signed upstream archive retrieval](../../packaging/node-lxc/qualification/SIGNED-ARCHIVE-EVIDENCE.md) is verified. Proxmox import and nested runtime qualification remain open. | **Preview** with a shared host kernel and explicit nesting requirements. |
| Portable Linux installer | Run inside a supported Linux guest | [Installer fixtures](../node-installer.md) cover guest-side selection, rendering and state handling. | Documented guest-side path where a qualified image is unavailable. |
| Hyper-V VHDX | Microsoft spec (P1); `qemu-img` convert/check (P2) | **None.** Format documented, no guest import/boot performed. | **Not supported as a distinct artifact.** Use the portable installer inside a Linux guest on the target hypervisor. |
| VirtualBox VDI / OVA / OVF | VBoxManage import; OVF transport (P3, P5) | **None.** Controller/driver caveats (P3) mean import does not imply boot. | **Not supported.** Use the portable installer. |
| VMware VMDK / OVF / OVA | VMDK support (P3); OVF datasource (P5) | **None.** | **Not supported.** Use the portable installer. |
| bhyve (FreeBSD) | raw/qcow2 via `qemu-img` and virtio-blk (P2, P6) | **None.** | **Not supported.** Use the portable installer. |
| Other containers (Parallels HDD, etc.) | Format support is partial/version-limited (P3) | **None.** | **Not supported.** Use the portable installer. |

## Consequences

* Candidate artifact kinds are `qcow2`, `lxc-rootfs` and `installer`. Signing
  requires both native verification and a reviewed record with the tested
  bundle's exact checksum manifest digest. VHDX, VDI, VMDK, OVA, OVF and
  bhyve disk-image variants are outside the publication workflow.
* Home-hypervisor users can follow the [portable Linux installer](../node-installer.md)
  for the available guest-side tooling. Supply enrollment inputs after boot.
  VM cloud-init configuration uses a per-instance `instance-id` (P4).
* A future lane may promote one of the unproven formats to first-class only by
  adding real clean import/boot evidence for our artifact and updating this
  table, the artifact allowlist and the installation docs together.
