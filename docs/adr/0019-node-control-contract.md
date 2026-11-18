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

### Chosen scheme — node-generated secret, REQUIRED approved-key binding, challenge-bound claim

The controller **never generates, displays, returns or stores a plaintext
enrollment secret**. `S` originates on the node and stays there; the operator
never handles it. The operator approves a **specific public key** out of band,
and proof of possession must come from that exact key. Two distinct signature
artifacts are used: a pre-registration CSR that merely carries the approved
key, and a **fresh claim signature made after the server issues a challenge**.

1. **(a) Pre-registration bundle, generated on the node:** generate a CSPRNG
   secret `S` (>= 256 bits), a random salt, and a node key pair `K` (Ed25519 or
   P-256). Compute `digest = SHA-256(salt || S)`. Produce `CSR_PRE`, signed by
   `K`, carrying the node label, salt, digest and the SPKI of `K`. The one-time
   bundle exported to the node administrator contains `salt`, `digest`,
   `CSR_PRE`, `approved_public_key_spki` (DER SPKI) and
   `approved_key_fingerprint` (SHA-256 of the SPKI). `CSR_PRE` is registration
   material only — it is **not** the claim proof.
2. **Operator domain:** an authenticated operator with `nodes:create`/
   `nodes:update` registers that bundle out of band. The following fields are
   **REQUIRED**: `approved_public_key_spki`, `approved_key_fingerprint`,
   `enrollment_secret_digest`, `enrollment_salt`, `node_id`, TTL, required
   capabilities and protocol version. The server stores the approved SPKI,
   fingerprint, salt, digest and metadata only — never `S`. The approved key is
   operator-sanctioned identity material, not a caller assertion.
3. **Bounded challenge step (how the node obtains the nonce):** after operator
   registration, the node calls a new **unauthenticated but bounded**
   `BeginEnrollmentChallenge(enrollment_id)` RPC. The server rate-limits it,
   requires a `PENDING`, unexpired enrollment, issues a single-use
   `challenge_id` + random `server_nonce` with a short TTL, and returns the
   `node_id`, enrollment `expires_at` and the server-observed channel context
   reference. No secret is returned. A stale or reused nonce is refused.
4. **(b) Fresh claim signature:** the node computes
   `SIG_CLAIM = Sign_K("carbon-panel-enroll-v1" || node_id || enrollment_id ||
   challenge_id || server_nonce || expires_at || canonical_capabilities ||
   server_observed_channel_context)` and calls `ClaimEnrollment` **once** with
   `S`, `challenge_id`, `SIG_CLAIM`, capabilities and a channel-binding
   assertion. The server:
   - requires the approved SPKI/fingerprint to be present and equal to the key
     in `CSR_PRE` and the key that produced `SIG_CLAIM` — **a signature from
     any other key is refused even when `S` is valid**;
   - verifies `SIG_CLAIM` with the approved key over the exact context above;
   - recomputes `SHA-256(stored_salt || S)` and compares in constant time;
   - requires the challenge nonce to be current, unused and unexpired;
   - independently derives the channel binding and refuses a mismatch.
   A caller-declared fingerprint is metadata only and never an authentication
   factor.
5. **Atomic compare-and-consume:** all checks plus issuance happen in one DB
   transaction: lock the enrollment row, require `PENDING`/unexpired, verify
   every check, consume the challenge and enrollment with timestamps and the
   recorded channel binding, bind the approved key fingerprint, set
   `credential_generation = 1`, `transport = managed_agent`,
   `enrollment_state = active`, and store only a digest of the issued
   credential. Any failure rolls back completely. Replay, revoked state,
   expired TTL, stale nonce and generation mismatch get distinct refusals.

