---
type: Decision
title: Packwiz batch mutations report per-entry outcomes
description: BatchMods returns one result per requested slug; count is the number of successes, and per-entry failures stay visible on both transports.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/rpc/handlers/packwiz_ops.go
  - internal/rpc/handlers/packwiz.go
  - internal/rpc/services/packwiz.go
  - proto/carbonpanel/v1/packwiz.proto
sources:
  - title: Contributor guide
    resource: ../../AGENTS.md
---

# Choice

The shared `PackwizOperations.BatchMods` returns exactly one result for every
requested slug, in request order. Each result carries `slug`, `success`,
`message` and a stable `error_code`. A failure on one entry is recorded on that
entry and the remaining entries are still attempted, preserving the best-effort
behavior both transports already relied on. `count` is the number of successful
entries, not the number requested. The generated `BatchPackModsResponse` exposes
`repeated results` as an additive field, and the legacy `/mods/batch` REST
response exposes the same `results` array. An unrecognized action yields a
per-entry `invalid_action` failure rather than a silent success.

# Alternatives

The previous contract discarded every per-item error and returned
`len(slugs)`, so no wrapper could tell a full success from a total failure.
Failing the entire batch on the first error would discard partial progress and
hide which entries still succeeded. A top-level error list without a slug field
cannot be correlated with the request. Changing `count` to successes while
omitting `results` would fix the aggregate but not expose which entry failed.

# Consequence

Every caller, including an MCP wrapper, can report truthful partial outcomes.
Because `results` is additive and `count` only differs from the requested count
when an entry actually failed, well-behaved clients that only read `count` on
full success keep working. New batch mutation families must report per-entry
outcomes through this shared operation instead of returning a bare requested
count. This contract covers mutation outcomes only; it does not by itself prove
that a tool is reachable, authorized, or tested.
