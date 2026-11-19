---
type: Failure Mode
title: Known gaps
description: Cross-cutting gaps that are not defects to rush, with the live tracker as the authority.
status: stable
owners: [oss-runtime]
code_refs: [internal/docker/freezer.go, cmd/carbon-panel, internal/nodestorage, internal/docker/runtime_profile.go, pkg/runtimeprofile, pkg/files, internal/s3]
sources:
  - title: Reliability plan
    resource: ../../docs/plans/reliability-hardening.md
---

# Parts

- [Known gaps — Current gaps + How to use this](known-gaps-current-gaps.md) — A bind-mounted world is not quota-limited by Docker. storage.container_writable_mb
- [Known gaps — Read next](known-gaps-read-next.md) — Detailed guidance on read next.
