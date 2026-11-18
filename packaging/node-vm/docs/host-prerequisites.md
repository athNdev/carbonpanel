# Host prerequisites (Proxmox VE and KVM/libvirt)

Design checklist for the operator-owned virtualization host. This lane does not
check, change or install any of it on a live host; the checks are inputs to
preflight on a disposable owned VM.

## Host hardware / firmware

- x86_64 (amd64) or arm64 host with hardware virtualisation enabled in firmware
  (Intel VT-x / AMD-V, or arm64 EL2).
- UEFI/OVMF firmware available for the guest (the declared target is UEFI).
- Sufficient free disk for sparse QCOW2 images plus per-node persistent volumes.
  Keep world/backup volumes on their own disk or filesystem with quotas; the
  container `storage-opt` path is not a hard world-disk guarantee (known gap).
- NTP/time sync so certificate validity and bounded enrollment TTLs behave.

## Proxmox VE

- Proxmox VE 8.x with a local or shared storage pool able to hold QCOW2 images
  and ISO images.
- A non-root operator with the narrowest role that may create VMs/volumes for
  this installation. **No Proxmox API token, root password or cluster config in
  the image, seed or provisioning metadata.**
- No automatic VM creation by this lane. Import and boot are operator actions
  on disposable owned hosts only.
- Network bridge/VLAN with DHCP for first boot (or operator-supplied static
  address as public metadata, never in the shared image).

## Generic KVM/libvirt

- qemu >= 8.2 and libvirt with qemu/KVM driver; OVMF firmware package.
- A storage pool for QCOW2 disks and an ISO pool for the NoCloud seed.
- The libvirt URI and any TLS credentials stay on the operator host and are
  **never** embedded in the artifact.
- One unique domain UUID and one unique MAC per node.

## Guest prerequisites (inside the VM)

- Ubuntu 24.04 cloud image with cloud-init and Python 3 present (first-boot
  `enroll.py` is Python 3).
- `openssl` CLI present for Ed25519 key generation and claim signing (standard
  in cloud images). Offline fixtures never invoke it.
- A writable data disk/partition mounted under the preserved data root for the
  node daemon and world/backup data; no NFS data-root (upstream unsupported).
- Outbound TLS to the controller for the reverse-initiated enrollment/session;
  no inbound port required on the VM.

## Refusals (fail closed)

- Missing hardware virtualisation, wrong firmware, unpinned base-image digest,
  missing Python 3/openssl, or an unsupported TLS topology must block
  qualification with an actionable reason — never silently weaken the setup.
- No host Docker socket, public raw Docker API, blanket-privileged DinD or
  broad host mount is an acceptable substitute for any prerequisite above.
