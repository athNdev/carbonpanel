---
type: Architecture
title: Server portability archive
description: Native ZIP and tar.gz containers share a versioned server manifest and per-file SHA-256 validation.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/serverarchive
  - internal/rpc/services
  - internal/nodestorage/allocation.go
  - proto/carbonpanel/v1
sources:
  - title: Server portability archive format
    resource: ../../docs/server-portability.md
  - title: Server portability
    resource: https://carbonpanel.atlassian.net/browse/OSS-422
---

# Rules

- Stream server data through the owning node's storage accessor. Export callers
  must hold lifecycle admission and verify the server is stopped.
- Hold the server's module lifecycle gate for the whole export. It serializes
  module create, start, stop, update, recreate and delete paths, including their
  asynchronous workers, and refuses an export while one is in flight. Module
  create and update reserve that admission before acknowledging the request.
  Direct panel file writes hold a shared data-write claim and are refused while
  an export owns the server. Verify attached module containers on their owning
  nodes and refuse modules whose resolved bind mounts can write the data
  directory or whose live effective data-directory mounts differ from the
  stored configuration. A writable mount whose source is an ancestor of the data
  directory is an overlap too. A stopped container is not writer
  process-death qualification; independent host and daemon writers remain the
  operator's responsibility.
- Record an archive import intent and verified content identity atomically with
  its idempotency claim, record the validated allocation root, and place data
  only after exclusive allocation. Reclaim an interrupted destination only when
  the durable allocation root, node, path and ownership marker agree; never
  reclaim a committed intent, including retained data after a keep-data delete.
  Fail visibly when ownership or identity cannot be proven. See
  [import recovery](../decisions/server-archive-import-recovery.md).
- Serialize only explicitly portable runtime fields. Keep deployment identity,
  host paths, overrides and credentials outside the manifest.
- Validate the complete archive before allocating a destination. Copy only into
  a newly allocated destination and verify its input against the inspected
  manifest before keeping the destination.
- Parse the typed gameplay allowlist from the filtered root
  `server.properties` and seed it into the initial config in the same
  transaction as the server row, before Docker creation. Valid archived values
  beat panel defaults; destination credentials, ports, routing, auth, image and
  ownership always win. An absent or unsupported value keeps the default. See
  [gameplay precedence](../decisions/server-archive-gameplay-precedence.md).
- Keep upload ownership, server-create authorization and retry idempotency scoped
  to the submitting principal. Allocate the destination exclusively on the
  chosen node before committing the new server row.
- Base archive preview authorization on server-create rather than node
  inventory: return only enabled destination IDs and names, never addresses or
  private inventory. See [destination preflight](../decisions/archive-destination-preflight.md).
- Export and create-from-archive console identity and preflight fencing: see
  [server archive console](server-archive-console.md).
