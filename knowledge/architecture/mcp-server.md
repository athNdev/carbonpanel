---
type: Architecture
title: MCP tool gateway
description: A stdio gateway exposes bounded, fail-closed operator workflows through generated Connect clients and a shared coverage drift gate.
status: stable
owners: [oss-runtime]
code_refs:
  - cmd/carbonpanel-mcp
  - internal/mcp
  - internal/rpc/auth_interceptor.go
  - internal/rbac/mapping.go
sources:
  - title: MCP OSS server lane
    resource: https://carbonpanel.atlassian.net/browse/OSS-352
---

# Rule

`cmd/carbonpanel-mcp` speaks MCP over stdio. It calls the controller only
through generated `carbonpanelv1connect.New<Service>Client` types; there is no
hand-rolled product HTTP path. An `dp_…` API token is read from
`CARBONPANEL_API_TOKEN` (or `CARBONPANEL_TOKEN`) and the controller base URL from
`CARBONPANEL_URL`; both are required, and the token is sent as a Bearer header
by a Connect unary interceptor. Missing credentials stop startup rather than
falling back to anonymous access, session cookies or the no-auth bypass.

This is a FIRST SLICE of 30 tools; inventory coverage is not implemented API
coverage, and the remaining supported families stay tracked under OSS-370.
Tools are high-level, bounded workflows: lifecycle with a `confirm=true` gate,
player lookup, world library/view, backup list and restore, and file browse and
read. There are no free-form command, node discovery, enrollment, secret,
WebSocket or write-file tools in this slice. Paths are instance-relative and
result limits are capped in Go as well as described in the schema.

Before tools are exposed, `Clients.Verify` checks that the controller has an
enabled auth provider and that the bearer token resolves to a concrete active
user. A well-formed token against a no-auth installation is refused, and every
call uses a bounded HTTP client timeout.

The tool catalog is stable across controller builds. `internal/mcp/catalog.go`
holds the declared tool tuples and the 25-name mount list mirroring
`internal/rpc/server.go registerServices`; `catalog_data.go` is generated from
proto descriptors and the live RBAC map. The coverage drift test enumerates the
220 reachable RPCs from that shared list and fails on a missing or extra
declared tuple, a scope that no longer matches `internal/rbac`, a changed
descriptor fingerprint, an accidentally mounted `NodeControlService`, or a
missing `WorldViewService`. gRPC reflection (19 names) and the 24-name RBAC
parity list are deliberately not sources of truth.

Disposable fixtures connect a real MCP SDK client over the stdio wire format
to the in-process gateway; its tools call typed clients against `httptest`
Connect handlers wrapped in `rpc.NewAuthInterceptor`, with scoped API tokens.
The binary also has a subprocess test using the SDK `CommandTransport`.

# Read next

- [Capability qualification](../decisions/mcp-capability-qualification.md)
- [Fail-closed authorization](../invariants/authz-fail-closed.md)
- [RPC contract](rpc-contract.md)
