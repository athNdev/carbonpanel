---
type: Workflow
title: Verification
description: The commands that define a verified change in this repository.
status: stable
owners: [oss-runtime]
code_refs: [Makefile, .github/workflows/ci.yml, cmd/govulncheck-policy, scripts/check_lint_report.py]
sources:
  - title: Contributor guide
    resource: ../../AGENTS.md
  - title: Validation policy and reports
    resource: ../../docs/testing/validation.md
---

# Parts

- [Verification — Backend + MCP coverage gate + Frontend + Production frontend build](verification-backend.md) — make test is the same suite. CI also compiles every package for Windows,
- [Verification — Landing page + Proto + Knowledge + Local note](verification-landing-page.md) — python3 -m unittest scripts.test_landing_page
