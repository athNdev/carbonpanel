---
type: Architecture
title: Host-native private daemon mode
description: Mode-specific Linux runtime that owns a private Docker socket, roots and network; it consumes the shared node-control contract and never falls back to a host engine.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/noderuntime/native
  - cmd/carbon-node
  - docs/plans/host-native-private-daemon.md
sources:
  - title: ADR 0019 node control contract
    resource: ../../docs/adr/0019-node-control-contract.md
  - title: Host-native mode prerequisite and isolation contract
    resource: ../../docs/plans/host-native-private-daemon.md
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
- Rootless renders a per-user systemd unit (`WantedBy=default.target`, no
  `User=`) using `dockerd-rootless.sh` with `XDG_RUNTIME_DIR`, never a direct
  system dockerd unit. Rootful renders an explicit system unit running as root
  (no `User=`) with the capability set dockerd needs (`CAP_SYS_ADMIN`,
  `CAP_NET_ADMIN`, `CAP_NET_RAW`, `CAP_SYS_RESOURCE`, file-ownership caps),
  `NoNewPrivileges=no`, `ProtectSystem=full`, cgroup delegation and journald.
  An empty capability set or a non-root `User=` is refused.
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
  issue-certificate-first atomic consume, controller-derived TLS topology
  support/refusal, a signed public client certificate and three non-crossing
  authorization domains. No secret is stored or returned in a read response.
- `cmd/carbon-node` is the bounded preflight/doctor renderer. It reads an
  injected HostFacts document and can print the rendered config/unit to owned
  paths; it never probes or changes a live host, installs a daemon or writes a
  system unit. Enrollment fixtures use synthetic keys and temp-dir private
  material only.
- **Qualification limit:** the existing real dockerd evidence is a PRIVILEGED
  disposable nested **rootful** boot of the contributed daemon.json with a
  `json-file` log-driver override, plus `systemd-analyze verify` syntax checks
  of both rendered units. It does **not** prove that the shipped rootless
  per-user unit or the shipped rootful unit boots on a clean Linux systemd
  host. Those two unit boots remain unverified; do not claim deployability from
  the fixture evidence.

# Why

Parallel deployment modes share one enrollment/control contract. Keeping host
prerequisites, private-isolation gates and the local secret store in a
mode-specific package avoids forking the contract while making every
privilege decision explicit and testable without a live host.

# How to check

- `go test ./internal/noderuntime/... ./cmd/carbon-node` and `go vet` on the
  touched packages.
- The gate matrix and refusal codes are in
  `docs/plans/host-native-private-daemon.md`.
- `CheckOptionParity` fixtures fail on duplicate keys, extra CLI flags and
  CLI/config conflicts; `systemd-analyze verify` only checks unit syntax.
- Disposable dind boot evidence is in the external audit file
  `security-runtime-plans/node-native-boot-evidence.md`. It is a privileged
  nested rootful config boot with `json-file` override, **not** a boot of the
  shipped rootless per-user unit or the shipped rootful unit on a clean systemd
  host. Those remain unverified. No live-host, install, gameplay or deployment
  success may be claimed from fixtures.

# Read next

- [Shared enrollment boundary](shared-enrollment-boundary.md)
- [Node control protocol](node-control-protocol.md)
- [ADR 0019](../../docs/adr/0019-node-control-contract.md)
