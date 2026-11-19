---
type: Architecture
title: "Testing seams — Shape + Use it when"
description: "The ignore-policy check uses a disposable Git repository and synthetic canaries."
status: stable
owners: [oss-runtime]
code_refs: [internal/docker, internal/db, .github/workflows/ci.yml, Makefile, .gitignore, .dockerignore, scripts/test-ignore-policies.py, internal/proxy/listeners_test.go, internal/rpc/services/server_docker_privilege_test.go, internal/rpc/services/server_port_test.go, docs/testing/windows-verification.md, scripts/windows-verification-evidence.ps1, scripts/test-windows-verification-evidence.ps1, web/carbon-panel/test/dom-setup.mjs, web/carbon-panel/src/lib/components/carbon/a11y-dom.test.ts, scripts/manual-review/e2e/a11y-check.mjs]
sources:
  - title: Parent concept
    resource: harness.md
---
# Shape

The ignore-policy check uses a disposable Git repository and synthetic canaries.
With `--docker` it compares real exported build contexts from a clean source
archive and a copy containing synthetic local state; it also checks required
controller, worker, protocol and backup-encryption inputs. It never deletes or
reads a developer's actual local data.

The check asserts that SvelteKit source routes ship in packaged builds while
saved-world data and credentials do not. The `/worlds` route re-include in
`.dockerignore` must stay above the general sensitive/persistent exclusions and
be limited to source extensions, so those later rules still win. Sentinel
canaries placed inside the route tree (credential, database, nested data)
verify that ordering; a broad trailing negation that leaks them fails the check.

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
