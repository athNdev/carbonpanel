---
type: Architecture
title: "MCP tool gateway — Rule"
description: "cmd/carbonpanel-mcp speaks MCP over stdio. It calls the controller only"
status: stable
owners: [oss-runtime]
code_refs: [cmd/carbonpanel-mcp, internal/mcp, internal/rpc/auth_interceptor.go, internal/rbac/mapping.go]
sources:
  - title: Parent concept
    resource: mcp-server.md
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
mounts the combined OSS-370 catalog: 212 tools covering 201 supported operations
(0 PENDING) from the OSS-375 audit matrix. The byte-transport lanes add five
download tools and four upload/commit tools to the earlier combined set. The 30 first-slice declarations are
the highest-precedence source; family descriptors (server, content, admin),
stream typed equivalents and integration-owned additions are merged by
`internal/mcp/cataloggen` into one generated `exposedTools` list. The 77
excluded operations stay outside the tool list with exact source reasons and supported
equivalent flows in `family_audit_matrix.json`; free-form commands, node
enrollment/TLS dumps, secret bootstrap and third-party egress are never tools.
The staged config read tool projects rollout metadata and drops saved config
payloads. It requires a concrete server grant and bounds the response size.
Tools are high-level, bounded workflows: mutations carry a `confirm=true` gate,
paths are instance-relative, and result limits are capped in Go as well as in
the schema.

Before tools are exposed, `Clients.Verify` checks that the controller has an
enabled auth provider and that the bearer token resolves to a concrete active
user. A well-formed token against a no-auth installation is refused, and every
call uses a bounded HTTP client timeout.

