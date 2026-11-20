---
type: Architecture
title: "MCP tool gateway — Rule (part 3)"
description: "The nine formerly PENDING operations are now mounted by production"
status: stable
owners: [oss-runtime]
code_refs: [cmd/carbonpanel-mcp, internal/mcp, internal/rpc/auth_interceptor.go, internal/rbac/mapping.go, scripts/mcp_build_only_packaged_smoke.py]
sources:
  - title: Parent concept
    resource: mcp-server.md
  - title: MCP artifact smoke runbook
    resource: ../../docs/runbook-mcp-smoke.md
---
The nine formerly PENDING operations are now mounted by production
`NewServer` / `registerCombinedTools` and carry executable `tools/call`
evidence, so `PendingInventory` is empty and the STRICT publication gate is
green (200/200 supported operations listed, reachable and verified). The same
gate now also runs in ordinary CI (`mcp-coverage` job) with a fail-closed
negative fixture and a real-binary stdio conformance smoke. The exact
retired inventory and its per-tool evidence map live in
`internal/mcp/integration_pending.go`; the committed plan is
`docs/plans/mcp-controller-side-stream-transport.md`. The hold mechanism is
still live: adding an unmounted operation back to `PendingInventory`
immediately turns the STRICT gate red again. No enabled-but-unusable tool is
advertised.
Registration strings, quoted procedure names and substring-of-JSON checks
never count. The exclusion policy is bound to the server before any tool is
added, so audited-out tools are never mounted; go-sdk replaces same-name tools
(last wins), so registration runs in reverse generator priority and a live
`tools/list` metadata assertion keeps it aligned. gRPC reflection (19 names)
and the 24-name RBAC parity list are deliberately not sources of truth.

Disposable fixtures connect a real MCP SDK client over the stdio wire format
to the in-process gateway; its tools call typed clients against `httptest`
Connect handlers wrapped in `rpc.NewAuthInterceptor`, with scoped API tokens.
The binary also has a subprocess test using the SDK `CommandTransport`.
For build-only container smoke, the report ties the stdio handshake, tool list
and scoped read call to the local image ID. The runner removes its temporary
container, network and automatically tagged images. Cleanup failure fails the
smoke and preserves the report.
