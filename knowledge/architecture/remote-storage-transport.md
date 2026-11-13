---
type: Architecture
title: Remote storage transport
description: JSON accessor worker and Docker attach path for nodes without shared controller storage.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/nodestorage
  - internal/storageworker
  - internal/docker/storage_transport.go
  - cmd/carbon-storage
  - internal/rpc/services/file.go
  - internal/rpc/services/storage_node.go
  - internal/rpc/services/server.go
  - internal/rpc/services/backup.go
  - internal/scheduler/backup.go
  - internal/snapshot/snapshot.go
  - internal/worldstore
sources:
  - title: Bounded storage streaming
    resource: ../../docs/adr/0013-bounded-storage-streaming.md
  - title: ADR 0005 node-aware file access
    resource: ../../docs/adr/0005-node-aware-file-access.md
---

# When transport is used

A remote node with a declared `storage_root` and no reachable
`controller_storage_root` (and no local directory at the node root path) is
treated as transport-only: the controller must not create or read a local copy
of the server tree.

# Worker channel

Bulk access uses protocol v2: 256 KiB acknowledged chunks, per-frame/whole-stream
SHA-256, sequence checks, cancellation and at most 16 open handles/session.
`Accessor.OpenRead` supports sequential and bounded random reads; `BeginWrite`
returns an explicit-commit writer. Whole-file helpers enforce 8 MiB on local,
shared and worker adapters. Old workers refuse bulk operations with an upgrade
error. Both line readers bound frames. Malformed channel I/O closes the session.
See [limits and recovery](../../docs/storage-streaming.md).
Binary upload completion/copy/download/manual-pack calls stream through this
seam. File listings never read content to infer editability; filename/size supply
that UI hint. Staged file publication keeps verified on-node undo files and a
manifest rather than aggregate old-content slices.

`cmd/carbon-storage --mode=accessor` serves the `storageworker` JSON line
protocol over stdin/stdout, delegating to the same confined rules as
`nodestorage.NewLocal`. `nodestorage.ForNode` opens a `Transport` when
`RequiresRemoteTransport` is true.

Production wiring: set `storage.worker_image` to an image that ships
`/usr/local/bin/carbon-storage` (the panel image build includes it). The panel
registers `docker.StorageTransport` on startup.

The subprocess adapter uses inherited OS pipes: the parent closes its copies
of the child ends after startup and retains the session ends until shutdown.
Its roundtrip test must assert that transport was selected, rather than
pre-creating a controller-visible root and inadvertently testing shared storage.
Closing a session interrupts an in-flight request without waiting for the
request lock. The subprocess adapter closes both session pipes and reaps the
worker, terminating it after a bounded grace period if it does not exit.
Process and Docker worker sessions bind the handshake and session lifetime to
the supplied context; cancellation closes their transport.
Docker worker removal uses an independent bounded cleanup context, including
when startup fails after container creation. Removal failures are reported.
An instance-owned resolver can select a transport with the caller's context.
Legacy `ForNode` callers still use the registered process-wide transport and a
background context until each owning subsystem is migrated.
ListFiles and GetFile use the composition-root resolver and the RPC context.
Mutations and background archive operations retain their existing lifetime
until they gain independently owned cancellation and rollback policies.

Accessor backups enumerate metadata and apply exclusions before reading file
contents. Each retained file is read once while writing its archive entry and
checksum; excluded temporary files cannot fail a backup through content reads.

# Fail-closed

With no transport configured, transport-only nodes keep `ErrUnavailable`. Remote
accessors never fall back to controller paths.

# File RPC coverage

Accessor-backed file RPCs (`ListFiles`, `UpdateFile`, `DeleteFile`,
`SaveUploadedFile`, `GetFile`, `RenameFile`, `CreateFolder`, `CopyFile`,
`MoveFile`, `InitFileDownload`), `serverLatestLog`, and CreateServer's
manual server-pack zip open storage through `nodestorage.ForNode`.
`ExtractArchive` streams a zip through the same accessor
(`nodestorage.ExtractZip`) on transport-only nodes. Zip *write* RPCs
`CreateArchive`, `DownloadArchive`, and `ExportWorld` walk and write
through `nodestorage.CreateZip` / `WriteZip` (transactional output streams for
on-node archives; download/export zip bytes land only in the controller download
temp). Snapshot create/rollback and worldstore backup/restore
stream through `nodestorage.ForNode` (`worldstore.BackupToAccessor` /
`RestoreFromAccessor`, `WriteZip` / `ReplaceTreeFromZip`). Backup
artifacts stay in the controller backup directory. `DownloadRemoteArchive`
downloads to a controller temp file then writes or extracts through the
same accessor. No worker means `ErrUnavailable` / Connect
`CodeUnavailable` — never a controller-local fallback.

Backup/restore and ZIP extraction/create use streams rather than whole-file
slices. Worldstore local backup shares the metadata-only accessor archive path;
retained files are read once. Existing restore recovery trees are never erased
on restart. Ordinary abort cleans only its owned temporary; failed file commits
and tree promotions preserve recovery staging. Same-directory promotion does
not imply cross-platform power-loss durability.

# Read next

- [Node resolution](node-resolution.md)
- [Remote node fallback](../failures/remote-node-fallback.md)
