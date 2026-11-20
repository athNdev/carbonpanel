---
type: Architecture
title: "Node VM artifact — Rule (part 2)"
description: "Disposable boot and enrollment proof checks for the planned QCOW2 artifact."
status: draft
owners: [oss-runtime]
code_refs: [packaging/node-vm, scripts/node_vm_build.py, scripts/node_vm_integrity.py, scripts/node_vm_upgrade.py, scripts/node_vm_verify_image.py, scripts/test_node_vm_build.py, scripts/test_node_vm_integrity.py, scripts/test_node_vm_upgrade.py, scripts/test_node_vm_enroll.py, scripts/test_node_vm_verify_image.py, packaging/node-vm/qualification/VM-BOOT-QUALIFICATION.md]
sources:
  - title: Parent concept
    resource: node-vm-artifact.md
---
The build entry point emits a plan and NoCloud seed. The guest publishes
the Ed25519 key, secret and digest-only bundle together in a root-only
enrollment directory. Repeated first boot reuses matching state. Incomplete
or mismatched state is preserved and refused. Use a signed PKCS#10 DER
`CSR_PRE` and an unpadded base64url secret for the shared protobuf string
field. The default NoCloud seed enables IPv4 DHCP on Ubuntu's predictable
Ethernet interface names.

Clean boot can be checked on a disposable QEMU runner with hardware or software
acceleration. Record the firmware, architecture, seed and base image digests.
Measure network and identity persistence in the guest. Shared enrollment
authority checks validate proof and credential scope. Live mTLS enrollment,
heartbeat and daemon health require an installed node agent and a mounted
controller service. See [disposable VM qualification](../../packaging/node-vm/qualification/VM-BOOT-QUALIFICATION.md).
