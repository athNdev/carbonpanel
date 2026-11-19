---
type: Architecture
title: MCP tool gateway
description: A stdio gateway exposes bounded, fail-closed operator workflows through generated Connect clients and a shared coverage drift gate.
status: stable
owners: [oss-runtime]
code_refs: [cmd/carbonpanel-mcp, internal/mcp, internal/rpc/auth_interceptor.go, internal/rbac/mapping.go]
sources:
  - title: MCP OSS server lane
    resource: https://carbonpanel.atlassian.net/browse/OSS-352
---

# Parts

- [MCP tool gateway — Rule](mcp-server-rule.md) — cmd/carbonpanel-mcp speaks MCP over stdio. It calls the controller only
- [MCP tool gateway — Rule (part 2)](mcp-server-rule-2.md) — The tool catalog is stable across controller builds. internal/mcp/catalog.go
- [MCP tool gateway — Rule (part 3)](mcp-server-rule-3.md) — The nine formerly PENDING operations are now mounted by production
- [MCP tool gateway — Read next](mcp-server-read-next.md) — Fail-closed authorization
