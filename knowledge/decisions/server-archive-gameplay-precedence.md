---
type: Decision
title: Archived gameplay settings seed the initial server config
description: Valid archived gameplay values beat panel defaults but never replace destination identity, credentials or network settings.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/serverarchive/archive.go
  - internal/db/store.go
  - internal/rpc/services/server_archive_import.go
sources:
  - title: Server portability archive format
    resource: ../../docs/server-portability.md
  - title: Server portability archive
    resource: ../architecture/server-portability.md
---

# Choice

The archive keeps the checked ZIP/TAR bytes, and import also parses a typed
allowlist from the already filtered, checksum-validated root
`server.properties`: `level-name`, `difficulty`, `gamemode`,
`sync-chunk-writes` and `pause-when-empty-seconds`. Accepted values are seeded
into the new server's `ServerConfig` in the same transaction as the server row,
before the managed image creates a container. An absent, empty or unsupported
value keeps the panel default instead of persisting a value the game would
reject.

Precedence is fixed. Destination RCON, ports, routing, proxy settings, auth,
image and ownership always come from the destination; an archive cannot supply
them. A present and valid archived gameplay value wins over the panel default.
Other recognized properties keep their archived bytes, except where a
destination global setting forces an environment value outside the allowlist.

# Alternatives

Copying all environment-backed properties would let the archive rewrite
destination credentials and deployment bindings. Copying none keeps the current
bug: itzg startup rewrites archived difficulty and game mode with panel
defaults. Editing `server.properties` at import would drift from the config row
and be undone by the image's next startup write. Rejecting an archive for an
unsupported gameplay value would make ordinary hand-edited sources
unimportable without improving safety.

# Consequence

Archive import must keep the seed and the server row in one database
transaction so a failed import leaves neither. The gameplay allowlist is
intentionally small; adding a property requires it to be safe for the
destination and available in the filtered export. Tests must assert the exact
persisted config and the effective Docker environment for both ZIP and TAR.

Writer qualification is separate. A stopped container does not prove that an
independent host writer died. Export holds the server transition and the module
lifecycle gate, refuses attached modules whose resolved or live mounts can write
the archived data directory, and refuses live mounts that do not match the
stored configuration. Process-death and external-daemon qualification remain
pending; operators must stop independent writers.
