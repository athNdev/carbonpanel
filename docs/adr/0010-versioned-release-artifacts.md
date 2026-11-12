# ADR 0010: Versioned release bundles and publication

Status: Accepted

## Context

The earlier tag workflow built archives and Docker images independently, lacked
download checksums, and did not embed the release version in the Go binaries.
It could publish a GitHub release while its Docker image was still failing.
ARM image builds also repeated the full frontend and Go compilation in QEMU.

## Decision

Use a SemVer `v` tag as the single release-version source. Build the web UI
once, compile and smoke-test the five supported native targets, and package
both controller and helper with configuration, licenses and build metadata.
Use the pure-Go SQLite implementation already selected by the service, with
`CGO_ENABLED=0`, so binaries do not depend on host SQLite or cross-compilers.

Assemble release Docker images from the exact tested Linux binaries. Only the
release workflow owns version tags, stable minor tags and `latest`. Main CD
owns development `edge` and commit tags. Prereleases do not move stable tags.

Require green main CI for the exact published source commit. Validate the full
archive set, checksums and published image architectures before exposing a
GitHub release. Uploaded assets are staged in a draft; published versions are
never overwritten by this workflow. A non-publishing rehearsal exercises the
same builds without registry or release writes.

## Consequences

Release preparation depends on native Linux ARM, macOS and Windows runners,
but avoids emulating compiler work. Unsigned portable archives remain the
distribution format; installers and code signing are separate future work.
The source commit timestamp is used for reproducible archive metadata and
build dates. See [release operations](../releases.md).
