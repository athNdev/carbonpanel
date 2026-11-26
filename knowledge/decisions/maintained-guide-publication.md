---
type: Decision
title: "Maintained guide publication — Decision"
description: "Automatic docs-only publication of release-independent operational guides"
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows/docs-only-publish.yml
  - .github/workflows/landing.yml
  - scripts/docs_publication_gate.py
  - scripts/test_docs_publication_gate.py
sources:
  - title: Parent concept
    resource: website-docs.md
  - title: Runbook
    resource: ../../docs/website-publishing.md
---
# Decision

- Release-independent operational guides declared in `docs/maintained-guides.json`
  publish automatically on a successful trusted exact-main CI run through the
  focused docs-only publication workflow. They never move an image alias,
  tag, release or immutable product pin. The workflow joins the same
  non-cancelling `pages-deploy` lock as other publishers; ineligible CI events
  use a unique group so they cannot displace a queued real deployment.
  `NIGHTLY_RECOVERY_ACTIVE=true` holds automatic and manual website writes
  while the read-only guide and website checks remain available.
- The live Stable and Nightly product/image receipt is resolved and qualified
  before rendering and reproduced exactly. A promoted final channel uses the
  observed revision, digest and receipt; the explicitly unpromoted matched
  baseline keeps its contract. Public canonical pages and stamped guide
  provenance are verified on both routes before success.
- Both website publishers qualify Stable from matching public docs indexes,
  GitHub Latest, its peeled release tag and release-manifest digest, and the
  matching Linux amd64/arm64 OCI identity in both registries. The verified
  Stable tag, revision and digest are supplied to the builder and validator;
  the legacy render fallback requires exact equality to the verified public
  release. The guide receipt schema is unchanged.
- Liveness compares per-guide content digests, source paths and scopes from the
  exact Git blobs against the live per-channel guide receipt. A distinct
  trusted commit with identical maintained inputs is a safe no-op; a missing
  receipt publishes. Per-page source links and sha256 of actual built bytes are
  attested in a publication receipt and compared locally and publicly, with no
  hardcoded guide URLs.
- Rollback resolves the latest successful Pages deploy step in its linked
  Actions job, skipping environment successes whose deploy steps were all
  skipped. Use the artifact named by that step, including rollback artifacts,
  and validate live index, per-channel manifests and declared page bytes.
  Only when that artifact is no longer retained may a baseline be reconstructed
  from its exact deployment SHA confirmed on `origin/main` in a separate
  worktree with re-qualified live channel state; it is never claimed exact.
- See [Website and documentation publication](website-docs-decision.md) and the
  [website publishing runbook](../../docs/website-publishing.md).
