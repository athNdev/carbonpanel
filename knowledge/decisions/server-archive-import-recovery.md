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
path, a deterministic digest of the independently verified archive content and
manifest, the resolved allocation root, a random claim token and a phase
(`claimed`, `staged`, `committed`). After exclusive directory allocation the
import records the validated root, writes `.carbon-archive-import` containing
the token, and only then copies archive bytes. The marker name is excluded by
the archive format, so it can never be exported as server data.

A retry reads the durable intent instead of failing on the stranded claim. It
verifies the supplied upload against the recorded content identity before
touching anything, so a fresh upload id is accepted only for identical verified
content. When no server row exists it removes the destination only if the
resolved allocation root, recorded path, node and marker token prove ownership.
A changed node storage root is a visible mismatch, never a redirect.

The committed phase is written in the same transaction as the server and its
initial config. It is durable publication evidence in its own right: recovery
never reclaims a committed destination even when the server row is gone after a
normal keep-data delete whose marker cleanup was interrupted. A missing marker,
a changed allocation root, or an unverified replaced archive fails closed with a
visible error and keeps the claim for inspection. Nothing is removed on a guess.

The replay identity hashes the submission without its upload id, while the
intent records the verified content identity. Upload sessions do not survive a
restart, so the same request id must be retryable with a fresh copy of the same
bytes.

# Alternatives

The server operation journal is keyed by an existing server row, while this
window precedes the row commit. Trusting a recomputed path would remove data the
panel never owned, and releasing an unproven claim leaves an untracked partial
tree with no recovery evidence.

# Consequence

`ClaimArchiveImport` must stay atomic with the idempotency row. Recovery is
driven by the retry path and in-process serialization, not a second controller.
External writers, a replaced Docker daemon and process death remain outside the
stopped consistency policy.
