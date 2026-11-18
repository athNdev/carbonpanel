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
  - title: Publication boundary and version domains
    resource: ../../docs/adr/0033-release-tag-and-publication-policy.md
---

# Steps

1. Update `CHANGELOG.md` and any user-facing docs affected by the change.
2. Run the full verification from [testing](../testing/verification.md).
3. Build the frontend with `bash scripts/web-build.sh`.
4. Let CI build the backend, Windows compile target, frontend checks, proto
   lint and images.
5. Tag only after CI is green. A tag stages, builds and verifies; it does not
   publish. Publication is a separate operator action: run Carbon Panel Release
   with `publish=true`, or dispatch Module Builder for module images. Do not
   merge or deploy from an agent session; that is the supervisor's decision.

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

- Publication is an explicit opt-in enforced by `publication_allowed()` in
  `scripts/release.py`. Branch pushes, tag pushes, PRs, CI, documentation and
  rehearsal tags only stage, build and verify; no registry or release write.
  `workflow_dispatch` with `publish=true` is the only panel publication path.
  Staging a tag makes no claim about its target's age: tags can point at old
  commits and tag-triggered runs reflect that history. The dispatch revision is
  a convenience, not the trusted gate; the Python boundary plus exact-commit
  main CI are authoritative.
- Publication is phased. A read-only preflight classifies each registry
  reference as absent, present or unknown; conflicts, unknown errors and partial
  states refuse. The immutable full-version tag is pushed and verified
  (tag→digest plus both Linux architectures in both registries) before any
  alias moves. Aliases are promoted in separate, serialized, re-verified steps,
  monotonically against published stable SemVer: no moving backwards, no
  promotion from older versions, no `latest` from prereleases. An alias whose
  ordering or immutability cannot be established is omitted with a recorded
  reason rather than guessed. RESUME requires more than identical tag digests:
  an operator-supplied verified digest, exact tag-to-commit identity and a
  release manifest whose version, commit, revision and digest all match;
  unknown or mismatched provenance refuses. Module images use the same split and
  the exact `mod-vMAJOR.MINOR.PATCH` ref; build uses `git show-ref --verify`,
  never a glob existence check. The durable release record completes before any
  alias moves; each move re-checks the actual registry alias digest against
  durable digest-to-version evidence and refuses on uncertainty. Main `edge` and
  Pages effects use the same gate: an ordinary verified merge builds and
  validates only; publication requires a trusted `refs/heads/main` dispatch with
  the channel opt-in and a read-only exact-commit main-CI check (`verify-main-ci`)
  for a PINNED commit. Manual dispatch checks out the event SHA and workflow_run
  checks out the CI run's head SHA; full history is fetched for ancestry and a
  step asserts the actual HEAD equals that SHA before any side-effecting step.
  Tags, labels and provenance use the same SHA, never a moving branch tip.
  Verification uses per-SHA cancellable concurrency groups; opted-in
  publication uses a fixed channel-wide non-cancelling group (`cd-publish-edge`
  / `pages-deploy`) with no SHA, so publishes of different commits serialize on
  the moving alias and routine builds cannot cancel an in-flight write. An
  operator checkbox is never CI evidence. IndexNow notification is deferred
  because URLs come from a push diff while deployment is dispatch-only. A scoped
  publish-boundary opt-in needs release-auditor review before held merges.
- Human-readable `build-YYYY-MM-DD-HHMMSS` aliases are immutable additions at
  exact existing digests, not releases or channels, and carry no SHA fragment.
  They are recomputed from the checked-in mapping by
  `scripts/docker_readable_aliases.py` using observed Hub tag publication time
  (`tag_last_pushed`, fallback `last_updated`; a proxy, not build time) and never
  move `latest`, a minor tag, `edge`, `nightly` or an original `sha-*` ref. Only
  an explicit trusted-main dispatch may add them, using imagetools create with
  strict observation and idempotence; see ADR 0034.
- `nightly` is a development alias, not a release version: it points to the most
  recent qualifying build of a pinned main commit with exact successful CI, and
  reuses the verified `sha-<full-commit>` image rather than adding a date alias.
  Both edge and nightly writers preflight that shared sha tag before any build
  or push: strict lookup classification treats only a definitive manifest-unknown
  as absent, and a missing tag with an expected digest refuses. Reuse needs
  independent pinned-build evidence — exact successful CI for the commit, digest
  match, both platform child manifests and matching `org.opencontainers.image.revision`
  in each digest-addressed child config (not descriptor annotations); a bare
  expected digest is not evidence. Every evidence fetch is anchored to the
  observed digest (`repo@digest`) and the reported index digest must match or
  publication refuses with zero writes. Not-found classification is scoped to
  the queried reference. Edge uses separate previous binding inputs. Nightly and edge promotion bind the previous
  commit to the observed alias digest through that same evidence before checking
  ancestry. Buildx writes only the sha reference; an explicitly gated digest-based
  step moves edge so it survives sha reuse in either order. Nightly never moves
  `latest` or a minor tag, and no scheduled registry write is enabled. Seeding a
  live nightly is separately authorized; the existing `edge` image is not a fresh
  nightly.
- A release note must describe shipped behavior, not planned behavior.
- Buf CLI 1.50.0 is the default in Make, container and publication workflows;
  generator plugin versions are pinned separately in `buf.gen.yaml`. The
  [development guide](../../docs/development.md) describes explicit overrides
  and the staged generation check.
- `toolchain-versions.json` records tested Go, Bun and Buf defaults. Update its
  checked consumers together and run the policy gate, staged generation check
  and applicable image builds before release; version tags are not digests.
- Do not publish an image that was built from a dirty or unverified tree.
- The controller image is published to Docker Hub as
  `atharvnaphade/carbonpanel`. CD's permission probe, image tags, tagged release
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

# Read next

- [Verification](../testing/verification.md)
- [Repository map](../repo-map.md)
