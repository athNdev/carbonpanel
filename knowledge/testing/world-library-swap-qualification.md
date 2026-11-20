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
  - title: Release staging artifact contract
    resource: ../decisions/release-staging-artifact-contract.md
  - title: World Library identity and binding
    resource: ../../docs/adr/0016-world-library-identity-and-binding.md
---

Dispatch Compose release qualification with `world_swap: true` only on the
final candidate after the three World Library ancestry commits are present.
The staging-artifact contract is owned by the release staging decision: the
same successful `publish=false` run retains `verified-release-oci` and the raw
`release-image-context` that the matrix builds from. The harness prefers that
raw context, byte-compares it with the release bundle, and refuses a missing or
ambiguous artifact. For earlier immutable stage runs that predate the restored
contract, it may reconstruct the context from the verified Linux bundle only
after checking `stage.json.commit` against the staged SHA and finding the OCI
index blob named by `stage.json.digest`; the sample config belongs at the
context root. Never fall back to a different run or an unstaged image.

Connect JSON serializes `int64` fields such as upload byte counts as decimal
strings. The harness normalizes those wire values with `BigInt` before comparing
them to Node buffer lengths, and `scripts/test-qualify-world-swap.mjs` covers a
serialized `GetUploadStatus` response so a strict-equality regression is caught
offline.

The optional phase reuses the live candidate JVM from the upgrade leg. It
packages a distinct disposable world from a second server, imports two
revisions through `UploadService` and `ImportWorld`, previews compatibility,
creates a supported pre-swap backup, binds the first revision to the running
level, and rejoins an actual Minecraft client. The binder stops and restarts
the existing owned container, so verify the restart with Docker's start time,
not a new container id. Prove preserved data through the pre-swap backup
record surviving unchanged: the binder retires its internal temporary old-world
tree after the journal commits and the live JVM may rewrite `level.dat`.
Failed-swap automatic rollback is covered by repository world-binding tests,
not by this hosted leg. The harness asserts the new world marker, exact
revision binding, exactly one `world.bind` event for the request key, and
refusal of a replay with that key and a different revision. The second revision
exists only so the replay cannot hide behind an identical revision id.

The Compose leg rebuilds the candidate image from verified release binaries and
labels that provenance explicitly; it does not claim to have exercised the
staged OCI image digest. Exact-image evidence is a separate release gate.

This is evidence for the release matrix, not a production operation. The
fixture uses disposable hosted data and a loopback registry; the Stable lead
alone publishes after all gates pass.
