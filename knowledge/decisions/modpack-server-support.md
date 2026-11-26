---
type: Decision
title: Modrinth dedicated server support
description: Indexed Modrinth projects that explicitly declare dedicated servers unsupported cannot be deployed or attached to a server.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/indexers/modrinth/adapter.go
  - internal/rpc/services/modpack.go
  - internal/rpc/services/server.go
sources:
  - title: Modpack compatibility sweep
    resource: ../../docs/testing/modpack-compatibility-sweep.md
---

# Decision

Library sync preserves Modrinth’s project-level `server_side`. Creating a
server or changing its pack rejects `unsupported` before resource allocation
or configuration changes. An empty value from an older library row remains
deployable until sync refreshes the declaration.

File-level side declarations govern individual files and can disagree with the
project declaration. Even a pack whose project declares dedicated-server
support still needs a runtime compatibility test.
