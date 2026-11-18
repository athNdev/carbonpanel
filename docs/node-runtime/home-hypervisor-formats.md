# Home-hypervisor disk formats: evidence and support decision

This page records the OSS-380 research on Hyper-V VHDX and other home-hypervisor
disk formats and states which formats this project supports. The controlling rule
is deliberately strict:

> Implement and publish a distinct disk-image artifact **only** after a real
> clean import/boot of *our* artifact into that hypervisor has been performed and
> its evidence recorded. Otherwise the supported path is the portable Linux
> guest installer.

This lane may not create live VMs, provision hosts or change global host
configuration. It therefore produces **no** new import/boot evidence of its own.
A hypervisor's documented ability to read a disk format is not evidence that our
artifact imports and boots cleanly.

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

## Decision table

"Real clean import/boot evidence" means: our artifact imported into the named
hypervisor, the guest completed first boot, reached enrollment, and the commands
and results were recorded. A published format specification or a tool's
conversion flag does not meet that bar.

| Format / target | Documented import path (source) | Real clean import/boot evidence for our artifact | Decision |
| --- | --- | --- | --- |
| QCOW2 — Proxmox VE / KVM/libvirt | Native import; `qemu-img` (P2) | **Not in this lane.** First-class artifact and its evidence are owned by the sibling QCOW2 lane (OSS-377). This lane does not re-verify it. | **First-class / publishable**, under OSS-377 ownership. |
| Unprivileged LXC rootfs — Proxmox VE | Rootfs template import | **Not in this lane.** Owned by the sibling LXC lane (OSS-378). | **First-class / publishable**, under OSS-378 ownership. |
| Portable Linux installer | Run inside any supported Linux guest | **Not in this lane.** Owned by the sibling installer lane (OSS-379). | **Supported fallback / publishable** for every unqualified format below. |
| Hyper-V VHDX | Microsoft spec (P1); `qemu-img` convert/check (P2) | **None.** Format documented, no guest import/boot performed. | **Not supported as a distinct artifact.** Use the portable installer inside a Linux guest on the target hypervisor. |
| VirtualBox VDI / OVA / OVF | VBoxManage import; OVF transport (P3, P5) | **None.** Controller/driver caveats (P3) mean import does not imply boot. | **Not supported.** Use the portable installer. |
| VMware VMDK / OVF / OVA | VMDK support (P3); OVF datasource (P5) | **None.** | **Not supported.** Use the portable installer. |
| bhyve (FreeBSD) | raw/qcow2 via `qemu-img` and virtio-blk (P2, P6) | **None.** | **Not supported.** Use the portable installer. |
| Other containers (Parallels HDD, etc.) | Format support is partial/version-limited (P3) | **None.** | **Not supported.** Use the portable installer. |

## Consequences

* The publishable artifact set in code is exactly `qcow2`, `lxc-rootfs` and
  `installer` (`scripts/runtime_artifacts.py`, `VERIFIED_ARTIFACTS`). VHDX, VDI,
  VMDK, OVA, OVF and bhyve are refused by the publication gate.
* Home-hypervisor users are not abandoned: the portable guest installer is the
  documented, supported path on those platforms. Its unique first-boot identity
  uses the principle in P4 (per-instance `instance-id`), and enrollment inputs
  are supplied at first boot rather than baked into images.
* A future lane may promote one of the unproven formats to first-class only by
  adding real clean import/boot evidence for our artifact and updating this
  table, the artifact allowlist and the installation docs together.
