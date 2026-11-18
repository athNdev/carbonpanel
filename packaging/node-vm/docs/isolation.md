# Distinct VM isolation model

A managed node VM is a **separate kernel boundary**, not another container on
the controller host. This is the VM-isolation option described in
`docs/adr/0019`'s parent plan (§5.4): an operator who requires a stronger
boundary than shared-kernel container isolation runs one owned VM per node.

## Boundary properties

- **One VM per node.** Each managed node gets its own domain, disk, CPU/memory
  allocation and network interface. Two nodes never share a VM, disk serial,
  UUID, MAC or first-boot instance-id.
- **Separate kernel.** The node's dedicated Docker daemon (and its game
  containers) run inside the VM kernel; a container breakout reaches the VM,
  not the Proxmox/KVM host or the controller. This is a stronger boundary than
  rootless containers on a shared kernel, but it is not a security proof:
  hypervisor, firmware and device-passthrough attack surface remain.
- **No host Docker socket.** No `/var/run/docker.sock` bind, no host Docker
  data root and no broad host mounts cross the VM boundary. The daemon inside
  the VM uses its own private socket, data root, exec root and containerd
  namespace (managed-runtime lane).
- **No inbound management port.** The agent dials out to the controller under
  the shared node-control contract. No raw Docker API or agent admin port is
  published from the VM.
- **No shared identity.** First boot derives a unique `node_id` from the
  instance-id and generates a fresh keypair and secret locally. No baked token,
  key or enrollment input is shared between images.

## What the VM boundary does NOT provide

- It is not a claim of proven isolation. Effective CPU/memory/pids limits,
  virtio device exposure and PCI passthrough still need qualification on owned
  disposable hosts.
- It does not replace least privilege inside the VM: the dedicated daemon is
  still rootless-by-default with a hardened dedicated-service fallback (managed
  runtime lane), and privileged mode remains a named operator override.
- It does not make a single controller a distributed admission authority. The
  in-process admission decision (ADR0014) is unchanged; multiple controllers
  targeting one node remain an open cross-process question.
- It does not migrate legacy node data automatically or provide a silent
  fallback between transports.

## Fixture status

This document describes intended isolation properties. No live VM, effective
policy inspection or breakout/legitimate-traffic test has been run in this lane;
those belong to the disposable owned-VM qualification matrix.
