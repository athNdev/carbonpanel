---
type: Runbook
title: World swap harness wire and fixture constraints
description: Connect JSON int64 handling, node-owned fixture reads, real backup-byte verification and staging artifact ambiguity rules for the world swap qualification harness.
status: stable
owners: [oss-runtime]
code_refs:
  - scripts/qualify-world-swap.mjs
  - scripts/test-qualify-world-swap.mjs
  - scripts/qualify-compose-artifacts.sh
sources:
  - title: Final-candidate World Library swap qualification
    resource: world-library-swap-qualification.md
---

Connect JSON (protojson) serializes `int64` fields such as upload byte counts as
decimal strings. Normalize them with `BigInt` before comparing with Node buffer
lengths. `scripts/test-qualify-world-swap.mjs` feeds a serialized
`GetUploadStatus` response so a strict-equality regression is caught offline.

BindWorld can take longer than an ordinary read because a supported graceful
stop may need 60 seconds. Use its own bounded deadline and reconcile an
interrupted call by replaying the same `request_id`; the journal returns the
original operation rather than starting a second swap.

World bind needs the owning node's storage adapter. Leave `node_id` unset so
the server uses the controller's own daemon, which
`nodestorage.IsLegacyLocalNodeID` recognizes for `""`/`local`/`default`. Do not
create a harness-owned node: it is treated as remote with no storage transport
and BindWorld is refused with `remote node storage unavailable`. This shipped
Compose fixture has no separately registered `is_local` DB node.

Node-owned server fixture directories are deliberately not world-writable.
Archive them with narrowly scoped fixture `sudo` reads and restore the archive
file's ownership to the runner; never chmod managed world data.

A backup record's size and digest are metadata, not proof that archived bytes
exist. Locate the real archive by its published digest, inspect its entries and
compare the archived `level.dat` bytes with the original. A missing or damaged
archive fails the check.

Two unexpired copies of a primary staging artifact are ambiguous, not absent.
Reject ambiguity before selecting any input path; only a genuinely missing
primary artifact may use the bounded reconstruction fallback.

# Read next

- [Final-candidate World Library swap qualification](world-library-swap-qualification.md)
