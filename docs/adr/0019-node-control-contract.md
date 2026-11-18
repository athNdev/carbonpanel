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
3. One-time enrollment is bounded by TTL and lifecycle state. Enrollment uses
   a **node-generated high-entropy secret with digest-only server registration**
   and a signed proof of possession of the node's own key; the controller never
   emits a plaintext secret and never stores one. The exact scheme and its
   three authorization domains are specified below. This preserves the
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

## Enrollment provisioning, proof of possession and authorization domains

**Parent-review correction.** The P0 contract described a caller-declared
`key_fingerprint` and a `token` in `ClaimEnrollmentRequest` as if they proved
identity. A string fingerprint is metadata, not proof, and a captured token
could be replayed. That wording is superseded here: P1 must implement exactly
the scheme below before any node-control RPC is mounted in an implementation.

### Chosen scheme — node-generated secret, digest-only registration, CSR proof

The controller **never generates, displays, returns or stores a plaintext
enrollment secret**. The node generates the secret and proves it holds both the
secret and its own private key:

1. **Node side (before any controller call):** generate a CSPRNG secret
   `S` (>= 256 bits), a random salt, and a node key pair `K` (Ed25519 or P-256).
   Compute `digest = SHA-256(salt || S)`. Produce a CSR (or equivalent signed
   proof) carrying the node label and signing the enrollment context. Export a
   one-time enrollment bundle containing `salt`, `digest`, the CSR and an
   optional public-key fingerprint. Only `S` and `K`'s private half stay on the
   node.
2. **Operator domain:** an authenticated operator with node-management
   permission (`nodes:create`/`nodes:update`) obtains that bundle **out of
   band** from the node administrator and creates an enrollment with `salt`,
   `digest`, optional expected public-key fingerprint, TTL, required
   capabilities and protocol version. The server stores only the salt, digest,
   metadata and a fresh public `nonce`. `CreateEnrollmentResponse` contains
   metadata and the nonce only — never `S`, never a token. The operator's
   handoff of `S` to the node is controlled out-of-band provisioning; the
   controller is never the secret channel.
3. **One-time bootstrap domain:** the node opens a bounded enrollment session,
   then calls `ClaimEnrollment` **once** over a TLS-protected channel carrying
   `enrollment_id`, `nonce`, `S`, the CSR/signed proof, capability snapshot and
   a client assertion of the channel binding. The server independently derives
   the TLS channel binding from the connection and refuses a mismatch. It
   recomputes `SHA-256(stored_salt || S)` and compares in constant time; if an
   expected public-key fingerprint was registered it must match the CSR key.
   It verifies the CSR signature over the exact enrollment context (id, nonce,
   expiry, capabilities, channel binding). A caller-declared fingerprint is
   only metadata and is never used as proof.
4. **Atomic compare-and-consume:** all of the above plus issuance happens in a
   single DB transaction: lock the enrollment row, require `PENDING` and an
   unexpired TTL, verify every check, set the row consumed with `consumed_at`
   and the recorded channel binding, bind the node's key fingerprint, set
   `credential_generation = 1`, set `transport = managed_agent` and
   `enrollment_state = active`, and store only a digest of the issued
   credential. Any failure rolls back with no partial node or credential.
   Replay is refused because the state is no longer `PENDING`; generation and
   revocation mismatches are refused with distinct codes.

**Proto revision required in P1 (in this owned proto only):** add
`enrollment_secret_digest` and `enrollment_salt` (and optional
`node_public_key_fingerprint`) to `CreateEnrollmentRequest`; add `nonce`,
`csr_der`/signed proof and `channel_binding` assertion to
`ClaimEnrollmentRequest`; treat `key_fingerprint` as descriptive metadata,
never as an authentication factor. No response message may gain a plaintext
secret field. The descriptor no-secret test must be extended to cover these
fields. The merged P0 contract stays as the versioned baseline; P1 revises it
in its own branch with regenerated Go/TS.

