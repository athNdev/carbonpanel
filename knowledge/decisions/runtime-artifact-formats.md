---
type: Decision
title: Runtime artifact format support
description: Publish only disk formats with real clean import/boot evidence; the portable installer is the supported fallback.
status: stable
owners: [oss-runtime]
code_refs: [scripts/runtime_artifacts.py, scripts/build-runtime-qcow2.sh, scripts/build-runtime-lxc.sh, scripts/build-runtime-installer.sh, scripts/node_vm_build.py, packaging/node-lxc/build-rootfs.sh, scripts/qualify-node-installer-systemd.sh, .github/workflows/runtime-artifacts.yml]
sources:
  - title: Home-hypervisor format evidence
    resource: ../../docs/node-runtime/home-hypervisor-formats.md
  - title: Verified artifact installation
    resource: ../../docs/node-runtime/verified-artifacts-install.md
  - title: Publication boundary and version domains
    resource: ../../docs/adr/0033-release-tag-and-publication-policy.md
---

# Parts

- [Runtime artifact format support — Decision](runtime-artifact-formats-decision.md) — Only QCOW2, unprivileged LXC rootfs and the portable Linux installer are
- [Runtime artifact format support — Decision (part 2)](runtime-artifact-formats-decision-2.md) — A filename suffix or byte count is never a deployable-artifact claim.
- [Runtime artifact format support — Alternatives + Consequence](runtime-artifact-formats-alternatives.md) — Publishing converted VHDX/VMDK/VDI images from qemu-img output would claim
