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
sources:
  - title: ADR 0005 node-aware file access
    resource: ../../docs/adr/0005-node-aware-file-access.md
---

# When transport is used

A remote node with a declared `storage_root` and no reachable
`controller_storage_root` (and no local directory at the node root path) is
treated as transport-only: the controller must not create or read a local copy
of the server tree.

# Worker channel

`cmd/carbon-storage --mode=accessor` serves the `storageworker` JSON line
protocol over stdin/stdout, delegating to the same confined rules as
`nodestorage.NewLocal`. `nodestorage.ForNode` opens a `Transport` when
`RequiresRemoteTransport` is true.

Production wiring: set `storage.worker_image` to an image that ships
`/usr/local/bin/carbon-storage` (the panel image build includes it). The panel
registers `docker.StorageTransport` on startup.

# Fail-closed

With no transport configured, transport-only nodes keep `ErrUnavailable`. Remote
accessors never fall back to controller paths.

# File RPC coverage

Accessor-backed file RPCs (`ListFiles`, `UpdateFile`, `DeleteFile`,
`SaveUploadedFile`, `GetFile`, `RenameFile`, `CreateFolder`, `CopyFile`,
`MoveFile`, `InitFileDownload`), `serverLatestLog`, and CreateServer's
manual server-pack zip open storage through `nodestorage.ForNode`. Zip-oriented RPCs (`CreateArchive`, `DownloadArchive`,
`ExtractArchive`, `DownloadRemoteArchive`) and `ExportWorld` still walk or
build archives on the controller filesystem; transport-only nodes are refused
at those entry points until a streaming zip path exists. No worker means
`ErrUnavailable` / Connect `CodeUnavailable` — never a controller-local
fallback.

# Read next

- [Node resolution](node-resolution.md)
- [Remote node fallback](../failures/remote-node-fallback.md)
