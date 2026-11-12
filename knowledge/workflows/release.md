---
type: Workflow
title: Release
description: The checks and artifacts required before a tagged Carbon Panel release.
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows
  - Makefile
  - CHANGELOG.md
sources:
  - title: Continuous integration
    resource: ../../.github/workflows/ci.yml
---

# Steps

1. Update `CHANGELOG.md` and any user-facing docs affected by the change.
2. Run the full verification from [testing](../testing/verification.md).
3. Build the frontend with `bash scripts/web-build.sh`.
4. Let CI build the backend, Windows compile target, frontend checks, proto
   lint and images.
5. Tag only after CI is green. Do not merge or deploy from an agent session;
   that is the supervisor's decision.

# Rules

- A release note must describe shipped behavior, not planned behavior.
- Do not publish an image that was built from a dirty or unverified tree.
- If a check cannot run, say so; do not describe it as passed.
- Changes to module image inputs also run the build-only `build-inputs.yml`
  workflow for Geyser and status on Linux amd64/arm64. PR checks do not log in
  to a registry or publish images; module-tag publication remains separate.
- Validation retains the full lint debt and structured vulnerability scan
  reports alongside the gates; incomplete scans fail. See
  [verification](../testing/verification.md) for the policy and pinned tools.
- Editor changes also run `editor-dialog.yml`, which renders the actual dialog
  in Chromium with deferred RPC responses. Session adapter tests alone cannot
  prove Svelte initialization, props timing or Monaco cleanup behavior.

# Read next

- [Verification](../testing/verification.md)
- [Repository map](../repo-map.md)
