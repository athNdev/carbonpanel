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

BindWorld needs its own bounded deadline because a graceful stop may take 60s;
reconcile an interrupted call by replaying the same `request_id`, which returns
the original operation rather than a second swap.

Shipped Compose has no auto-registered local node row. The fixture registers a
node for `unix:///var/run/docker.sock` if absent and places both servers on its
real UUID via `node_id`, so the supported node-id and endpoint-generation path
is exercised; the fixture asserts `is_local` and matching server `nodeId`. Do
not bypass node IDs or endpoint-generation checks or edit binding source. If BindWorld reports
`remote node storage unavailable` for that real node UUID, the defect is in the
product's storage resolution (binder must resolve the node row, not only legacy
string IDs) and belongs to a separate product branch, not this qualification PR.

Archive ONLY `world` from the disposable server data parent and assert
`world/level.dat` in both archives before import; `stageFamilies` requires a
world folder at the archive root. A flat ZIP is a separate product gap.

Node-owned fixture dirs are not world-writable: use narrow fixture `sudo`
reads and restore archive ownership to the runner; never chmod managed data.

A backup record's size and digest are metadata, not proof that archived bytes
exist. Find the archive by published digest in this fixture's backup directory
only. The stable marker is its `level.dat`, captured before the swap rather
than a live file that `save-all flush` may rewrite. Compare the archive digest
and that archived marker before and after the swap; a missing, damaged or
changed archive fails the check.

Classify transport failures from Undici's `error.cause.code`, not the top-level
message. Retry only reads and idempotency-keyed mutations (`request_id` or
`requestId`) or deterministic fixture console commands. Replay only interrupted
transport with the same key; preserve HTTP/precondition errors. Require a bind
operation id and `outcome: success` before later assertions. A wrong-revision
replay needs structured `failed_precondition` plus `ErrBindRequestConflict` text
and unchanged restart identity, binding, marker and event count.

Duplicate unexpired primary artifacts are a hard error, never a fallback trigger.

# Read next

- [Final-candidate World Library swap qualification](world-library-swap-qualification.md)
