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

The pinned FIRST SLICE of 30 tools remains unchanged, but the gateway now
mounts the combined OSS-370 catalog: 197 tools covering all 200 SUPPORTED
operations from the OSS-375 audit matrix. The 30 first-slice declarations are
the highest-precedence source; family descriptors (server, content, admin),
stream typed equivalents and integration-owned additions are merged by
`internal/mcp/cataloggen` into one generated `exposedTools` list. The 58
excluded operations stay unmounted with exact source reasons and supported
equivalent flows in `family_audit_matrix.json`; free-form commands, node
enrollment/TLS dumps, secret bootstrap and third-party egress are never tools.
Tools are high-level, bounded workflows: mutations carry a `confirm=true` gate,
paths are instance-relative, and result limits are capped in Go as well as in
the schema.

Before tools are exposed, `Clients.Verify` checks that the controller has an
enabled auth provider and that the bearer token resolves to a concrete active
user. A well-formed token against a no-auth installation is refused, and every
call uses a bounded HTTP client timeout.

The tool catalog is stable across controller builds. `internal/mcp/catalog.go`
holds the pinned first-slice declarations and the 25-name mount list mirroring
`internal/rpc/server.go registerServices`; `catalog_data.go` is generated from
proto descriptors and the live RBAC map. The single generator also emits
`integration_catalog.go`, resolving duplicate tools by source precedence so
`list_files`/`get_file` and similar collisions are never double-registered. The
coverage drift test still enumerates the 220 reachable RPCs and fails on a
missing or extra declared tuple, a scope that no longer matches `internal/rbac`,
a changed descriptor fingerprint, an accidentally mounted `NodeControlService`,
or a missing `WorldViewService`. `family_audit_test.go` recomputes LISTED and
REACHABLE from live source, but VERIFIED comes only from executable invocation
evidence: a real `tools/call` over the SDK stdio wire must reach the typed
Connect method with mapped, bounded arguments, observe the real handler status
and exercise the resource scope (including a denied ungranted dp_ token).
Registration strings and quoted procedure names never count. The exclusion
policy is bound to the server before any tool is added, so audited-out tools
are never mounted; go-sdk replaces same-name tools (last wins), so registration
runs in reverse generator priority and a live `tools/list` metadata assertion
keeps it aligned. gRPC reflection (19 names) and the 24-name RBAC parity list
are deliberately not sources of truth.

Disposable fixtures connect a real MCP SDK client over the stdio wire format
to the in-process gateway; its tools call typed clients against `httptest`
Connect handlers wrapped in `rpc.NewAuthInterceptor`, with scoped API tokens.
The binary also has a subprocess test using the SDK `CommandTransport`.

# Read next

- [Capability qualification](../decisions/mcp-capability-qualification.md)
- [Fail-closed authorization](../invariants/authz-fail-closed.md)
- [RPC contract](rpc-contract.md)
