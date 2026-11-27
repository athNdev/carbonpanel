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
head. It is CI-only for repository PRs and explicit dispatch, refuses anything
but a GitHub-hosted job and fresh `$RUNNER_TEMP`, and never publishes/deploys.

The job checks out the PR head it exports as `STAGED_SHA` and builds a controller
with an empty frontend embed. A second privileged Docker-in-Docker engine runs
on an owned bridge with separate `/storage`, no host networking and no published
management port; its digest is pinned from Docker Hub metadata. The controller
reaches the non-loopback bridge address, which also probes nested game ports.

The Python harness requires the hosted caller, a fresh `RUNNER_TEMP` and an
exclusive fixture record tying the clean checkout to both containers. It checks
identity, bridge, exact storage binds and the controller's `-config` directory.
It creates a managed Vanilla source, verifies the game and panel share the world
path and confirms the running export refusal. Then it saves, exports both
formats and checks every source and destination byte. Minecraft-owned world
files are hashed through server-scoped `FileService` reads, streaming above the
editor cap. Mounted metadata independently checks the bounded inventory and
rejects links. The pinned protocol client verifies both imports can be joined.

Every check fails closed. The report records the RPC scope, candidate commit,
pinned digest, archive hashes, capacities, destination verification and
readiness. Cleanup gates on resource IDs and the run label and fails the job on
any surviving server or failed teardown.

# Rules

- This proves the backend archive RPC round trip and Minecraft readiness, not a
  production host, ingress, proxy routing, OIDC, release or live deployment.
- OSS-428 browser acceptance belongs to #686; this job uses RPC only, not UI.
- Keep the engine off host networking on an owned bridge with no published
  management port; loopback Docker is classified as the controller's own engine.
- Keep the host bind and destination-ownership proofs; never chmod, chown, run
  as root or weaken the engine to read Minecraft-owned files. Hash their bytes
  through scoped `FileService` reads with independent size and digest checks.
- Do not replace the exact-head build, pinned client or formats with fixtures;
  fake-Docker Go tests are not acceptance evidence and offline tests must never
  record a round trip as verified.
- Keep the fixture deterministic; never add a real secret or live endpoint.
