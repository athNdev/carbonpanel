---
type: Architecture
title: "Host-native private daemon mode — Rule"
description: "internal/noderuntime/native is the HOST-NATIVE LINUX PRIVATE DAEMON mode for"
status: stable
owners: [oss-runtime]
code_refs: [internal/noderuntime/native, cmd/carbon-node, docs/plans/host-native-private-daemon.md]
sources:
  - title: Parent concept
    resource: host-native-runtime.md
---
# Rule

`internal/noderuntime/native` is the HOST-NATIVE LINUX PRIVATE DAEMON mode for
managed nodes. It is mode-specific and consumes the shared node-control
contract in `proto/carbonpanel/v1/node_control.proto`; it does not add proto
fields, RPCs, shared DB columns or auth middleware.

- A dedicated daemon process owns a private Unix socket, private data and exec
  roots, a dedicated containerd namespace and a private `bip` bridge CIDR. The
  canonical `DaemonOptions` struct is the ONLY source of daemon options; the
  rendered unit passes only `--config-file`. `CheckOptionParity` refuses a
  duplicate JSON key, an extra CLI flag or a config key repeated on the command
  line. A conflicting `bridge: none` alongside `bip` is never emitted.
- `DaemonSpec.BuildManifest()` is the exported renderer consumed by the OSS-379
  installer lane; it carries config JSON, unit, unit kind (`user`/`system`),
  launcher and canonical options. `LogDriver` defaults to journald; `json-file`
  is only for disposable fixtures without journald.
- A private `pidfile` is rendered into daemon.json under the owned runtime root
  (no-clobber validation), and the rootful unit carries the matching `PIDFile=`
  directive. This prevents collision with a preinstalled engine's
  `/var/run/docker.pid`.
- Rootless renders a per-user systemd unit (`WantedBy=default.target`, no
  `User=`) using `dockerd-rootless.sh` with `XDG_RUNTIME_DIR` and
  **`NotifyAccess=all`** (required because the READY notification comes from the
  child dockerd under rootlesskit, not the launcher main PID), never a direct
  system dockerd unit. Rootful renders an explicit system unit running as root
  (no `User=`) with the full capability set dockerd and default-cap containers
  need: Docker's default container caps plus `CAP_SYS_ADMIN`, `CAP_NET_ADMIN`
  and `CAP_SYS_RESOURCE`. In particular `CAP_FSETID`, `CAP_KILL`,
  `CAP_NET_BIND_SERVICE` and `CAP_SETFCAP` are required or ordinary game
  containers fail with `unable to apply caps`. `NoNewPrivileges=no`,
  `ProtectSystem=full`, cgroup delegation and journald are retained. An empty
  capability set or a non-root `User=` is refused. `CheckUnitContract` asserts
  these properties.
- Compatibility gates select rootless when user namespaces, subuid/subgid,
  delegated cgroup v2 controllers, a qualified storage driver and a private
  0700 runtime dir are all present. Otherwise a hardened dedicated rootful
  service is the explicit fallback. Both paths require the universal isolation
  gates; an unqualified host fails closed with a precise refusal code.
- A preinstalled/shared Docker daemon is never a fallback. Its presence is
  reported but never used to qualify the mode or route an operation.
- The enrollment boundary is owned by the mode-agnostic
  `internal/noderuntime/enroll` package; this lane only consumes it through
  `native.Runner` and a compatibility re-export shim. No mode-local proof of
  possession, claim context, certificate policy or authorization check exists.
  The shared boundary implements the ADR 0019 scheme against shared proto
  types: node-generated secret, REQUIRED approved Ed25519 key, `CSR_PRE` as
  material, challenge-bound fresh `SIG_CLAIM` over the server-stored nonce,
