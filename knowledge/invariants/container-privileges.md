---
type: Invariant
title: Container privileges
description: Container hardening must stay the baseline; privileges are broadened only through a documented, tested operator override.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/docker
  - docker
sources:
  - title: Contributor guide
    resource: ../../AGENTS.md
verified: { by: agent:zed, at: 2026-11-04 }
review_after: 2027-01-23
---

# Rule

Do not add `privileged`, broad capabilities, host mounts or a writable Docker
socket to a game container outside an explicitly documented and tested operator
override. The default hardening must remain the baseline, and any change must
test the effective configuration after overrides are applied.

# Why

Each Minecraft server is an isolation boundary. A container that can reach the
host or the Docker socket can escape that boundary.

# How to check

- Inspect the effective config after overrides, not only the defaults.
  `docker.PreflightRuntime` runs on the resolved request in
  `Client.CreateContainer` and refuses a container that cannot boot before the
  daemon is asked to create anything.
- A new mount needs a path and a reason; a new capability needs a named
  requirement.
- Do not copy a hardening exemption from one image to another.

# Review

Reviewed `2026-10-24` by `agent:mimo-v2.6-pro` against the code and attested by
`human:athNdev`:

- `applyContainerHardening` in `internal/docker/client.go` sets `CapDrop ALL`,
  the Minecraft bootstrap capabilities and `no-new-privileges`.
- `ApplyOverrides` applies operator `privileged`, `CapAdd`, `CapDrop`,
  `SecurityOpt` and mount overrides without validation, so this rule is a
  change policy, not a runtime bound.

A validation bound on operator overrides is a known gap and would strengthen
this invariant; it is recorded as accepted risk rather than a merge blocker.

Re-verified `2026-11-04` by `agent:zed`:

- `docker.PreflightRuntime` inspects the resolved `container.Config` and
  `container.HostConfig` after `ApplyOverrides` and `applyServerRootfs`, so a
  stored row from an older panel or another writer is checked too. Fatal
  findings stop creation; warnings are logged with a named code and remedy.
- The preflight also names a read-only /data mount, a missing /data mount,
  host networking, a host device mapping, bootstrap capabilities removed by
  `CapDrop ALL`, a runtime uid that cannot own the world directory, and a
  rootless daemon paired with host-root-owned data during the root-then-drop
  bootstrap (`rootless_host_root_data`).
- It reports the applied memory, swap, pids and writable-layer limits, and
  warns when a requested limit is absent. A writable-layer cap is applied only
  on a writable root, because a read-only root cannot grow.
- Broadening confinement (privileged, `CapAdd`, a writable rootfs) is still
  permission-gated rather than refused, and is reported as a warning.