**Proto revision required in P1 (in this owned proto only):** add REQUIRED
`approved_public_key_spki`, `approved_key_fingerprint`,
`enrollment_secret_digest`, `enrollment_salt` and `node_id` to
`CreateEnrollmentRequest`; add the bounded `BeginEnrollmentChallenge` RPC with
challenge request/response messages; add `challenge_id`, `fresh_claim_signature`,
`capabilities` and a `channel_binding` assertion to `ClaimEnrollmentRequest`;
retain `CSR_PRE` only as registered material. Treat the existing
`key_fingerprint` field as descriptive metadata. No response may gain a
plaintext secret field; the descriptor no-secret test must cover all new
fields. The merged P0 contract stays the versioned baseline; P1 revises it with
regenerated Go/TS.

### Enrolled credential delivery and TLS channel binding

**Chosen credential form:** the controller issues a **signed public X.509
client certificate** bound to the approved node public key, with node ID,
per-node scopes, `credential_generation`, `notBefore`/`notAfter` and a serial
in its extensions/subject. The node's private key **never leaves the node**.
`ClaimEnrollmentResponse` returns only that public certificate, the issuing CA
pin and generation/expiry metadata — no secret, token or private key in any
response or read RPC. Enrolled operations authenticate with **mTLS**: the node
presents the certificate and proves possession of the approved private key in
the TLS handshake itself. The server stores only certificate serial/fingerprint
and generation, not a bearer secret. Rotation issues a new certificate with
generation + 1; revocation invalidates the old serial immediately and is
durable across restart.

**Channel-binding topology policy (server-derived or explicit refusal):**

| Topology | Controller visibility | Decision |
| --- | --- | --- |
| Direct TLS to the controller (trusted termination at controller) | Full TLS handshake | **Supported.** Binding derived from the controller's own TLS connection (exporter/client-cert fingerprint). |
| L4 TLS pass-through / TCP proxy with PROXY protocol v2; TLS still terminates at controller | Full TLS handshake | **Supported.** Binding derived locally; PROXY protocol supplies address only and is not treated as identity. |
| Trusted TLS-terminating reverse proxy configured in the controller | Plaintext HTTP at controller | **Supported only** when the proxy is explicitly configured as trusted and forwards a short-lived binding attestation signed with a controller-configured key (proxy-validated client-certificate fingerprint plus exporter value). The controller verifies the signature and freshness. A client-supplied header or assertion alone is never accepted. |
| Untrusted/unknown proxy, plain HTTP, or ambiguous/mixed chain | No verifiable client TLS context | **REFUSED** with `UNSUPPORTED_TLS_TOPOLOGY`. No enrollment and no channel-bound operation proceeds. |

The `channel_binding` field in a request is only a client assertion; the
controller always compares it against its own derived value or a verified
trusted-proxy attestation. It is never trusted on its own. Panel TLS and
reverse-proxy artifacts are owned by Track A/B; Track C does not invent a
second TLS stack and must refuse topologies it cannot verify.

### Three authorization domains — no implicit privilege crossing

| Domain | Actor and credential | Permitted | Explicitly forbidden |
| --- | --- | --- | --- |
| Operator provisioning | Authenticated human operator with `nodes:create`/`nodes:update` | Create enrollment, register out-of-band digest/salt, revoke enrollment or credential, read enrollment metadata | Cannot call node operation RPCs, cannot act as a node, never receives node private key or `S` |
| One-time bootstrap | Unauthenticated node holding `S` and `K`, on a TLS-bound bounded session, after `BeginEnrollmentChallenge` | **Only** `BeginEnrollmentChallenge` and one `ClaimEnrollment`, to obtain a signed public client certificate | No general node RPC access, no read responses, no world/container/storage operations, no operator RPCs; `S` is never a credential and never reaches the operator |
| Enrolled node | Node principal authenticating with a server-signed public client certificate bound to the approved node key, proven by mTLS, with `credential_generation` and per-node scopes on its own `object_id` | Heartbeat, preflight/health report, operation ack, bounded dispatch reply for its own node | Other nodes' objects, operator enrollment management, product RPCs, legacy-endpoint paths, unsupported TLS topologies |

There is no implicit crossing: a bootstrap secret cannot be reused as a node
credential, an enrolled-node credential cannot create or revoke enrollments,
and an operator identity cannot dispatch node operations. Revocation and
re-enrollment bump generation and never silently rebind a different daemon
(ADR 0004 / `ErrDaemonIdentity`).

