---
type: Workflow
title: Final-candidate World Library swap qualification
description: Real running-JVM import, bind, rejoin and preserved-data proof on a staged final candidate through the hosted Compose harness.
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows/compose-release-qualification.yml
  - scripts/qualify-compose-artifacts.sh
  - scripts/qualify-compose-release.sh
  - scripts/qualify-world-swap.mjs
sources:
  - title: Deployment modes
    resource: ../operations/deployment-modes.md
  - title: World Library identity and binding
    resource: ../../docs/adr/0016-world-library-identity-and-binding.md
---

Dispatch Compose release qualification with `world_swap: true` only on the
final candidate after the three World Library ancestry commits are present.
The harness verifies the exact stage identity before building anything: it
accepts the older `release-image-context` artifact or a newer `publish=false`
stage retaining `qualified-stable-stage` and `verified-release-oci`, checks
`stage.json.commit` against the staged SHA, and verifies the OCI index blob
named by `stage.json.digest`. A missing or ambiguous artifact refuses the job;
never fall back to a different run or an unstaged image.

The optional phase reuses the live candidate JVM from the upgrade leg. It
packages a distinct disposable world from a second server, imports two
revisions through `UploadService` and `ImportWorld`, previews compatibility,
binds the first revision to the running level, and rejoins an actual
Minecraft client. It then asserts the new world marker, retained original
`level.dat` bytes, the exact revision binding, exactly one `world.bind`
success event for the request key, and refusal of a replay with that key and
a different revision. The second revision exists only so the replay cannot
hide behind an identical revision id.

This is evidence for the release matrix, not a production operation. The
fixture uses disposable hosted data and a loopback registry; the Stable lead
alone publishes after all gates pass.
