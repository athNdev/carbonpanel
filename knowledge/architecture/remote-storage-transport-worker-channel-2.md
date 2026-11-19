---
type: Architecture
title: "Remote storage transport — Worker channel (part 2)"
description: "Accessor backups enumerate metadata and apply exclusions before reading file"
status: stable
owners: [oss-runtime]
code_refs: [internal/nodestorage, internal/storageworker, internal/docker/storage_transport.go, cmd/carbon-storage, internal/rpc/services/file.go, internal/rpc/services/storage_node.go, internal/rpc/services/server.go, internal/rpc/services/backup.go, internal/scheduler/backup.go, internal/snapshot/snapshot.go, internal/worldstore]
sources:
  - title: Parent concept
    resource: remote-storage-transport.md
---
controller-local tree.

Accessor backups enumerate metadata and apply exclusions before reading file
contents. Each retained file is read once while writing its archive entry and
checksum; excluded temporary files cannot fail a backup through content reads.
