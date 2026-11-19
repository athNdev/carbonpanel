---
type: Invariant
title: "Container privileges — Rule + Why + How to check"
description: "Do not add privileged, broad capabilities, host mounts or a writable Docker"
status: stable
owners: [oss-runtime]
code_refs: [internal/docker, pkg/runtimeprofile, docker]
verified: { by: agent:opencode, at: 2026-11-19 }
sources:
  - title: Parent concept
    resource: container-privileges.md
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
  daemon is asked to create anything. The checks themselves are
  `pkg/runtimeprofile`, which has no panel database, handler, or protobuf types.
- A new mount needs a path and a reason; a new capability needs a named
  requirement.
- Do not copy a hardening exemption from one image to another.
