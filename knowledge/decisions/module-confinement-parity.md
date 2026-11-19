---
type: Decision
title: Module sidecar confinement parity
description: Module sidecars inherit the game-container HostConfig baseline; one named, permission-gated profile is the only relaxation.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/docker/module.go
  - internal/rpc/services/module.go
sources:
  - title: Contributor guide
    resource: ../../AGENTS.md
---

# Choice

`applyModuleConfinement` sets `Privileged=false`, `CapDrop ALL`,
`no-new-privileges:true`, a 512 pids limit, a zero-swap memory limit
(`MemorySwap == Memory`) and a read-only rootfs with a writable `/tmp` tmpfs on
every module container after resource setup and before create. A template may
declare exactly one named exception through
`metadata["security_profile"] = "host-extension"`. That profile may request
capabilities from a fixed allowlist and, with an explicit writable-root flag, a
writable rootfs; it still keeps CapDrop ALL, no-new-privileges, pids and
zero-swap, and never enables `Privileged` or mounts the Docker socket.
`ModuleService.CreateModule` grants the exception only to callers holding
`servers/manage_docker_privileged`; missing identity, missing enforcer, denied
decision or enforcer error all fail closed.

# Alternatives

Leaving sidecars at `Privileged=false` alone lets a module template inherit a
weaker isolation boundary than its game server. Silently auto-enabling
privileged mode to recover from a failed start hides the exception and bypasses
audit. An arbitrary metadata-driven HostConfig merge would turn a UI/template
edit into a host privilege grant without a permission check.

# Consequence

Effective-config tests must assert the resolved HostConfig after defaults and
overrides, not just the template fields. New named profiles need an explicit
allowlist entry and the same RPC permission gate; unknown profile names fall
back to the hardened baseline. This is an in-process permission decision, not a
cross-process Docker daemon guarantee; the daemon still owns enforcement.
