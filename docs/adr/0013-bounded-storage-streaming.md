# ADR 0013: Bounded storage streams with explicit commit

Status: accepted

## Context

An accessor's whole-file byte slices and the worker's base64 JSON line cap made
large-file support depend on the adapter. Archive output also accumulated the
complete archive in memory. Worldstore advertised 4 GiB files while its local
backup implementation still used the 128 MiB in-memory read path.

## Decision

Keep one node-aware `Accessor` seam. Add `OpenRead`, returning a closeable
sequential/random-access handle, and `BeginWrite`, returning a writer with an
explicit `Commit`. Bulk callers use these methods; whole-file convenience
operations enforce an 8 MiB limit on every adapter.

Worker protocol version 2 exchanges at most 256 KiB of binary data per stream
frame. Each frame is acknowledged before the next is sent. Sequence numbers
and SHA-256 validate sequential chunks, and the final whole-stream digest is
validated before commit. Random-access frames validate their offset and chunk
digest. A session permits at most 16 handles. Both channel readers bound JSON
lines to the base64 expansion of 8 MiB plus 1 MiB framing allowance. A worker
without version 2 refuses streaming with an upgrade message.

Writers exclusively create unpredictable `.carbon-write-<random>.partial` files
beside the destination, initially private to the process user. Commit syncs and
closes the file, then promotes it with a same-directory rename. Existing target
permissions are retained; new files retain the previous accessor's 0644 mode.
Writes remain within a pinned server-root handle, including symlink traversal.
Closing an uncommitted transaction removes only its owned temporary. A commit
failure retains that temporary and reports its relative path for recovery.
Cancellation during a frame closes the session, since a partial JSON exchange
cannot safely be reused. Worker EOF closes remaining handles.

Backup, restore and archive creation/extraction use streams, with ZIP random
access served in bounded chunks. Local worldstore backup uses the same
metadata-only enumeration and archive writer as accessor backup. Identity
checks and refusal of worldstore symlinks remain in force. Restore refuses to
erase an existing recovery stage or old tree; failures preserve those trees.
Binary file RPCs and staged modpack publication also use streams. Staged undo
keeps verified copies and a versioned manifest on the owning node before live
mutation, rather than retaining all old file bodies in controller memory.
An unresolved undo directory blocks a fresh publication until recovery.

## Consequences

Transfer memory depends on frame size rather than file/archive size. Metadata
lists and ZIP central-directory/manifest structures still depend on bounded
entry counts. Callers own handles and operation contexts; detached jobs must
pass their own lifetime rather than an initiating request's context.

No cross-platform power-loss durability is claimed: directory metadata is not
fsynced, and rename atomicity follows the host filesystem/OS guarantees. The
implementation does not move or delete the existing target before promotion.
An ambiguous disconnect during commit requires inspecting the target and any
reported recovery temporary before retrying. Controller and worker images must
be upgraded together for bulk operations.

See [storage streaming operations](../storage-streaming.md) for limits and
recovery ownership.
