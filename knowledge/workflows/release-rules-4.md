---
type: Workflow
title: "Release — Rules (part 4)"
description: "or reclassified as a successful check."
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: release.md
---
  or reclassified as a successful check.
- If a check cannot run, say so; do not describe it as passed.
- Changes to module image inputs also run the build-only `build-inputs.yml`
  workflow for Geyser and status on Linux amd64/arm64. PR checks do not log in
  to a registry or publish images; module-tag publication remains separate.
- Ignore policy changes run a Git sentinel matrix and compare actual clean and
  cluttered Docker contexts, including required worker and backup sources.
- Validation retains the full lint debt and structured vulnerability scan
  reports alongside the gates; incomplete scans fail. See
  [verification](../testing/verification.md) for the policy and pinned tools.
- Editor changes also run `editor-dialog.yml`, which renders the actual dialog
  in Chromium with deferred RPC responses. Session adapter tests alone cannot
  prove Svelte initialization, props timing or Monaco cleanup behavior.
  The same workflow builds a production editor fixture and checks both Monaco
  consumers plus HTTP serving of the emitted worker bundles.
  That workflow derives its owned browser cache from `RUNNER_TEMP` in a step
  and exports it through `GITHUB_ENV`; `runner` is not an available context in
  job-level `env`, which makes the whole workflow fail before any job is
  scheduled. `scripts/workflow-contract.test.mjs` guards this rule.
- Showcase review stacks use a marked per-session data directory and a matching
  container label before teardown. The selected port is inside that directory.
- The same browser workflow verifies a real local WebSocket reconnect and
  subscription history options in Chromium when frontend inputs change.
- Development launcher checks use the pinned Go toolchain to build their native
  Windows supervisor and fixtures. Both Linux and Windows run the launcher tests;
  Windows also checks the qualification evidence evaluator and setup guards.
- Clean-profile Windows qualification is a separate manual workflow. Its
  disposable VM must prove a fresh standard-user profile and a working listener
  control before zero test events can be accepted. See the
  [evidence decision](../decisions/windows-clean-profile-evidence.md).
