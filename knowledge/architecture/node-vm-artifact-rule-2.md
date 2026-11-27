---
type: Architecture
title: "Node VM artifact — Rule (part 2)"
description: "Disposable boot and enrollment proof checks for the planned QCOW2 artifact."
status: draft
owners: [oss-runtime]
code_refs: [packaging/node-vm, scripts/node_vm_build.py, scripts/node_vm_gpt.py, scripts/node_vm_integrity.py, scripts/node_vm_upgrade.py, scripts/node_vm_verify_image.py, scripts/node_vm_boot_qual.py, scripts/node_vm_arm64_acceptance.py, scripts/test_node_vm_build.py, scripts/test_node_vm_gpt.py, scripts/test_node_vm_integrity.py, scripts/test_node_vm_upgrade.py, scripts/test_node_vm_enroll.py, scripts/test_node_vm_verify_image.py, scripts/test_node_vm_boot_qual.py, scripts/test_node_vm_arm64_acceptance.py, internal/rpc/oss377_managed_fixture_test.go, packaging/node-vm/qualification/VM-BOOT-QUALIFICATION.md]
sources:
  - title: Parent concept
    resource: node-vm-artifact.md
---
The build entry point emits a plan and NoCloud seed. The guest publishes
the Ed25519 key, secret and digest-only bundle together in a root-only
enrollment directory. Repeated first boot reuses matching state only after the
complete saved preregistration record passes strict bounded base64, node/key/
fingerprint/digest checks, policy-equal `contract_ref`, `object_id_binding` and
bootstrap scopes, and the same `CSR_PRE` assertions as the shared authority:
one self-signed Ed25519 PKCS#10 DER request whose SPKI equals the approved key,
plus a detached proof made by that key. Der framing is canonical (no redundant
leading zero in a long-form length) and the Ed25519 signature algorithm has no
parameters, matching the shared Go authority. No reserialized CSR bytes are
assumed. First-boot generation validates the strict secret/salt size bounds
before consuming entropy, so it cannot persist a record that reuse rejects.
Missing, malformed or inconsistent state is preserved and refused; pre-hardening
records (raw token, missing `node.key`, signed-JSON pseudo-CSR) are refused
read-only with no automatic migration. Use a signed PKCS#10 DER `CSR_PRE` and an
unpadded base64url secret for the shared protobuf string field. The default
NoCloud seed enables IPv4 DHCP on Ubuntu's predictable Ethernet interface names.
Clean boot can be checked on a disposable QEMU runner with hardware or software
acceleration. Record the firmware, architecture, seed and base image digests.
The helper defaults to the declared UEFI target, fails closed without OVMF/edk2
instead of booting SeaBIOS, copies a fresh private variable store and can request
an expanded overlay. Every attempt needs a fresh work directory: the helper
reserves its output set before writing a seed, overlay or report, refuses reuse
without changing prior bytes, rejects unsupported architecture/firmware/machine
combinations before outputs, and persists a failure report when QEMU cannot
launch. The arm64 acceptance mode passes an explicit restricted-user-network
option, which the helper renders as `-netdev user,id=net0,restrict=on` with no
host forward. The driver asserts that effective argv and records the per-instance
seed ISO SHA-256, the seed-derived instance ID with its SHA-256, and cloud-init
completion and a terminal guest reboot/poweroff marker. It checks node ID
derivation and uniqueness. Failure receipts name the phase and retain completed
verification. Serial logs, seeds, keys and raw helper output stay private.
Measure network and identity persistence in the guest. The disposable amd64
20 GiB q35 run covers disk expansion and identity persistence across a clean
second boot. The base-sized arm64 run covers a guest-initiated restart, a
separate QEMU launch on the same disk/UEFI/seed, and a QMP-initiated second-boot
shutdown; local evidence was independently confirmed. An expanded disk now
flattens a read-only base and relocates the GPT backup header before boot; the
arm64 20 GiB profile failed that geometry and remains unqualified pending hosted
evidence. Hosted first-boot evidence is scoped to the exact PR head. Shared
enrollment authority checks validate proof and credential scope.
`internal/rpc/oss377_managed_fixture_test.go` runs the real shared authority and
`agent.Run` on Linux over loopback TLS 1.3 for challenge, scoped mTLS,
heartbeat and a fake-daemon health probe. That is a controller/agent fixture,
not VM evidence: the guest-to-controller claim, a real dockerd, a
production-mounted control RPC and the host matrix remain open. Offline
`scripts/test_node_vm_boot_qual.py` and `scripts/test_node_vm_arm64_acceptance.py`
cover argv, receipt and identity refusals without QEMU. See [disposable VM
qualification](../../packaging/node-vm/qualification/VM-BOOT-QUALIFICATION.md).