### Three authorization domains — no implicit privilege crossing

| Domain | Actor and credential | Permitted | Explicitly forbidden |
| --- | --- | --- | --- |
| Operator provisioning | Authenticated human operator with `nodes:create`/`nodes:update` | Create enrollment, register out-of-band digest/salt, revoke enrollment or credential, read enrollment metadata | Cannot call node operation RPCs, cannot act as a node, never receives node private key or `S` |
| One-time bootstrap | Unauthenticated node holding `S` and `K`, on a TLS-bound bounded session | **Only** `ClaimEnrollment`, once, to obtain an enrolled credential | No general node RPC access, no read responses, no world/container/storage operations, no operator RPCs; `S` is never a credential |
| Enrolled node | Node principal with server-issued credential bound to `K`, `credential_generation`, and per-node scopes on its own `object_id` | Heartbeat, preflight/health report, operation ack, bounded dispatch reply for its own node | Other nodes' objects, operator enrollment management, product RPCs, legacy-endpoint paths |

There is no implicit crossing: a bootstrap secret cannot be reused as a node
credential, an enrolled-node credential cannot create or revoke enrollments,
and an operator identity cannot dispatch node operations. Revocation and
re-enrollment bump generation and never silently rebind a different daemon
(ADR 0004 / `ErrDaemonIdentity`).

### Rotating and revoking

Rotation issues a new credential with `credential_generation + 1`; a bounded
overlap accepts both old and new, then the old is dropped. Revocation is an
immediate server-side invalidation with a distinct error code. Credential
loss, daemon-ID change or node-ID change requires a fresh one-time enrollment
through the operator domain; no silent acceptance of a new key.

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
- A caller-declared key fingerprint, a token without proof of possession, or a
  bootstrap secret presented on another TLS channel can never enroll a node.
  Any implementation that accepts them is out of conformance with this ADR.
- **No success evidence may be claimed from an unimplemented stub.** P0 tests
  prove denial and contract shape only. P1 fixture tests prove the enrollment
  state machine; neither is deployment or live-node compatibility evidence.
  Only owned disposable lifecycle runs (P3+) may claim install/upgrade success.

## Verification

### Harmless fixture acceptance BEFORE any live or owned deployment-lifecycle work

P1 must add these tests with synthetic keys, fake TLS channels and an owned
in-memory/local DB fixture — no daemon, no host, no real credential, no live
node. They gate the start of P1 lifecycle work:

1. **Synthetic node key/CSR:** generate an ephemeral Ed25519/P-256 key and CSR
   in the fixture; assert the server verifies the signature over the exact
   enrollment context and accepts a correctly formed claim.
2. **Valid claim:** digest match, nonce match, unexpired PENDING enrollment,
   matching channel binding and registered key fingerprint produce one node
   with `credential_generation = 1`, `transport = managed_agent`,
   `enrollment_state = active`, and a stored credential digest only.
3. **Replay refusal:** a second claim with the same `S` and enrollment ID is
   refused because the row is consumed; generation is unchanged.
4. **Revoked / generation mismatch refusal:** revoked state, expired TTL and a
   stale generation each produce distinct refusal codes with no node mutation.
5. **Wrong-scope refusal:** a bootstrap secret presented to a node-operation
   RPC, an enrolled credential presented to `CreateEnrollment`/`Revoke`, and a
   node credential used for another node's `object_id` are all denied.
6. **Digest-only storage:** inspect the owned DB after a valid claim; no column
   contains `S` or credential plaintext, only salt/digest/fingerprint.
7. **No secret in read responses:** `GetNode`/`ListNodes` and all enrollment
   metadata responses contain no `S`, token, key or credential value.
8. **Atomicity:** inject a failure after verification but before commit and
   assert neither a node nor a consumed enrollment survives the rollback.

### P0 contract verification (shipped; denial and shape only)

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
