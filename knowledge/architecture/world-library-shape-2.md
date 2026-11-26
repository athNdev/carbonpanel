---
type: Architecture
title: "World Library — Rollback rules (part 2)"
description: "Failed multi-family swap compensation: quarantine, byte proof and binding proof."
status: stable
owners: [oss-runtime]
code_refs: [internal/worldlib/binding.go, docs/adr/0049-world-swap-data-preserving-rollback.md]
sources:
  - title: Parent concept
    resource: world-library.md
---
# Rollback rules

- A failed multi-family publish quarantines already-promoted world bytes in a
  separate tree instead of mixing them with the retained old family.
- Compensation restores each retained family and checks its path and any old
  `level.dat` against the pre-swap observation. File hashing uses a stream.
- Restoring the prior binding follows disk verification. Tree cleanup requires
  a terminal journal decision.
- Ambiguous I/O deletes nothing: both trees stay for operator recovery, the
  journal keeps its lock, and the outcome is `unknown` (ADR 0049).
- Active operations prevent retry and startup tree changes. Legacy recovery
  without an active operation restores only when the live path is confirmed
  missing and preserves staged and quarantined trees.

# Running servers

- Bind and recovery read locality and endpoint generation from the owning
  node row. A registered controller daemon uses its local data path even
  with an assigned node ID. Legacy local aliases remain supported.
- The binder holds the server transition across Docker observation, stop, swap
  and restart. The internal adapter requires server power grants and the
  active journal's matching server, container and node endpoint.
- A safe rollback restores running intent on the original container.
  Uncertain stop or restart retains the journal lock and recovery data.

# Request replay

- A bind request ID identifies one server, level, revision, activation window
  and node generation. A completed replay must still name the binding installed
  by that operation, before current config or layout is read. An omitted
  level can replay only when one retained binding proves the request. Legacy
  journal rows rely on binding and timestamps; the activation window is
  unverifiable. Replay audit uses the proved binding's level and revision.
- Fresh bind requires persisted config and refuses a different explicit level
  before creating a journal. Bind checks the level again after acquiring
  the lifecycle transition and after the operation journal admits the bind.
  Staged level and custom server properties writes check for an active bind
  within the same transaction, including rollback.

# Revision archives

- Import records a library-relative archive key. Preview and bind resolve it
  under the configured World Library root and accept legacy paths within that
  root. Fresh bind validates the same bounded layout as preview before
  stopping the running server. The copied archive and extraction tree are
  siblings so root archive entries cannot collide with the staged ZIP.
- Match Java world data versions to the release JAR's `version.json`
  `world_version`. Java 1.21.1 writes 3955 and Java 1.21 writes 3953, even
  though they share a network protocol version.
