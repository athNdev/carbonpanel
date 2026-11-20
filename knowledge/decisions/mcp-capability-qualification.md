---
type: Decision
title: MCP capability qualification
description: A stable MCP tool catalog reports old-controller RPC gaps as structured capability errors instead of hiding tools or switching channels.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/mcp/catalog.go
  - internal/mcp/errors.go
  - internal/mcp/mcp_test.go
sources:
  - title: MCP stable build compatibility
    resource: https://carbonpanel.atlassian.net/browse/OSS-353
  - title: MCP operator guide
    resource: docs/carbon-panel/src/content/docs/guides/mcp-server.mdx
---

# Choice

Stable v1.0.40 predates `PlayerService`, `WorldService`, `WorldViewService` and
`ModBrowseService`. The MCP server does not detect the controller version,
default to a nightly channel, or remove tools for an older panel: clients must
always see the same catalog. Each declared tool instead records the minimum
panel build containing its RPC. Tools on the four new families are qualified
as `post-v1.0.40+fa23a8d73c181f9907633024d9023aead928324e`; the smoke tools are
read-only RPCs present in v1.0.40.

Only an `Unimplemented` response (including Connect's HTTP 404 to Unimplemented
mapping) or a `NotFound` whose message explicitly identifies a transport-level
missing procedure is a capability gap. The tool then returns `IsError` with a
structured `error.kind = capability_unavailable` payload naming the tool, the
exact procedure, the Connect code and the minimum build. Record the installed
controller build separately. A domain `NotFound` (a
missing server, file or world record) instead returns a `not_found` error and
never claims an upgrade is required. Permission and authentication denials use
their own stable error kinds. This never reports false success, never hides a
tool and never crashes the server.

# Verification

Fixtures run the stable read-only smoke path against a fake older panel and
assert that a post-stable tool returns the structured capability error for
`Unimplemented` and a transport-level `NotFound`. A separate fixture asserts a
domain `NotFound` is reported as `not_found`, a no-auth panel is refused even
with a well-formed token, and a hung backend fails within the bounded timeout.
They also assert that `tools/list` is identical on old and new panels. The
public package, packaged stdio workflow and controller compatibility require
separate evidence. API tokens inherit the owning account's grants.

# Read next

- [MCP server core](../architecture/mcp-server.md)
- [Fail-closed authorization](../invariants/authz-fail-closed.md)
