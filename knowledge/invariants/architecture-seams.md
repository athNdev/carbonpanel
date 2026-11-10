---
type: Invariant
title: Recorded architecture seams
description: The Locator, lifecycle handle, idle-power, PlanFor and worldstore seams stay named in DESIGN.md with live package paths.
status: stable
owners: [oss-runtime]
code_refs:
  - DESIGN.md
  - internal/docker/locator.go
  - internal/docker/transition.go
  - internal/docker/idle.go
  - internal/proxy/plan.go
  - internal/worldstore
  - internal/docscheck
sources:
  - title: Design specification
    resource: ../../DESIGN.md
verified: { by: agent:cursor-grok, at: 2026-11-10 }
review_after: 2027-02-10
---

# Rule

The five architecture seams from OSS-21 stay recorded in DESIGN.md §4.5 with
their package paths. Those paths must exist. Call sites must not grow a second
node-to-daemon resolver that returns `(nil, nil)` instead of a typed locator
error.

# Why

The epic acceptance criterion is that stable seams are recorded. Recording
without a CI check lets a rename or a leftover adapter drift silently.

# How to check

- `go test ./internal/docscheck` — DESIGN.md cites each seam path and each
  path exists.
- `go run ./cmd/knowledge-lint -root .` — this concept's `code_refs` and the
  retrieval query in `knowledge/queries.yaml` still resolve.
- `go test ./internal/docker -run TestLocatorCallersDoNotSwallowNoClient`

# Review

Reviewed `2026-11-10` by `agent:cursor-grok` against DESIGN.md §4.5, the five
ADRs, and the package paths on `origin/main`.

# Read next

- [Runtime lifecycle and world-data seams](../architecture/runtime-seams.md)
