---
type: Workflow
title: Server portability archive acceptance rehearsal
description: Hosted exact-head backend ZIP and tar.gz RPC round trip through a second Docker engine, including preview, import hash checks and Minecraft readiness.
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows/server-archive-rehearsal.yml
  - scripts/qualify-server-archive-hosted.sh
  - scripts/server_archive_rehearsal_ci.py
  - scripts/qualify-archive-player.mjs
  - scripts/test_server_archive_rehearsal_ci.py
sources:
  - title: Server portability archive format
    resource: ../../docs/server-portability.md
  - title: Architecture decisions — Disposable server-portability acceptance rehearsal
    resource: ../../docs/adr/0057-disposable-server-archive-acceptance.md
---

Run the `Server archive acceptance rehearsal` workflow at the exact candidate
head. It is CI-only for repository-owned pull requests and explicit dispatch:
the wrapper refuses to run outside a GitHub-hosted job and outside a fresh
`$RUNNER_TEMP` subdirectory, and it never publishes, deploys or contacts a live
panel, world or registry.

The job checks out the same PR head SHA it exports as `STAGED_SHA` and builds a
controller with an empty frontend embed. A second privileged Docker-in-Docker
engine runs on an owned bridge with a separate `/storage` mount, no host
networking and no published management port; its digest is pinned from primary
Docker Hub metadata. The controller reaches the non-loopback bridge address,
which also probes nested game ports.

The Python harness requires the hosted caller, a fresh `RUNNER_TEMP`
subdirectory and an exclusively created fixture record tying the clean exact
checkout to both containers. It verifies their identity, bridge, storage
mounts and the controller's `-config` directory before writes. It creates a managed
Vanilla source without overrides, verifies the running-export refusal, saves
the fixture, exports both formats and checks destination hashes before start.
The pinned protocol client verifies both imported servers can be joined.

Every check fails closed. The report records the backend RPC scope, candidate
commit, pinned digest, archive hashes, capacities, destination verification and
readiness. The wrapper gates diagnostics and removal on resource IDs and the
run label. Cleanup checks the run's servers are gone. A failed teardown fails
the hosted job and is recorded alongside the RPC report.

# Rules

- This proves the backend archive RPC round trip and Minecraft readiness, not a
  production host, ingress, proxy routing, OIDC, published release or live
  deployment. State those limits in the PR receipt rather than upgrading them.
- OSS-428 browser acceptance belongs to #686; this job uses RPC only, not UI.
- Keep the engine off host networking on an owned bridge with no published
  management port. A loopback Docker endpoint is invalid because the controller
  classifies loopback as its own local engine.
- Do not replace the exact-head build, pinned client or formats with fixtures.
  Fake-Docker Go tests are not acceptance evidence, and offline tests must never
  record a round trip as verified.
- Keep the fixture deterministic; never add a real secret or live endpoint.
