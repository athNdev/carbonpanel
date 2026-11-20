---
type: Decision
title: "MCP controller stream session semantics — Verification + Read next"
description: "Service fixtures prove exact server binding with no state change on refusal,"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: mcp-controller-stream-sessions.md
---
# Verification

Service fixtures prove exact server binding with no state change on refusal,
TTL eviction with handle close, registry lock isolation during a blocked read,
same-index retry after caller cancellation, terminal behavior for short reads
with `io.ErrUnexpectedEOF` and a custom error, over-cap termination, in-flight
open admission caps, and clean `Stop` release. A runtime fixture drives a real
`Server.Shutdown` with a blocked remote read and observes the context cancel,
read unblock, handle close and session eviction. The tools/call fixture proves
307 KiB framed transfer with SHA equality and read-only/revoked/cross-owner/
cross-server refusals with zero writes.

The packaged smoke job repeats the stream round trip and refusals through the
OCI entrypoint by immutable local image ID. The fixture mounts generated auth,
file and upload handlers behind the production authorization interceptor. It
owns its database and file tree, and the MCP container reaches the fixture
over the Linux runner's loopback without data or Docker socket mounts.

Staged downloads reuse this machinery: `OpenStreamRequest.download_session_id`
opens the controller temp file through the same registry, owner/server binding
and frame/SHA contract instead of a node path. See
[staged-download byte transport](mcp-download-byte-transport.md).

# Read next

- [MCP server core](../architecture/mcp-server.md)
- [Fail-closed authorization](../invariants/authz-fail-closed.md)