### Rotating and revoking

Rotation issues a new signed client certificate with
`credential_generation + 1`; a bounded overlap accepts both old and new
serials, then the old is revoked. Revocation is an immediate, **durable**
server-side invalidation with a distinct error code and must survive controller
restart. Credential loss, daemon-ID change or node-ID change requires a fresh
one-time enrollment through the operator domain; no silent acceptance of a new
key. Generation and revocation state are persisted with the node row and are
covered by a restart fixture before any lifecycle work.

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
- A missing operator-approved public key, a signature from a key other than the
  approved key, a pre-registration CSR used as the claim proof, a caller-declared
  fingerprint, a stale/replayed nonce, or a bootstrap secret presented on
  another TLS channel can never enroll a node. Any implementation that accepts
  them is out of conformance with this ADR.
- A topology the controller cannot verify is refused, not silently trusted.
  Client-supplied binding headers/assertions alone never satisfy channel
  binding, and no read response ever carries a secret or credential private key.
- **No success evidence may be claimed from an unimplemented stub.** P0 tests
  prove denial and contract shape only. P1 fixture tests prove the enrollment
  state machine; neither is deployment or live-node compatibility evidence.
  Only owned disposable lifecycle runs (P3+) may claim install/upgrade success.

## Verification

### Harmless fixture acceptance BEFORE any live or owned deployment-lifecycle work

P1 must add these tests with synthetic keys, fake TLS channels and an owned
in-memory/local DB fixture — no daemon, no host, no real credential, no live
node. They gate the start of P1 lifecycle work:

1. **Synthetic key, pre-registration CSR and fresh signature:** generate an
   ephemeral Ed25519/P-256 key; assert `CSR_PRE` is accepted only as registered
   material and that a fresh `SIG_CLAIM` by the approved key over node ID,
   enrollment ID, challenge ID/nonce, expiry, capabilities and verified channel
   context is required for a valid claim. Reusing `CSR_PRE` as the claim proof
   is refused.
2. **Valid claim:** required approved SPKI/fingerprint present, digest match,
   current challenge nonce, unexpired PENDING enrollment, server-derived
   channel binding match and approved-key signature produce one node with
   `credential_generation = 1`, `transport = managed_agent`,
   `enrollment_state = active`, and a signed public client certificate only.
3. **Mismatched approved key:** a valid `S` and digest but a signature from a
   different key — including a self-consistent CSR from that key — is refused;
   no node is created and the challenge is not consumed.
4. **Stale/replayed nonce:** a claim with a reused, expired or unknown
   `challenge_id`/`server_nonce` is refused with a distinct code, even when the
   digest and approved key are correct.
5. **Replay refusal:** a second claim with the same `S` and enrollment ID is
   refused because the row is consumed; generation is unchanged.
6. **Revoked / generation mismatch refusal:** revoked state, expired TTL and a
   stale generation each produce distinct refusal codes with no node mutation.
7. **Wrong-scope and preflight-scope refusal:** a bootstrap secret presented to
   a node-operation RPC, an enrolled certificate presented to
   `CreateEnrollment`/`Revoke`, a node credential used for another node's
   `object_id`, and a preflight report carrying scopes beyond its own node are
   all denied. An unsupported TLS topology is refused with
   `UNSUPPORTED_TLS_TOPOLOGY`.
8. **Digest-only storage and no secret in responses:** inspect the owned DB
   after a valid claim; no column contains `S`, a private key or credential
   plaintext — only salt, digest, approved SPKI/fingerprint and certificate
   serial. `GetNode`/`ListNodes` and all enrollment metadata responses contain
   no secret value.
9. **Atomicity:** inject a failure after verification but before commit and
   assert neither a node nor a consumed enrollment survives the rollback.
10. **Durable rotation:** rotate to `credential_generation + 1`, revoke the old
    certificate serial, close and reopen the owned DB, then assert the new
    generation and revocation still apply and the old serial is still refused.

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
