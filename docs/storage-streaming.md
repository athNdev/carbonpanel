# Storage streaming and recovery

Bulk backup, restore and archive operations use the same bounded streaming
accessor on local storage, a controller-mounted shared root, subprocess workers
and Docker-attached workers. The worker and controller must both support
protocol version 2. Rebuild/publish the worker image alongside the controller;
an older worker returns an explicit upgrade error for bulk operations.

## Limits

| Surface | Enforced limit |
| --- | --- |
| Accessor `ReadFile` / `WriteFile` convenience operations | 8 MiB per file |
| Stream binary frame | 256 KiB |
| Open stream handles | 16 per worker session |
| Explicit stream write limit | Caller-supplied, at most 64 GiB |
| Worldstore backup / restore | 4 GiB per file, 64 GiB expanded total |
| Worldstore metadata | 500,000 files and a 1 MiB serialized manifest; both apply |
| Generic ZIP extraction | 20 GiB expanded total |
| On-node ZIP creation | 64 GiB output |
| Staged batch file publication / undo | 64 GiB each, 500,000 files, 8 MiB undo manifest |

Small-file helpers refuse larger content and direct callers to streams. UI
operations still using those helpers inherit their limit; streaming backup and
archive operations do not buffer a file to pass through a small-file helper.
Chunked upload completion, file copy, prepared downloads, remote archive saving
and manual server packs use streams. The editor reports its 8 MiB limit with
download/upload as the alternative. File listing uses filename and size for
its edit affordance without sniffing file bodies. Diagnostic log uploads read
at most the recent 8 MiB.
Protocol line readers allow the base64 expansion of 8 MiB plus 1 MiB overhead;
malformed or oversized frames terminate the affected channel. Directory listing
returns metadata, never file bodies. Memory for metadata and ZIP directory
structures grows with entry counts, separately from bounded transfer buffers.

## Ownership and recovery

Each write owns one exclusive `.carbon-write-<random>.partial` in the target
directory. These names are reserved recovery artifacts and are excluded from
backup/archive walks. Ordinary abort or cancellation removes only that
transaction's temporary. The old destination remains visible until `Commit`.

Commit syncs and closes the staged file before renaming it into place. If commit
fails, its error identifies the retained temporary; closing the writer preserves
it. Inspect the old target and staging file while the relevant operation/server
is stopped. Copy recoverable data elsewhere before manually promoting or
removing it. No background sweep deletes these recovery files. A disconnected
commit may have succeeded even if its acknowledgement was lost; inspect both
locations before retrying.

Accessor world restore uses `.worldstore-restore-stage` / `.worldstore-restore-old`;
generic tree replacement uses `.carbon-restore-stage` / `.carbon-restore-old`.
An interrupted extraction or failed tree promotion retains the recovery trees.
Starting another restore refuses existing recovery trees instead of deleting
them. Recover the old tree or finish the staged promotion deliberately before
removing those named directories and retrying. Per-entry promotion failures may
leave some entries in the live root and others under the old/stage trees; retain
all three until recovery completes.

Controller backup output exclusively creates `<destination>.partial`. A prior
partial is not silently deleted on retry; a failed final rename retains it and
reports its path. Source-world data is never removed by archive cleanup.

Staged batch publication stores old file contents under
`.carbon-panel/undo/<batch-id>/` with a versioned manifest containing target
paths, prior existence, sizes and SHA-256. All snapshots and the manifest are
committed before any live file is changed. Rollback verifies each backup before
promoting it. Successful rollback or finalized publication removes only that
batch's owned undo directory. A failed rollback or interrupted publish retains
it and blocks a new apply. Inspect its manifest and recover deliberately before
removing that directory. Recovery uses an independent bounded lifetime so a
canceled initiating operation can still put files back. Old content is kept on
the owning node, with no aggregate file-content slice in controller memory.

These guarantees cover cancellation and reported I/O failures. They do not
establish power-loss durability on every filesystem. Rename replacement and
open-file sharing follow the host OS; failure preserves the existing target.
