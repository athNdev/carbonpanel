---
type: Architecture
title: "Node VM artifact — Why + How to check + Read next"
description: "A shared image with baked credentials or an unpinned base would defeat the"
status: draft
owners: [oss-runtime]
code_refs: [packaging/node-vm, scripts/node_vm_build.py, scripts/node_vm_gpt.py, scripts/node_vm_integrity.py, scripts/node_vm_upgrade.py, scripts/node_vm_verify_image.py, scripts/node_vm_boot_qual.py, scripts/test_node_vm_build.py, scripts/test_node_vm_gpt.py, scripts/test_node_vm_integrity.py, scripts/test_node_vm_upgrade.py, scripts/test_node_vm_enroll.py, scripts/test_node_vm_verify_image.py, scripts/test_node_vm_boot_qual.py, packaging/node-vm/qualification/VM-BOOT-QUALIFICATION.md]
sources:
  - title: Parent concept
    resource: node-vm-artifact.md
---
# Why

A shared image with baked credentials or an unpinned base would defeat the
fail-closed enrollment boundary and make two nodes share identity. Independent
build inputs and a secret-free first boot keep the VM option compatible with
the shared node-control contract without duplicating it.

# How to check

- `python -m py_compile scripts/node_vm_*.py scripts/test_node_vm_*.py
  packaging/node-vm/cloud-init/firstboot/enroll.py`
- `python -m unittest discover -s scripts -p 'test_node_vm_*.py'` (offline,
  synthetic keys; fixture provenance profile)
- `python scripts/node_vm_build.py --profile artifact --check` (real dated pins;
  refuses placeholder digests) and `--profile fixture --check` (offline plan)
- `python scripts/node_vm_integrity.py --profile fixture --verify` (checksum +
  CycloneDX SBOM + key-free signing plan)
- `python scripts/node_vm_upgrade.py --from-revision A --to-revision B
  --batch-id ID` (staged undo plan)
- `python3 scripts/node_vm_verify_image.py --image <downloaded.img> --ref
  ubuntu-24.04-amd64 --sha256sums <SHA256SUMS>` (real-byte check, offline and
  read-only; requires a caller-supplied image file)
- `python3 scripts/node_vm_boot_qual.py --work-dir <owned-tmp> --generate-seed`
  prepares deterministic seed/ISO inputs and reports qemu/firmware availability;
  with `--image <verified.qcow2>` on a capable disposable runner (qemu-system with
  TCG or KVM) it attempts a boot, captures serial-console evidence and can
  perform offline disk inspection. UEFI is the default; missing OVMF/edk2 is
  reported blocked, never silently replaced by BIOS. Use a fresh work directory
  per attempt: the helper reserves outputs before writing a seed, overlay or
  report and refuses reuse, so prior evidence bytes are preserved. Unsupported
  target combinations and QEMU launch failures return non-zero; launch failures
  still persist a failure report. `--disk-size 20G` flattens a read-only base
  and relocates the GPT backup header to the new end before boot; the arm64
  expanded profile remains unqualified. Report blocked/unobserved gates
  honestly; never claim live boot, enrollment or network isolation without
  evidence.
- Never substitute a real key, endpoint or world path for a fixture value, and
  never run these drivers against a live VM from this lane.

# Read next

- [ADR 0035](../../docs/adr/0035-node-vm-artifact.md)
- [Node control protocol](node-control-protocol.md)
- [Runtime seams](runtime-seams.md)
