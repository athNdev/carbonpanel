---
type: Architecture
title: "Remote storage transport — When transport is used"
description: "A remote node with a declared storage_root and no reachable"
status: stable
owners: [oss-runtime]
code_refs: [internal/nodestorage, internal/storageworker, internal/docker/storage_transport.go, cmd/carbon-storage, internal/rpc/services/file.go, internal/rpc/services/storage_node.go, internal/rpc/services/server.go, internal/rpc/services/backup.go, internal/scheduler/backup.go, internal/snapshot/snapshot.go, internal/worldstore]
sources:
  - title: Parent concept
    resource: remote-storage-transport.md
---
# When transport is used

A remote node with a declared `storage_root` and no reachable
`controller_storage_root` (and no local directory at the node root path) is
treated as transport-only: the controller must not create or read a local copy
of the server tree.
