---
type: Decision
title: "Runtime artifact format support — Alternatives + Consequence"
description: "Publishing converted VHDX/VMDK/VDI images from qemu-img output would claim"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: runtime-artifact-formats.md
---
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
