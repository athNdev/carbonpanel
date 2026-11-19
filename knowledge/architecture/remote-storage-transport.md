---
type: Architecture
title: Remote storage transport
description: JSON accessor worker and Docker attach path for nodes without shared controller storage.
status: stable
owners: [oss-runtime]
code_refs: [internal/nodestorage, internal/storageworker, internal/docker/storage_transport.go, cmd/carbon-storage, internal/rpc/services/file.go, internal/rpc/services/storage_node.go, internal/rpc/services/server.go, internal/rpc/services/backup.go, internal/scheduler/backup.go, internal/snapshot/snapshot.go, internal/worldstore]
sources:
  - title: Bounded storage streaming
    resource: ../../docs/adr/0013-bounded-storage-streaming.md
  - title: ADR 0005 node-aware file access
    resource: ../../docs/adr/0005-node-aware-file-access.md
---

# Parts

- [Remote storage transport — When transport is used](remote-storage-transport-when-transport-is-used.md) — A remote node with a declared storage_root and no reachable
- [Remote storage transport — Worker channel](remote-storage-transport-worker-channel.md) — Bulk access uses protocol v2: 256 KiB acknowledged chunks, per-frame/whole-stream
- [Remote storage transport — Worker channel (part 2)](remote-storage-transport-worker-channel-2.md) — Accessor backups enumerate metadata and apply exclusions before reading file
- [Remote storage transport — Fail-closed + File RPC coverage + Read next](remote-storage-transport-fail-closed.md) — With no transport configured, transport-only nodes keep ErrUnavailable. Remote

# Key facts

- A remote node with a declared `storage_root` and no reachable
- `controller_storage_root` (and no local directory at the node root path) is
- pre-creating a controller-visible root and inadvertently testing shared storage.
- shared and worker adapters. Old workers refuse bulk operations with an upgrade
- See [limits and recovery](../../docs/storage-streaming.md).
- that UI hint. Staged file publication keeps verified on-node undo files and a
- `cmd/carbon-storage --mode=accessor` serves the `storageworker` JSON line
- `nodestorage.NewLocal`. `(*nodestorage.Resolver).ForNode(ctx, node, dataRoot)`
- controller instances cannot change one another's storage selection.
- Closing a session interrupts an in-flight request without waiting for the
- identifiable through the unavailable-storage wrapper and never selects the
- apply the same default: a transport-only node is refused when the caller passes
- no resolver, an explicit nil resolver, or a resolver without a transport. The
- consulted, and nil-receiver `ForNode` still serves local/shared nodes.
- manual server-pack zip open storage through the instance-owned resolver.
- on-node archives; download/export zip bytes land only in the controller download
- [Node resolution](node-resolution.md)
- [Remote node fallback](../failures/remote-node-fallback.md)
- temp). Snapshot create/rollback and worldstore backup/restore
- artifacts stay in the controller backup directory. `DownloadRemoteArchive`
- Backup/restore and ZIP extraction/create use streams rather than whole-file
- slices. Worldstore local backup shares the metadata-only accessor archive path;
- services, the scheduler (and its snapshot engine), staging and backup owners.
- checksum; excluded temporary files cannot fail a backup through content reads.
- `ExtractArchive` streams a zip through the same accessor
- `CreateArchive`, `DownloadArchive`, and `ExportWorld` walk and write
- stream through the injected resolver's accessor (`worldstore.BackupToAccessor` /
- `RestoreFromAccessor`, `WriteZip` / `ReplaceTreeFromZip`). Backup
- retained files are read once. Existing restore recovery trees are never erased
