---
type: Decision
title: MCP controller stream session semantics
description: Bounded controller-owned stream sessions use service-owned deadlines, exact server binding, idempotent frames and terminal partial reads to avoid lost bytes and leaked handles.
status: stable
owners: [oss-runtime]
code_refs: [internal/rpc/services/file_stream.go, internal/mcp/integration_stream_transfer.go, internal/mcp/integration_stream_tools.go, internal/mcp/family_download_tools.go]
sources:
  - title: Controller-side MCP stream transport
    resource: https://carbonpanel.atlassian.net/browse/OSS-387
---

# Parts

- [MCP controller stream session semantics — Choice](mcp-controller-stream-sessions-choice.md) — The MCP gateway never opens panel/node storage. Bounded OpenStream,
- [MCP controller stream session semantics — Verification + Read next](mcp-controller-stream-sessions-verification.md) — Service fixtures prove exact server binding with no state change on refusal,
