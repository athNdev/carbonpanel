---
type: Architecture
title: "Testing seams — Rules + Read next"
description: "Proto generation tests exercise the real entrypoint with a failing generator"
status: stable
owners: [oss-runtime]
code_refs: [internal/docker, internal/db, .github/workflows/ci.yml, Makefile, .gitignore, .dockerignore, scripts/test-ignore-policies.py, internal/proxy/listeners_test.go, internal/rpc/services/server_docker_privilege_test.go, internal/rpc/services/server_port_test.go, docs/testing/windows-verification.md, scripts/windows-verification-evidence.ps1, scripts/test-windows-verification-evidence.ps1, web/carbon-panel/test/dom-setup.mjs, web/carbon-panel/src/lib/components/carbon/a11y-dom.test.ts, scripts/manual-review/e2e/a11y-check.mjs]
sources:
  - title: Parent concept
    resource: harness.md
---
# Rules

- Proto generation tests exercise the real entrypoint with a failing generator
  and require every previous output to survive. The native comparison command
  verifies generated Go and TypeScript after staging and formatting.

- Test the failure path at the closest integration seam, not only the happy
  path.
- Do not require a live Minecraft server for a unit-level failure test.
- Native network fixtures use loopback-only, instance-owned socket adapters and
  ephemeral ports. Cover indirect manager/module starts as well as direct
  listeners. A wildcard regression must fail before calling the OS, not after
  opening a public port and triggering consent for a temporary test executable.
- Docker override authorization fixtures supply available ports through the
  service instance's port seam, so unrelated workstation listeners cannot mask
  permission, persistence or audit assertions. The host-port integration tests
  retain real loopback probes and verify rejection of an owned occupied port.
- A passing native suite is not proof of unattended execution: repeat uncached
  runs and check for Windows approval prompts/query rules as described in the
  [Windows verification procedure](../../docs/testing/windows-verification.md).
- Windows qualification records the test token, effective firewall policy,
  fresh executable paths, completed commands and continuous event observations.
  Missing proof is incomplete, while a failed command or attributed Query User
  event is a failed regression. Clean-profile acceptance additionally needs a
  genuinely new standard-user profile and a working positive control in a
  disposable environment. Pure evidence fixtures and elevated hosted-runner
  results do not establish that acceptance. The read-only evidence helper never
  changes host policy; UAC-disabled results retain their narrower firewall scope.
- If a failure cannot be reproduced in a test, say so in the change.
- Frontend accessibility is checked through public UI seams: Happy DOM component
  tests assert focus, roles and states, and the owned-Chromium journey checks
  focus order, Escape, visible focus and reflow at 360/768/1440 on both the
  component fixture and real routes served from the production static build,
  with the high-value routes checked in both light and dark themes. Route data
  is seeded only at the HTTP/RPC boundary; no source module or store
  is imported into the browser. Source-string assertions alone are not
  accessibility coverage; see
  [Frontend accessibility coverage](../decisions/frontend-a11y-coverage.md).
- The runtime qualification matrix in `internal/docker/runtime_matrix_test.go`
  always inspects the resolved CreateContainer request. Skip only the
  daemon-`/info` case when Docker is absent; game-readiness stays OSS-247.

# Read next

- [Verification](verification.md)
- [Scheduler](../architecture/scheduler.md)
