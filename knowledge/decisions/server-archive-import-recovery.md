---
type: Decision
title: Durable server archive import ownership and recovery
description: An archive submission records its chosen destination before placing data, and a retry removes only a directory whose durable marker proves this request created it.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/db/archive_import.go
  - internal/rpc/services/server_archive_import.go
  - internal/rpc/services/server.go
sources:
  - title: Server portability archive format
    resource: ../../docs/server-portability.md
  - title: Server portability archive
    resource: ../architecture/server-portability.md
---

# Choice

Import records an `ArchiveImport` row in the same database transaction as the
request's idempotency claim. The row stores the chosen server id, node, data
path, a random claim token and a phase (`claimed`, `staged`, `committed`).
After exclusive directory allocation the import writes
`.carbon-archive-import` containing the token before copying archive bytes.
The marker name is excluded by the archive format, so it can never be exported
as server data.

A retry with the same request id reads the durable intent instead of failing on
the stranded claim. When no server row exists it removes the destination only
if the recorded path, node and marker token all prove ownership. A committed
server row always wins: recovery clears the marker but never removes data. If
the marker is missing or different, recovery refuses with a visible
failed-precondition error and keeps the durable claim so an operator can
inspect the destination. No path is removed on a guess.

The replay identity hashes the submission without its upload id. Upload
sessions are in-memory and do not survive a controller restart, so the same
request id must be retryable with a freshly uploaded copy of the bytes.

# Alternatives

Reusing the server operation journal does not fit: that journal is keyed by an
existing server row, while this window deliberately precedes the row commit.
Trusting the idempotency claim plus a recomputed path cannot distinguish an
interrupted destination from an operator-created directory. Deleting any
directory at the recomputed path would remove data the panel never owned.
Releasing the claim automatically when ownership is unproven makes the request
usable again but leaves an untracked partial tree with no recovery evidence.

# Consequence

`ClaimArchiveImport` must stay atomic with the idempotency row; a separate
claim remains the original bug. Recovery is driven by the retry path and by
in-process serialization of the same request, not by a second controller
process. External writers, a replaced Docker daemon and process death remain
outside what the stopped consistency policy proves.
