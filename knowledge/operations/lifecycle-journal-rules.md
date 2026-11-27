---
type: Runbook
title: "Server lifecycle journal — Rules"
description: "Persist the intent before calling Docker. If the intent cannot be stored,"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: lifecycle-journal.md
---
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
- Diagnostics project the journal, never the raw row: stable outcome, reason
  code and next action (`retry_with_new_request_id`, `resolve_operation`,
  `wait`). `unknown` is never success or failure; stale `executing` is
  `executing_stale`, uncertain effect is `uncertain_external_effect`, both
  "do not retry blindly".
- The projection joins by node id, endpoint generation, request key and phase.
  Daemon identity appears only as `matched` / `unmatched` / `unknown`, and no raw
  daemon id is projected: the correlation fields carry no daemon id, host,
  socket or credential. See [ADR 0058](../../docs/adr/0058-operation-daemon-id-disclosure.md).
- Credentials in recorded error text are redacted before it reaches a
  diagnostic. Hosts, paths and daemon messages stay: they are the evidence.
- The activity feed stores the same client `request_id` as the journal row, so
  operators can join them; an empty id stays empty.
