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
4. A future privileged credential-view or credential-replacement operation is a
   separate, explicitly authorized RPC with its own fail-closed negative tests.
   It does not reopen `nodes/read`.

## Consequences

- The console build that treats empty/redacted TLS fields as "unchanged" must
  ship with or before this change; the integrated matrix records that ordering.
  The frontend lane owner owns `node-settings.svelte`; this ADR does not edit it.
- Empty strings can no longer be used to clear stored TLS credentials through
  `UpdateNode`. Disabling TLS remains a separate `tls_enabled` change; complete
  credential removal belongs to a future explicit operation.
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
