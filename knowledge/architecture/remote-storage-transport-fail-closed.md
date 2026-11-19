---
type: Architecture
title: "Remote storage transport — Fail-closed + File RPC coverage + Read next"
description: "With no transport configured, transport-only nodes keep ErrUnavailable. Remote"
status: stable
owners: [oss-runtime]
code_refs: [internal/nodestorage, internal/storageworker, internal/docker/storage_transport.go, cmd/carbon-storage, internal/rpc/services/file.go, internal/rpc/services/storage_node.go, internal/rpc/services/server.go, internal/rpc/services/backup.go, internal/scheduler/backup.go, internal/snapshot/snapshot.go, internal/worldstore]
sources:
  - title: Parent concept
    resource: remote-storage-transport.md
---
# Fail-closed

With no transport configured, transport-only nodes keep `ErrUnavailable`. Remote
accessors never fall back to controller paths. `MapDataPath` and `EnsureDataRoot`
apply the same default: a transport-only node is refused when the caller passes
no resolver, an explicit nil resolver, or a resolver without a transport. The
instance-owned resolver is passed explicitly by callers that may map a path with
a configured transport (RPC services, mod/packwiz handlers, scheduler, snapshot
engine, metrics collector). No process-global transport or context value is
consulted, and nil-receiver `ForNode` still serves local/shared nodes.

# File RPC coverage

Accessor-backed file RPCs (`ListFiles`, `UpdateFile`, `DeleteFile`,
`SaveUploadedFile`, `GetFile`, `RenameFile`, `CreateFolder`, `CopyFile`,
`MoveFile`, `InitFileDownload`), `serverLatestLog`, and CreateServer's
manual server-pack zip open storage through the instance-owned resolver.
`ExtractArchive` streams a zip through the same accessor
(`nodestorage.ExtractZip`) on transport-only nodes. Zip *write* RPCs
`CreateArchive`, `DownloadArchive`, and `ExportWorld` walk and write
through `nodestorage.CreateZip` / `WriteZip` (transactional output streams for
on-node archives; download/export zip bytes land only in the controller download
temp). Snapshot create/rollback and worldstore backup/restore
stream through the injected resolver's accessor (`worldstore.BackupToAccessor` /
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
