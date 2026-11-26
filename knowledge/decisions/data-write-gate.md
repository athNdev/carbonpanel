---
type: Decision
title: One data-write gate for panel writes and staged publication
description: Ordinary panel file mutations take a shared per-server lease; staged publication owns the same path exclusively from before its undo snapshot through rollback.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/datawrite
  - internal/staging/apply.go
  - internal/staging/file_undo.go
  - internal/rpc/services/file.go
  - internal/operation/journal.go
sources:
  - title: ADR 0056
    resource: ../../docs/adr/0056-data-write-gate.md
---

# Choice

`internal/datawrite` keeps one gate per server. Direct editor writes, uploads,
deletes, renames, moves, copies, folder creation, archive creation, extraction,
remote archive downloads and direct mod enable/disable/delete/stack installs
take a shared lease. A staged publication takes the exclusive lease after it
owns the batch journal and before it snapshots the live tree, and keeps it
through publication and any rollback. An exclusive claim reserves the drain
from the first observation until it either owns the path or is canceled, so a
new shared writer cannot slip in between a completed drain and ownership.

`ApplyBatch` releases the lease when its journaled callback finishes.
`CommitNextRestartFiles` returns a commit handle because the start/restart call
site has no journal: the handle keeps the lease until the caller calls
`Finish` on success or `Undo` on failure. `Undo` after `Finish` is a no-op.

A reviewed live digest travels with its target as a typed drift guard. It is
checked during snapshot creation and again immediately before the live writer
commits.

# Consequences

- A normal panel write is refused with a retryable error while a staged
  publication owns the path; it does not wait behind a long publication.
- A staged publication waits for direct writers already in flight, including a
  remote archive download or extraction, instead of reading a partial tree.
- Direct writes do not create per-file journal rows; the lifecycle journal and
  this data gate cover different concerns.
- The guard narrows but cannot close an out-of-band writer that bypasses the
  panel; the final writer is not an atomic compare-and-swap.
- The module archive-export fence from PR #681 must eventually merge with this
  gate so one per-server owner covers archives, staged publication and direct
  writes.
