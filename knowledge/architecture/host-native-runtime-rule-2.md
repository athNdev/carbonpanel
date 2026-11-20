---
type: Architecture
title: "Host-native private daemon mode — Rule (part 2)"
description: "issue-certificate-first atomic consume, controller-derived TLS topology"
status: stable
owners: [oss-runtime]
code_refs: [internal/noderuntime/native, cmd/carbon-node, docs/plans/host-native-private-daemon.md]
sources:
  - title: Parent concept
    resource: host-native-runtime.md
---
  issue-certificate-first atomic consume, controller-derived TLS topology
  support/refusal, a signed public client certificate and three non-crossing
  authorization domains. No secret is stored or returned in a read response.
- `cmd/carbon-node` is the bounded preflight/doctor renderer. It reads an
  injected HostFacts document and can print the rendered config/unit to owned
  paths; it never probes or changes a live host, installs a daemon or writes a
  system unit. Enrollment fixtures use synthetic keys and temp-dir private
  material only.
- **Qualification status (fixture-only):** on a clean disposable Ubuntu 24.04 /
  systemd 255 fixture with real dockerd 29.1.3, the EXACT shipped **rootful**
  unit booted `active/success` with private socket/pidfile/roots, bridge, DNAT,
  default-cap container, limits and live-restore restart. The EXACT shipped
  **rootless** per-user unit later also booted `active/success` with real
  dockerd on a suitable disposable fixture: real non-root `systemd --user`
  session, private socket/pidfile/roots, `name=rootless`, `vfs`, a real
  container with a published port, bridge-side listener, **measured client TCP
  reachability and HTTP 200**, applied memory/pids limits and survival of an
  ambient `XDG_RUNTIME_DIR` reset. Rootless publishing is slirp4netns builtin
  userspace forwarding, so no host DNAT rule is claimed for that mode. The
  earlier WSL2 failure was a fixture layout/kernel limit, not a unit defect.
  Neither mode has live-host, gameplay, release or deployment evidence.
- `NodeCapability` values are usable feature grants the controller gates on,
  not raw detections. With no qualified daemon mode every daemon-dependent
  grant is withheld at one boundary; host diagnostics and daemon-independent
  facilities (`STORAGE_WORKER`, `DRAIN`) remain. See the plan's capability
  snapshot contract.
- Qualification requires both the host gates and the derived daemon spec to
  pass. A spec refusal fails preflight with its code and remedy and prevents
  the doctor from emitting a unit or config.
- Rootless qualification requires systemd and a user manager with linger.
  A missing user manager or linger can select the dedicated rootful service
  when the operator explicitly qualifies it.
