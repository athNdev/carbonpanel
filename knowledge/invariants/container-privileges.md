---
type: Invariant
title: Container privileges
description: Container hardening must stay the baseline; privileges are broadened only through a documented, tested operator override.
status: stable
owners: [oss-runtime]
code_refs: [internal/docker, pkg/runtimeprofile, docker]
sources:
  - title: Contributor guide
    resource: ../../AGENTS.md
verified: { by: agent:opencode, at: 2026-11-19 }
review_after: 2027-01-23
---

# Parts

- [Container privileges — Rule + Why + How to check](container-privileges-rule.md) — Do not add privileged, broad capabilities, host mounts or a writable Docker
- [Container privileges — Review](container-privileges-review.md) — Reviewed 2026-10-24 by agent:mimo-v2.6-pro against the code and attested by
- [Container privileges — Review (part 2)](container-privileges-review-2.md) — Re-verified 2026-11-17 by agent:cursor: the same preflight still runs after
