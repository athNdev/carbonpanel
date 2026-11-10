---
type: Failure Mode
title: Remote node fallback
description: Operations that cannot resolve a remote node must error instead of using the controller's Docker socket.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/docker
  - internal/module/hooks.go
  - internal/docker/freezer.go
  - internal/snapshot
sources:
  - title: Fail-closed node resolution change
    resource: https://github.com/athNdev/carbon-panel/pull/225
---

# Symptom

A module, snapshot, sleep or file operation succeeds against the wrong host, or
silently operates on the controller's local Docker socket.

# What was fixed

Module, snapshot and deep-sleep helpers now return an error when the target node
cannot be resolved, and a module records the server's node ID. A cached client
is not reused after that node's host or TLS material changes. `GetClient` does
not answer a named node with the local daemon.

# Remaining exposure

- `internal/docker/freezer.go` still targets the local client unconditionally.
- `internal/module/hooks.go:execInModule` still uses the manager's client.
- Creating a server, deleting it, mod installation, packwiz bake, snapshot
  create/restore, scheduled backup archives, and scheduled modpack sync use
  the node's mapped storage root. A remote node with no shared mount is
  refused unless `storage.worker_image` is configured and the call path uses
  `nodestorage.ForNode` (Get/Rename/List/Update/Delete, CreateFolder,
  Copy/Move, uploads, InitFileDownload, `serverLatestLog`, staged content,
  and zip extract). Creating or downloading archives and world export still
  require controller-local filesystem walks; transport-only nodes get
  `CodeUnavailable` on those entry points even when a worker is configured.
  Worldstore backup paths are not on the transport yet.

# Detection

Add a test with a server whose node cannot be resolved. The operation must
return an error and must not touch the local socket.

# Read next

- [Node resolution](../architecture/node-resolution.md)
- [Known gaps](known-gaps.md)
