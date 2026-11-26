---
type: Architecture
title: Server portability archive
description: Native ZIP and tar.gz containers share a versioned server manifest and per-file SHA-256 validation.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/serverarchive
  - internal/nodestorage/allocation.go
  - internal/rpc/services
sources:
  - title: Server portability archive format
    resource: ../../docs/server-portability.md
  - title: Server portability
    resource: https://carbonpanel.atlassian.net/browse/OSS-422
---

# Rules

- Stream server data through the owning node's storage accessor. Export callers
  must hold lifecycle admission and verify the server is stopped.
- Verify attached module containers on their owning nodes and refuse modules
  whose resolved bind mounts can write the data directory. Module lifecycles
  are not fenced by the export, and writer process-death qualification remains
  pending; independent writers must be stopped by the operator.
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
