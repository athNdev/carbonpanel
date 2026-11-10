---
type: Architecture
title: Testing seams
description: How failure and recovery behavior is tested without a live Minecraft server or a reachable Docker daemon.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/docker
  - internal/db
  - .github/workflows/ci.yml
  - Makefile
sources:
  - title: Contributor guide
    resource: ../../AGENTS.md
---

# Shape

Tests are ordinary Go package tests. Docker-facing code sits behind seams, for
example `ContainerFreezer` in `internal/docker`, so a test can substitute a
deterministic failure instead of a daemon. `internal/db` tests run against a
temporary test store rather than the controller's database file.

CI runs the suite, the race detector, and a Windows compile pass that builds
every package without executing the Windows binaries.

`internal/docscheck` is part of the suite. It resolves every code path cited by
`docs/plans/oss-feature-completeness-matrix.md` and fails when a cited path no
longer exists, when a feature id repeats, or when a row's status is not one the
legend defines. It also requires DESIGN.md §4.5 to cite the five architecture
seam packages (`locator.go`, `transition.go`, `idle.go`, `plan.go`,
`worldstore`) and fails if those paths disappear. A planning claim therefore
cannot outlive the code it names.

# Use it when

- A change touches recovery, retries, timeouts or partial failures.
- A Docker operation can fail after a side effect.
- A test needs a deterministic error instead of a real daemon.

# Rules

- Test the failure path at the closest integration seam, not only the happy
  path.
- Do not require a live Minecraft server for a unit-level failure test.
- If a failure cannot be reproduced in a test, say so in the change.
- The runtime qualification matrix in `internal/docker/runtime_matrix_test.go`
  always inspects the resolved CreateContainer request. Skip only the
  daemon-`/info` case when Docker is absent; game-readiness stays OSS-247.

# Read next

- [Verification](verification.md)
- [Scheduler](../architecture/scheduler.md)
