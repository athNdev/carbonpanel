---
type: Runbook
title: Server lifecycle journal
description: Interrupted container mutations stay locked until the daemon is observed, and an operator can release that lock without deleting containers.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/operation
  - internal/activity
  - internal/db/server_operation.go
  - cmd/carbon-panel/operations.go
  - internal/db/portlease.go
  - internal/rpc/services/support.go
sources:
  - title: Server operation runbook
    resource: ../../docs/runbook-server-operations.md
---

# Use this when

A server lifecycle action must survive a panel crash, or a server is stuck
behind an operation lock.

# Rules

- Persist the intent before calling Docker. If the intent cannot be stored,
  do not perform the mutation.
- A scheduled backup persists that same intent before it pauses world saves
  or writes an archive. An empty request id makes each run its own row. A
  crash leaves the row active until startup recovery observes the server.
  Recovery of that backup says the archive was not finalized and that the
  backup can be run again. A stopped container is released without a command,
  and the server is not started. A container that is still running stays
  locked until save-on succeeds; a failed save-on keeps the lock and says to
  run save-on or restart the server. Recovery of a restore, a
  delete, or a migration records the next action and keeps the lock: confirm
  the world, confirm the container and the world, or confirm which node holds
  the world. Container state does not release those three locks. An operator
  resolves one with `-operations resolve` after that check.
- Deleting a server persists a delete intent before modules, the container,
  the panel row, or the world are removed. A server with no container still
  takes that lock. An active backup, restore, or migration row refuses the
  delete and leaves the world in place.
- A start, stop, restart, recreate, boot auto-start, scheduled start, stop,
  or restart, and a deep-sleep or freeze wake all refuse while one of those
  locks is held. A deeply-asleep server stops without waking: its container
  is already exited, so the journaled stop skips Docker, clears the proxy
  knock, and converges the row to stopped.
- A frozen server with no active lock still auto-starts. A deeply asleep
  server still stays asleep across a panel restart.
- A client `request_id` plus the action kind is idempotent for one server.
  Completing a key does not run the mutation again. A failure needs a new key.
- Do not release a lock when the container state cannot be read. Retry the
  read when the daemon returns.
- `-operations resolve` clears the lock and writes a note. It must not stop,
  remove, or prune containers.
- `-operations release-port` frees host ports whose server row is already
  gone. It must refuse a server that still exists, and it must not stop a
  container or delete world files.
- Finished journal rows may be deleted after 30 days. An active lock may not.
- Diagnostics project the journal, never the raw row. `operation.Describe` maps
  the durable phase to three outcomes — success, failed, or unknown — and to a
  stable next action: `retry_with_new_request_id` after a failure (the same key
  is refused), `resolve_operation` for an attempt that started but never
  finished, `wait` while it is still expected to finish. An unknown outcome is
  never reported as success or failure; the external effect may or may not have
  happened.
- Credentials in recorded error text are redacted before it reaches a
  diagnostic. Hosts, paths and daemon messages stay: they are the evidence.
- The activity feed stores the same client `request_id` as the journal row it
  records, so operators can join the two without guessing. An empty client
  id stays empty on activity; it is not invented after the fact.
- The support bundle always carries `operations.json` with that projection and
  `observations.json` with per-server last-good timestamps, so a bundle can
  explain a stuck or stale server without the Docker socket.

# Read next

- [Persistence](../architecture/persistence.md)
- [Runtime model](../architecture/runtime.md)
