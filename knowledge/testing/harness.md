---
type: Architecture
title: Testing seams
description: How failure and recovery behavior is tested without a live Minecraft server or a reachable Docker daemon.
status: stable
owners: [oss-runtime]
code_refs: [internal/docker, internal/db, .github/workflows/ci.yml, Makefile, .gitignore, .dockerignore, scripts/test-ignore-policies.py, internal/proxy/listeners_test.go, internal/rpc/services/server_docker_privilege_test.go, internal/rpc/services/server_port_test.go, docs/testing/windows-verification.md, scripts/windows-verification-evidence.ps1, scripts/test-windows-verification-evidence.ps1, web/carbon-panel/test/dom-setup.mjs, web/carbon-panel/src/lib/components/carbon/a11y-dom.test.ts, scripts/manual-review/e2e/a11y-check.mjs]
sources:
  - title: Contributor guide
    resource: ../../AGENTS.md
---

# Parts

- [Testing seams — Shape + Use it when](harness-shape.md) — The ignore-policy check uses a disposable Git repository and synthetic canaries.
- [Testing seams — Rules + Read next](harness-rules.md) — Proto generation tests exercise the real entrypoint with a failing generator
