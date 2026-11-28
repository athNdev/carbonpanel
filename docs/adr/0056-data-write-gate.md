<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

# ADR 0056 — One data-write gate for panel writes and staged publication

Status: Accepted (2026-11-26). Governs the staged-publication and direct-file
seams. Coordinates with the module archive-export fence in PR #681.

## Context

A staged publication takes an undo snapshot of the live tree and then commits
staged bytes and config. A direct panel write, upload, delete, rename, copy or
archive extraction that lands between those steps can be overwritten by the
publication or by its rollback, or can make the snapshot describe different
bytes than the target it later replaces. The lifecycle operation journal
already serializes publication against other lifecycle mutations, but putting
every direct file operation in that journal would duplicate the journaling that
PR #681 owns and would make ordinary edits wait behind unrelated lifecycle
work.

The next-restart commit also runs from start/restart call sites that are not
inside a journal, and its undo runs after the container operation. The gate
must therefore outlive the commit function without becoming a second journal.

## Decision

1. `internal/datawrite` owns one per-server data-path gate. Ordinary panel
   mutations take a shared lease; a staged publication takes the exclusive
   lease. Shared writers may run together. An exclusive claim reserves the
   drain from its first observation, refuses new shared writers, waits for
   those already in flight, and then owns the path until release. The
   reservation is not cleared between a drain wake-up and ownership, so a new
   shared writer cannot force a second drain or starve the claim.
2. Staged `ApplyBatch` acquires the exclusive lease after the batch journal is
   owned and before the undo snapshot, and releases it only after publication
   and any rollback finish. `CommitNextRestartFiles` returns a commit handle
   that keeps the exclusive lease until the caller calls `Finish` on success or
   `Undo` on failure. `Finish` does not undo, and `Undo` after `Finish` is a
   no-op. A next-restart attempt that fails with an `applying` recovery snapshot
   keeps the lease instead of releasing it, and the retry re-adopts that
   retained lease; ordinary writes stay refused until the recorded snapshot is
   resolved (ADR 0058).
3. Direct file-service writes, uploads, deletes, renames, moves, copies, folder
   creation, archive creation, extraction and remote archive download, plus
   direct mod enable/disable/delete and optimization-stack installs, take a
   shared lease before they touch the live tree. They are refused with a
   retryable Connect error while a staged publication owns the path. Staging a
   file does not take the lease because it does not write the live tree.
4. Reviewed live digests travel with their target in a typed drift guard. The
   guard is checked while the snapshot is taken and again immediately before
   the live writer commits. The exclusive lease covers normal panel writers;
   the recheck narrows, but does not close, an out-of-band writer that bypasses
   the panel.
5. Ordinary writes stay out of the lifecycle operation journal. The shared
   lease only conflicts with an exclusive staged publication or archive-export
   fence.

## Consequences

- A normal editor save, upload, delete or rename cannot straddle a staged
  snapshot and commit, and does not need a per-file journal row.
- A long archive download or extraction holds the shared lease for its live
  write window, so staged publication waits for it rather than reading a
  partially written tree.
- An out-of-band process that bypasses the panel is not an atomic
  compare-and-swap. The pre-commit drift check refuses a target that changed,
  but cannot make the final writer atomic against such a process.
- Integration with PR #681 must merge its module archive-export fence and this
  gate into one per-server owner. Until that merge, each lane keeps its own
  registry; the shared and exclusive shapes and refusal semantics are the
  same.
