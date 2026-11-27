# ADR 0058: Correlate operations without exposing Docker daemon IDs

- Status: Accepted
- Date: 2026-11-28
- Owners: oss-runtime
- Relates to: OSS-245 (operation journal diagnostics), ADR 0018 (node response
  redaction), ADR 0019 (typed node-control contract)

## Context

The lifecycle journal records the Docker daemon id accepted for a node endpoint
so a changed endpoint is not mistaken for the one an operation was issued
against. That value is a host fingerprint. It is useful inside the controller
for identity checks, but it is not needed by an operator to answer the actual
question: does this operation belong to this server and node, and is its
outcome still safe to retry?

The operation API, the server Operations panel and the support bundle already
expose no raw daemon id and no dedicated Docker host, socket or credential
field. The remaining raw daemon id on the CLI list and in internal projections
would still disclose a stable host fingerprint without helping the operator.

## Decision

Raw Docker daemon ids are not serialized on any operator-facing surface: the
`OperationService` wire, the Operations panel, the `-operations list` output or
`operations.json` in a support bundle. The daemon id stays internal to the
database and to identity-conflict checks in the journal.

Operator projections expose only non-secret correlation data:

- the server and node ids, endpoint generation, stored request key, durable
  phase and last-advance timestamps;
- a stable reason code describing why an outcome is what it is; and
- a derived correlation state of `matched`, `unmatched` or `unknown`.

Correlation is `unmatched` when the recorded node, endpoint generation or
observed daemon identity differs from the current node. It is `unknown` when
either side has no observed daemon identity, so the panel does not claim a
match. The correlation fields never carry the raw daemon id, host, socket or
credential, and no operator-facing projection serializes the raw daemon id.
Recorded error text keeps its existing credential redaction; hosts, sockets and
daemon messages in that text remain as evidence.

## Consequences

- An operator can join an operation to a server/node and decide whether to
  wait, retry with a new request id or release the lock without seeing a
  daemon-id fingerprint.
- The wire change is additive: existing clients ignore the new fields, and the
  daemon id was never part of the operator response.
- Legacy nodes that have not observed a daemon id report `unknown` correlation
  until an observation exists. That is the honest state, not a failure.
- The journal still compares the stored daemon id internally, so an endpoint
  change continues to conflict with an idempotency key without being disclosed.
