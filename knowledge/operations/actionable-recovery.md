---
type: Runbook
title: Actionable recovery paths
description: Safe next actions for permission, port, pull, stale node, partial restore or migration, and disk exhaustion without treating unknown as failed.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/rpc/services/server.go
  - internal/rpc/services/support.go
  - internal/health
  - internal/operation
  - docs/runbook-server-operations.md
sources:
  - title: Server operations runbook
    resource: ../../docs/runbook-server-operations.md
  - title: Troubleshooting
    resource: ../../docs/carbon-panel/src/content/docs/troubleshooting.md
  - title: Docker failure semantics
    resource: ../../docs/research/docker-failure-semantics-and-recovery.md
---

# Use this when

A high-risk mutation is stuck, a node looks down, a join probe is stale, or an
operator needs a next action that will not destroy a world.

# Rules

Unknown and stale stay distinct from failed and from success. Do not retry a
non-idempotent request with the same client `request_id`. Do not require the
Docker socket for ordinary diagnostics: use the journal, `observations.json`
in a support bundle, and `GetServer.join_observation_kind`.

# Paths

| Failure | Evidence | Safe next action |
| --- | --- | --- |
| Permission / mount | `failed_precondition`, `no write permission on the mount dir`, UID/GID 1000 cannot write `/data` | Fix host ownership (`chown`) or the volume mount. Do not enable privileged mode or make the root filesystem writable. |
| Port in use | create/start fails because the host port or an additional port is taken | Change the published port or release a lease left by detach (`-operations ports` / `release-port`). Do not delete the world. |
| Image pull | pull stream error, rate limit, missing digest, architecture mismatch | Keep the last runnable image and world. Retry pull as a new request after the registry/auth/disk issue is fixed. An inspect permission failure is not "image missing". |
| Stale node | node heartbeat older than the freshness window; `GetClient` cannot resolve the node | Treat the game layer as stale/unknown. Restore the node or its heartbeat. Do not fall back to the controller Docker socket. |
| Partial restore or migration | journal row `unknown` or `resolve_operation`; restore/migrate lock held | Confirm which node holds the world, then `-operations resolve` after a hand check. Container state does not release those locks. Do not start a second restore onto a live world. |
| Disk exhaustion | ENOSPC, quota, snapshot retention filling the host | Free backup/staging/image cache space, then retry with a new request id. Do not prune worlds or the panel database as a cleanup step. |

A GetServer reading of `unobserved` or `stale` is last-good evidence, not a
current player count. Playable requires a fresh successful SLP or RCON probe.

# Read next

- [Health and readiness](health-checks.md)
- [Server lifecycle journal](lifecycle-journal.md)
- [Backup and restore](backup-restore.md)
- [Remote node fallback](../failures/remote-node-fallback.md)
- [Container privileges](../invariants/container-privileges.md)
