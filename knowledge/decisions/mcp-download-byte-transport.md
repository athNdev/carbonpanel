---
type: Decision
title: MCP staged-download byte transport
description: Download sessions reach the MCP gateway as bounded controller frames over typed RPCs, not through a gateway REST client or gateway filesystem.
status: stable
owners: [oss-runtime]
code_refs:
  - proto/carbonpanel/v1/file.proto
  - internal/rpc/services/file_stream.go
  - internal/mcp/family_download_tools.go
  - internal/mcp/family_download_descriptors.json
sources:
  - title: MCP download operations (OSS-370 follow-up)
    resource: https://carbonpanel.atlassian.net/browse/OSS-370
---

# Choice

`InitFileDownload`, `DownloadArchive` and `ExportWorld` stage a file in the
controller's owned temp directory and return only an opaque session id, filename
and authoritative size. The MCP gateway reads those bytes by passing the
download session id in a backward-compatible `OpenStreamRequest.download_session_id`
field and then using the **same** `ReadStreamFrame`/`CloseStream` typed RPCs as
node-path streams. The controller opens the staged temp file itself, keeps the
read handle on its side, and enforces that `server_id` matches the session's
immutable server and that the recording principal created the session. Every
frame is re-authorized by the production interceptor as `files/read[server_id]`;
frame size, declared total and gateway working set stay bounded by the W17 caps.

The browser/CLI REST surface `GET /api/v1/download/{sessionId}` is deliberately
**not** reproduced as a hand-rolled HTTP client in the gateway: that would add a
second product HTTP path and gateway-side byte staging. The MCP tool
`download_stream` is the composed typed byte path for the audited
`surface:rest_download_stream` operation; `download_get_file`,
`download_init_file` and the two bundle tools cover the other four pending
download operations. `DownloadToolSpecs` and `DownloadToolDescriptors` are
exported seams; the integration owner wires them into the shared catalog and
STRICT gate when all nine pending operations have real evidence. Until then the
publication gate stays RED.

# Verification

Real `tools/call` evidence over the SDK stdio wire drives a live FileService
behind `rpc.NewAuthInterceptor`: exact typed fields for all four RPCs and the
frame RPCs, byte-for-byte transfer with SHA-256/size, ZIP validation of owned
archive/world fixtures, cross-server and no-grant denials with zero bytes,
over-cap and under-cap refusals, digest mismatch, expired sessions, and
mid-frame cancellation that cancel-closes the owned controller handle. Service
fixtures cover staged-stream server, owner, cap and expiry binding directly.

# Read next

- [MCP server core](../architecture/mcp-server.md)
- [Controller stream session semantics](mcp-controller-stream-sessions.md)
- [Fail-closed authorization](../invariants/authz-fail-closed.md)
