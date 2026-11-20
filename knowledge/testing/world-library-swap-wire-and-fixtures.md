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

Shipped Compose has no auto-registered local node row. The fixture registers a
node for `unix:///var/run/docker.sock` if absent and places both servers on its
real UUID via `node_id`, so the supported node-id and endpoint-generation path
is exercised. The fixture asserts the node is classified `is_local` and that
each created server reports that exact `nodeId`. Do not bypass normal node IDs
or endpoint-generation checks, and
do not edit world binding source from this harness. If BindWorld reports
`remote node storage unavailable` for that real node UUID, the defect is in the
product's storage resolution (binder must resolve the node row, not only legacy
string IDs) and belongs to a separate product branch, not this qualification PR.

Node-owned server fixture directories are deliberately not world-writable.
Archive them with narrowly scoped fixture `sudo` reads and restore the archive
file's ownership to the runner; never chmod managed world data.

A backup record's size and digest are metadata, not proof that archived bytes
exist. Locate the real archive by its published digest inside this fixture's
backup directory only, with the same narrow fixture privilege. The stable
original-world marker comes
from the `level.dat` inside that archive captured before the swap, not from a
mutable live file that `save-all flush` may rewrite. Compare the archive digest
and that archived marker before and after the swap; a missing, damaged or
changed archive fails the check.

Classify transport failures from Undici's `error.cause.code`, not the top-level
message. Retry only reads and idempotency-keyed mutations (`request_id` or
`requestId`) or deterministic fixture console commands. Replay only interrupted
transport with the same key; preserve HTTP/precondition errors. Require a bind
operation id and `outcome: success` before later assertions.

Duplicate unexpired primary artifacts are a hard error, never a fallback trigger.

# Read next

- [Final-candidate World Library swap qualification](world-library-swap-qualification.md)
