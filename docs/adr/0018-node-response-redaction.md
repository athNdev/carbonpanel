# ADR 0018: Redact node TLS material from read responses

Status: Accepted

Date: 2026-11-18

## Context

`dbNodeToProto` in `internal/rpc/services/node.go` copied `Node.TLSCACert`,
`Node.TLSCert` and `Node.TLSKey` verbatim into the UI-bound `v1.Node` proto
message. `ListNodes`, `GetNode`, `CreateNode` and `UpdateNode` all use that mapper,
so any principal with `nodes/read` (global or object-scoped) received the Docker
daemon's private TLS key material. This is a direct secret-disclosure path, not a
degraded-mode concern.

The console's node editor also reads those fields into its edit form and sends
them back through `UpdateNode`. Once responses are redacted, empty TLS strings
would be interpreted by `UpdateNode` as a deliberate clear, destroying stored
credentials and bumping `EndpointGeneration`, which invalidates cached Docker
pool clients for that node.

ADR number 0018 is used under the orchestrator's central allocation: 0014 is the
merged baseline admission ADR, and 0015–0017 are allocated to other in-flight
tracks. This record is owned by the same Track C writer that owns the code and
the `knowledge/decisions/adrs.md` index row.

## Decision

1. `dbNodeToProto` does not populate `tls_ca_cert`, `tls_cert` or `tls_key`. The
   fields remain declared in `node.proto` for wire compatibility and serialize
   empty. This is a response-mapper change only.
2. `UpdateNode` treats an empty TLS string as **unchanged**, never as a clear. A
   non-empty value is required to replace a credential, and the endpoint
   generation is bumped only for a genuine host/TLS change.
3. Server-side consumers are unaffected: `internal/docker.ClientPool` reads TLS
   material from the stored database row, not from the RPC response.
4. A future privileged credential-view operation is a separate, explicitly
   authorized RPC with its own fail-closed negative tests. It does not reopen
   `nodes/read`.

## Rotation and reset policy

Retiring empty-field clearing prevents accidental credential loss, but it must
not leave old TLS material stranded forever. Two **named, distinct** operator
actions exist:

| Action | Mechanism | Permission | Effect |
| --- | --- | --- | --- |
| `Replace TLS credentials` | Ordinary `UpdateNode` with **non-empty** PEM values | `nodes:update` on the target node | Replaces the supplied fields; bumps `EndpointGeneration`; drops the cached pool client |
| `Clear TLS credentials` | Dedicated named action `ClearNodeTLSCredentials`, never an empty-string edit | `nodes:update` on the target node, **not** `nodes:read` | Clears only `tls_ca_cert`, `tls_cert`, `tls_key` on one named node; bumps `EndpointGeneration`; drops the cached pool client |

The clear action is deliberately narrow and fail-closed:

- **Explicit target confirmation:** the caller supplies the target node's current
  name and the exact phrase `CLEAR TLS CREDENTIALS`; a mismatch refuses the
  action. It can never be triggered by a silent form echo or an empty field.
- **Authenticated operator + permission:** the action requires a non-empty
  operator identity and a node-management (`nodes:update`) authorization
  supplied by the permission-enforcing boundary. `nodes:read` alone is never
  sufficient. Auth middleware ownership stays with Track A (S4).
- **Audit:** a named log/audit entry records the operator, node ID, generation
  before and after, and the action name. Credential values are never logged.
- **Generation bump and invalidation:** a successful clear changes the endpoint
  identity and drops the pooled Docker client, matching a real TLS change.
- **Scope:** one named node and three credential fields only. No world, backup,
  database, daemon data root, host service or other node is touched. There is no
  bulk or implicit clear.

P0-GATE status: the server-side named action and its harmless fixture land in
this PR so the contract is real and testable. **Wire exposure is a separate,
coordinated follow-up RPC** because proto/generated-file work is gated until
P0-GATE and both halves (server plus the PR510 `node-settings.svelte` change)
ship. The UI writer must not implement clearing via empty TLS fields in the
meantime; the orchestrator records the follow-up RPC with one owner for the
proto and generated outputs.

## Consequences

- The console build that treats empty/redacted TLS fields as "unchanged" must
  ship with or before this change; the integrated matrix records that ordering.
  The frontend lane owner owns `node-settings.svelte`; this ADR does not edit it.
- Empty strings can no longer be used to clear stored TLS credentials through
  `UpdateNode`. Disabling TLS remains a separate `tls_enabled` change; complete
  credential removal goes only through the named `Clear TLS credentials` action
  defined above, never through an ordinary edit.
- Existing remote TLS endpoint nodes keep working because the panel uses the
  stored row for daemon connections. No world, backup, daemon or node data is
  touched.
- Response shape is wire-compatible: no proto field is removed, so consumers
  that ignore TLS fields keep working without a generated-code change.

## Verification

- `internal/rpc/services/node_redaction_test.go` asserts that `CreateNode`,
  `GetNode` and `ListNodes` responses, their protojson wire output, formatted
  values and service logs contain none of the synthetic TLS sentinels while
  non-secret fields remain, and that stored TLS bytes are unchanged.
- The no-round-trip fixture echoes a redacted response into `UpdateNode` with a
  non-secret edit and asserts stored credentials and `EndpointGeneration` are
  unchanged; it also asserts a genuine non-empty replacement still works.
- `internal/rpc/node_redaction_test.go` exercises the generated `NodeService`
  through the real auth interceptor with a global `nodes/read` principal and
  inspects the raw HTTP response body for sentinel leakage.
- `internal/rpc/services/node_tls_reset_test.go` proves that only the named
  `ClearNodeTLSCredentials` action can remove stored material: ordinary edits,
  empty-string echoes and omitted fields never clear, while the action with the
  correct operator, `nodes:update` authorization, target name and confirmation
  phrase clears only that node's TLS fields, bumps the generation and records an
  audit entry. Missing confirmation, wrong target name or missing node-update
  authorization are refused.
