---
type: Workflow
title: "Release — Rules (part 3)"
description: "the existing edge image is not a fresh nightly."
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: release.md
---
  the existing `edge` image is not a fresh nightly.
- Managed node-runtime artifacts are a separate publication lane, not an
  application release. `runtime-artifacts.yml` is dispatch-only, uses the
  independent `runtime-vMAJOR.MINOR.PATCH` domain, and requires trusted-main
  SHA, exact-commit main CI and a bound identity assertion before any write.
  It produces checksums, a cosign keyless signature and a CycloneDX SBOM for a
  draft asset release; it never pushes a registry image, publishes an app
  release or provisions a host. Only formats with real clean import/boot
  evidence are publishable; see the format decision concept.
- The upgrade rehearsal is a separate read-only CI job, not a publication gate.
  `upgrade-rehearsal.yml` runs weekly, on path-gated PRs and on dispatch, with
  two fail-closed cases on seeded disposable DB/world copies using real
  `carbon-panel` binaries: the explicit v1.0.40 legacy baseline (pinned 35-table
  stable and rollback) and the current published stable derived from the
  authoritative release record via the OSS-395 contract (no hardcoded literal).
  Both derive the candidate count from the real candidate and require the
  historical model, archive-import table and replay receipt columns. They
  assert integrity, additive growth, row/world preservation, forced-failure
  refusal and both rollback paths; rollback restores the exact Stable table set
  and removes only tables observed in the candidate delta. Disposable artifacts
  stay under `$RUNNER_TEMP` (build caches excepted); a missing fixture or failed
  check fails closed with no silent skip and still writes a failing evidence
  report. It grants no write scope and never touches a registry, tag, release,
  host or live world; see ADR 0044 (not a substitute for OIDC/PROXY or OSS-366 gates).
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
