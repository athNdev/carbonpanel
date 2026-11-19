---
type: Invariant
title: "Container privileges — Review"
description: "Reviewed 2026-10-24 by agent:mimo-v2.6-pro against the code and attested by"
status: stable
owners: [oss-runtime]
code_refs: [internal/docker, pkg/runtimeprofile, docker]
verified: { by: agent:opencode, at: 2026-11-19 }
sources:
  - title: Parent concept
    resource: container-privileges.md
---
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
- `internal/docker/runtime_matrix_test.go` asserts the *effective* request
  after defaults and overrides. A fatal combination must stay refused.
  Qualification must not auto-enable privileged mode, disable
  no-new-privileges, or make the root writable to recover.
- Extra override binds that shadow `/etc`, `/proc`, `/sys`, `/dev`, or the
  Docker socket are refused at preflight (`sensitive_mount_target`), even when
  the host source sits under the server data directory. A bind that replaces
  `/tmp` is reported (`tmpfs_replaced`) and does not recover by widening the
  tmpfs.
- Module sidecars use the same rule: `applyModuleConfinement` never leaves
  `Privileged` true, and `ValidateModuleVolumeMounts` refuses `/var/run/docker.sock`.
  A host-extension template that needs those is an operator feature, not a
  builtin default.

Re-verified `2026-11-10` by `agent:zed` against the expanded runtime matrix.

