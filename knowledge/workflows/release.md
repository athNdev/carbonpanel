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
  - scripts/release.py
  - scripts/go_mod_download.py
  - internal/buildinfo
  - docker/Dockerfile.release
  - docker/Dockerfile.carbon-panel
sources:
  - title: Continuous integration
    resource: ../../.github/workflows/ci.yml
  - title: Release operations and versioning
    resource: ../../docs/releases.md
  - title: Release artifact decision
    resource: ../../docs/adr/0010-versioned-release-artifacts.md
---

# Steps

1. Update `CHANGELOG.md` and any user-facing docs affected by the change.
2. Run the full verification from [testing](../testing/verification.md).
3. Build the frontend with `bash scripts/web-build.sh`.
4. Let CI build the backend, Windows compile target, frontend checks, proto
   lint and images.
5. Tag only after CI is green. Do not merge or deploy from an agent session;
   that is the supervisor's decision.

The release workflow supports a build-only rehearsal. Publishing requires a
SemVer version, a main-ancestor commit with successful exact-commit main CI,
and a version that has not already been published. It builds five native
archives with an embedded, identically versioned UI, controller, storage helper,
example configuration, license files and build metadata. The pure-Go SQLite
driver permits `CGO_ENABLED=0`. Both CLIs expose metadata through `--version`
before configuration or data access.
The Changelog page shows the installed UI version. Its bundled markdown stays
byte-for-byte synchronized with the root `CHANGELOG.md`.
Release builds use the checked-in RPC sources verified by CI. OpenAPI generation
writes a separate build artifact and never overwrites tracked source files.

The release image is assembled from the same tested Linux binaries. The full
bundle set, hashes and both registry architectures must be ready before the
draft release is published. A completed release is not overwritten. A draft
can be retried; a public fix receives a new version. See release operations
for exact artifact names, native runner targets and checksum verification.
Draft verification uses the release-by-ID API resolved by the CLI; GitHub's
by-tag REST lookup omits unpublished drafts. A final-publication failure can
resume from the original verified download artifact without rebuilding images.

# Rules

- A release note must describe shipped behavior, not planned behavior.
- Buf CLI 1.50.0 is the default in Make, container and publication workflows;
  generator plugin versions are pinned separately in `buf.gen.yaml`. The
  [development guide](../../docs/development.md) describes explicit overrides
  and the staged generation check.
- Do not publish an image that was built from a dirty or unverified tree.
- The controller image is published to Docker Hub as
  `atharvnaphade/carbon-panel`. CD's permission probe, image tags, tagged release
  metadata and the local publishing script must agree on that destination.
  The GitHub owner name is not a Docker Hub namespace mapping.
- The release workflow alone publishes versioned images. Stable releases own
  `latest` and the matching minor tag; prereleases do not change either.
  Main CD publishes development `edge` and commit tags. It does not handle tag
  pushes, so one version cannot launch two competing image publishers.
- Versioned releases also publish the same image to GitHub's container registry,
  under the lowercased GitHub repository name. The source label links the package
  to the repository; its first publication needs public visibility set in Package
  settings. Both registries' architectures are verified before release publication.
- CD serialization belongs to its gated publishing job, never the outer workflow.
  Skipped PR and failed-CI events must not cancel an active publish. Image commit
  tags and revision labels identify the tested checkout, not a later branch tip.
- Dependency fetches retry transport failures before race checks and native
  release builds. Retries are bounded; tests and race failures are never retried
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
- Development launcher checks use the pinned Go toolchain to build their native
  Windows supervisor and fixtures. Both Linux and Windows run the launcher tests;
  Windows also checks the qualification evidence evaluator and setup guards.
- Clean-profile Windows qualification is a separate manual workflow. Its
  disposable VM must prove a fresh standard-user profile and a working listener
  control before zero test events can be accepted. See the
  [evidence decision](../decisions/windows-clean-profile-evidence.md).

# Read next

- [Verification](../testing/verification.md)
- [Repository map](../repo-map.md)
