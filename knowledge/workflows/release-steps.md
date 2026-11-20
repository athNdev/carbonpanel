---
type: Workflow
title: "Release — Steps"
description: "Update CHANGELOG.md and any user-facing docs affected by the change."
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: release.md
---
# Steps

1. Update `CHANGELOG.md` and any user-facing docs affected by the change.
2. Run the full verification from [testing](../testing/verification.md).
3. Build the frontend with `bash scripts/web-build.sh`.
4. Let CI build the backend, Windows compile target, frontend checks, proto
   lint and images.
5. Tag a product release only as `carbonpanel-vMAJOR.MINOR.PATCH[-alpha.N|-beta.N|-rc.N]`
   after CI is green. Plain `v*`, `mod-*`, `docs*` and inherited tags are
   historical refs, not panel releases. A tag stages, builds and verifies; it
   does not publish. Publication is a separate operator action: run Carbon Panel
   Release with `publish=true`, a canonical product tag, a trusted main ref and
   exact-commit CI, or dispatch Module Builder for module images. Do not merge
   or deploy from an agent session; that is the supervisor's decision.

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
