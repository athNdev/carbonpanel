---
type: Decision
title: Review stack data ownership
description: Review stack cleanup requires a scoped session path, matching marker and matching container label.
status: stable
owners: [oss-runtime]
code_refs:
  - scripts/review/devstack.sh
  - scripts/review/test-ownership.sh
  - scripts/manual-review/review.sh
  - scripts/manual-review/ownership-check.sh
  - .github/workflows/review-ownership.yml
sources:
  - title: Review harness
    resource: ../../scripts/review/README.md
---

# Choice

Single-stack review sessions own a direct named child under the user's review
cache. The path must resolve within the user home and match the session name;
unmarked directories are not adopted. A marker and matching Docker
label establish ownership before stopping or explicitly cleaning a session.
Ordinary down preserves its files. Listeners bind to loopback.

# Consequences

Old unmarked sessions require a deliberate migration; they are never silently
removed. New dual sessions require an equivalent revision-bound marker and
matching container labels; teardown checks both immutable container IDs before
removing either. The legacy dual destructive reset is unavailable and it
refuses to replace a mismatched worktree. This does not establish lifecycle or
gameplay acceptance.
