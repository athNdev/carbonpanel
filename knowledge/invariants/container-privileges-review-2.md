---
type: Invariant
title: "Container privileges — Review (part 2)"
description: "Re-verified 2026-11-17 by agent:cursor: the same preflight still runs after"
status: stable
owners: [oss-runtime]
code_refs: [internal/docker, pkg/runtimeprofile, docker]
verified: { by: agent:opencode, at: 2026-11-19 }
sources:
  - title: Parent concept
    resource: container-privileges.md
---
Re-verified `2026-11-17` by `agent:cursor`: the same preflight still runs after
overrides. The rule implementation moved to `pkg/runtimeprofile`, and
`internal/docker` calls it. Default hardening still uses that package's
bootstrap capability list.

Re-verified `2026-11-19` by `agent:opencode`: module sidecars now receive the same baseline as
game containers in their effective `HostConfig`: `Privileged=false`, `CapDrop
ALL`, `no-new-privileges:true`, pids limit 512, `MemorySwap == Memory` (zero
swap), and a read-only rootfs with a writable `/tmp` tmpfs. A template may opt
into exactly one named exception (`security_profile: host-extension`) to add
allowlisted capabilities and optionally a writable rootfs; it still never
enables `Privileged` and never mounts the Docker socket. The exception is
granted at `ModuleService.CreateModule` only to callers with
`servers/manage_docker_privileged`; an unknown profile, missing identity,
missing enforcer or denied decision fails closed. Effective-config tests assert
the resolved request after defaults and overrides. See
[Module sidecar confinement parity](../decisions/module-confinement-parity.md).
