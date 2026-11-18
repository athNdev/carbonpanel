# ADR 0019: Versioned typed node-control contract

Status: Accepted

Date: 2026-11-18

## Context

A node in Carbon Panel is currently a database row describing a Docker Engine
API endpoint. `node.proto` exposes only operator CRUD and ping; there is no
agent identity, enrollment, version or capability field and no node-initiated
channel. The controller talks directly to each daemon through
`internal/docker.ClientPool`, which correctly fails closed for named nodes but
has no typed contract for a managed runtime.

The managed-node runtime (Track C) needs a shared contract before any agent,
daemon lifecycle or enrollment server can be built. The P0-GATE redaction work
(ADR 0018) established that contract responses must never carry secret material.

ADR number 0019 is used under the orchestrator's central allocation: 0014 is
the merged baseline admission ADR, 0015–0017 belong to other in-flight tracks,
0018 is node response redaction, and 0020+ remain available. Track C owns this
ADR file and supplies its index row text to the orchestrator, who alone writes
`knowledge/decisions/adrs.md`.

## Decision

1. Add a **separate** versioned contract file
   `proto/carbonpanel/v1/node_control.proto`. No other owner's proto file is
   modified; generated Go and TypeScript outputs come only from this file via
   the pinned `scripts/proto-generate.mjs`.
2. The contract models authenticated node identity by node ID, name, key
   fingerprint, credential/endpoint generations and daemon ID — never by a
   private key or token. `NodeTransport` has no legacy value, so the control
   contract cannot select a legacy endpoint.
3. One-time enrollment is bounded by TTL and lifecycle state. Responses carry
   only a hash fingerprint and metadata; the one-time token exists only in a
   request and is delivered by the P1+ enrollment server. This preserves the
   ADR 0018 rule that responses contain no secret material.
4. Per-node authorization claims are explicit (`scopes`, `object_id` equal to
   `node_id`, credential generation, issue/expiry). Capability negotiation and
   an explicit supported-version range make version skew an
   `UPGRADE_REQUIRED` outcome rather than a silent degradation.
5. Health and preflight reports are versioned, bounded and redacted.
   Operation envelopes carry idempotency keys, request hash, endpoint and
   credential generations, deadline, attempt and capability snapshot so
   deduplication and bounded contexts are part of the wire contract.
6. `DispatchOperation` is fail-closed: an unspecified, non-managed or
   unresolvable target returns an unavailable reason. There is no
   controller-local, legacy-endpoint or host-socket fallback.
7. P0 ships **contract and model only**: proto, generated code, DB metadata
   columns, migration and negative tests. No agent, daemon lifecycle or
   enrollment server is implemented in this decision's slice.

## Consequences

- Every new RPC is denied by default by the shared auth interceptor until a
  permission is explicitly mapped. The negative fixture asserts that even a
  broad `nodes:*` grant and an explicit no-auth admin are refused, so no
  handler is reachable.
- DB columns record transport, enrollment state and expiry, protocol version,
  capability snapshot/owner, credential generation and fingerprints. Existing
  and newly created nodes default to `legacy`/unenrolled; nothing is
  auto-enrolled and no secret is stored in these columns. The migration is
  idempotent with a column-only rollback and preserves node rows.
- The S4 node-principal contract remains shared with Track A; this ADR defines
  the wire shape and claims, while auth middleware ownership stays with Track A.
- W16 instance-owned storage resolution, W17 bounded streaming and W15
  admission boundaries are untouched. This contract does not select a Docker
  client, move world data or change admission.
- The wire-exposed credential clear/rotation RPC from ADR 0018 remains a
  separate coordinated follow-up with one proto owner.

## Verification

- `buf lint` and `buf format -d` are clean for the new file;
  `bun scripts/proto-generate.mjs --check` regenerates identical Go and TS.
- `internal/rpc/node_control_contract_test.go` exercises all nine RPCs through
  the real interceptor across five authentication scenarios and checks the
  generated descriptor for secret fields in every response message.
- `internal/db/node_control_contract_migration_test.go` applies, re-applies and
  rolls back the real migration against an owned on-disk SQLite database and
  verifies legacy backfill plus preservation of the existing node row.
- Touched-package `go test`/`go vet` and frontend `bun check`/`test`/`lint`
  are required before merge.
