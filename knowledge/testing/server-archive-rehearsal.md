---
type: Workflow
title: Server portability archive acceptance rehearsal
description: Hosted exact-head RPC and production-UI ZIP/tar.gz export/import rehearsals through a second Docker engine, including preview, destination hashes, stopped imports and browser evidence.
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows/server-archive-rehearsal.yml
  - scripts/qualify-server-archive-hosted.sh
  - scripts/server_archive_rehearsal_ci.py
  - scripts/qualify-archive-ui-hosted.mjs
sources:
  - title: Server portability archive format
    resource: ../../docs/server-portability.md
---

Run the `Server archive acceptance rehearsal` workflow at the exact candidate
head. Its `rehearsal` job is the RPC round trip with an empty frontend embed; its
`ui` job builds the exact production frontend and drives the real export and
create-from-archive routes. Both are CI-only for repository PRs and explicit
dispatch, refuse anything but a GitHub-hosted job and fresh `$RUNNER_TEMP`, and
never publish or deploy; [ADR 0057](../../docs/adr/0057-disposable-server-archive-acceptance.md)
records the full contract.

Each job checks out the PR head it exports as `STAGED_SHA`, shares
`scripts/qualify-server-archive-hosted.sh`, and starts a second privileged
Docker-in-Docker engine on an owned bridge with separate `/storage`, no host
networking and no published management port; its digest is pinned from Docker
Hub metadata. The controller reaches the non-loopback bridge address, which also
probes nested game ports. The Python harness requires the hosted caller, a fresh
`RUNNER_TEMP` and an exclusive fixture record tying the clean checkout to both
containers.

The RPC job creates a managed Vanilla source, confirms the running export
refusal, saves, exports both formats, checks source and destination bytes, and
starts both stopped imports to verify readiness with the pinned client.

The UI job runs `scripts/qualify-archive-ui-hosted.mjs`: refuse export while the
source runs (warning visible, both controls disabled, no `ExportServerArchive`
call), download ZIP and tar.gz through the real `/servers/[id]` panel with its
disclosure and `HEAD` preflight, then upload each download through the real
`/servers/new` archive wizard onto the remote node. Create navigates to `/servers/<id>`;
the driver records and persists that id before screenshots or theme work, so a later failure
still leaves it in the partial import record for ownership-gated cleanup. The Python harness
re-verifies each download, hashes destination bytes through `FileService`, and requires the
engine stopped; the UI job does not start imports and boot/join stays with the RPC receipt.

# Rules

- This proves archive RPC and production-UI transport paths, not a production
  host, ingress, OIDC, release or live deployment; a UI pass never replaces the
  RPC boot/join receipt, and a fixture-only browser pass is not acceptance.
- Pass browser credentials only in the child environment, never argv or reports.
- Keep the engine on an owned bridge with no host networking or published
  management port; loopback Docker is the controller's own engine.
- Keep the host bind and destination-ownership proofs; hash Minecraft-owned
  bytes through scoped `FileService` reads, never chmod/chown or run as root.
- Do not replace the exact-head build, pinned client or formats with fixtures;
  fake-Docker and offline tests must never record a round trip as verified.
- Keep the fixture deterministic; never add a real secret or live endpoint.
