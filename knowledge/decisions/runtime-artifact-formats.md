---
type: Decision
title: Runtime artifact format support
description: Publish only disk formats with real clean import/boot evidence; the portable installer is the supported fallback.
status: stable
owners: [oss-runtime]
code_refs:
  - scripts/runtime_artifacts.py
  - .github/workflows/runtime-artifacts.yml
sources:
  - title: Home-hypervisor format evidence
    resource: ../../docs/node-runtime/home-hypervisor-formats.md
  - title: Verified artifact installation
    resource: ../../docs/node-runtime/verified-artifacts-install.md
  - title: Publication boundary and version domains
    resource: ../../docs/adr/0033-release-tag-and-publication-policy.md
---

# Decision

Only QCOW2, unprivileged LXC rootfs and the portable Linux installer are
publishable runtime artifacts (`VERIFIED_ARTIFACTS`). A distinct home-hypervisor
disk image (Hyper-V VHDX, VirtualBox VDI/OVA, VMware VMDK/OVF, bhyve) is added
only after a real clean import and boot of our artifact is performed and its
evidence recorded in the owning artifact lane. Until then the portable guest
installer is the supported path on those platforms.

A vendor's documented support for a disk format is not import/boot evidence.
The publication gate refuses unproven formats; the format table and its primary
sources are authoritative in the docs.

# Alternatives

- Publishing converted VHDX/VMDK/VDI images from `qemu-img` output would claim
  boot support that no test in these lanes establishes.
- Treating a successful disk import as a clean boot ignores guest controller
  and firmware driver requirements documented by the hypervisor vendor.

# Consequence

Home-hypervisor users install the portable guest installer inside a Linux
guest. Enrollment inputs stay out of images and provisioning metadata; unique
first-boot identity follows the cloud-init `instance-id` model. Promoting a new
format requires the new evidence, the artifact allowlist and the install docs to
change together.
