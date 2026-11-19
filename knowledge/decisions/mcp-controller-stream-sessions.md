---
type: Decision
title: MCP controller stream session semantics
description: Bounded controller-owned stream sessions use service-owned deadlines, exact server binding, idempotent frames and terminal partial reads to avoid lost bytes and leaked handles.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/rpc/services/file_stream.go
  - internal/mcp/integration_stream_transfer.go
  - internal/mcp/integration_stream_tools.go
sources:
  - title: Controller-side MCP stream transport
    resource: https://carbonpanel.atlassian.net/browse/OSS-387
---

# Choice

The MCP gateway never opens panel/node storage. Bounded `OpenStream`,
`ReadStreamFrame` and `CloseStream` RPCs carry frames through the production
auth interceptor; ownership comes from the authenticated token and every RPC
is re-authorized against the live `files/read[server_id]` scope. A session is
also bound to the exact server named at open, so a matching owner cannot use a
handle through a request scoped to a different server.

Each session owns a real bounded context derived from the service job context.
It is deliberately independent of the opening HTTP request (that request ends
before the first frame) but is not `context.WithoutCancel`: it has a TTL
deadline and is canceled once on close, expiry, terminal failure or shutdown.
The declared `total_size` / `size` is an admission **upper bound**, not an exact
count; the authoritative final size is `ReadStreamFrameResponse.offset` on
`done=true`, and composed commit uses those actual bytes, never the declared
bound. Frames are idempotent by `frame_index` via a small bounded cache, so a
caller canceled during a remote read can retry the same index without losing a
frame.
A read that returns bytes together with a non-EOF error, or that would exceed
the declared/staged cap, terminates the session and closes the handle rather
than returning a retryable error at an advanced offset. A bounded reaper evicts
expired sessions; `Server.Shutdown -> FileService.Stop -> StopStreams` cancels
session contexts before waiting on in-flight reads so shutdown unblocks a
blocked remote frame. The registry lock is never held across a storage read;
only the affected session is serialized.

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

# Read next

- [MCP server core](../architecture/mcp-server.md)
- [Fail-closed authorization](../invariants/authz-fail-closed.md)
