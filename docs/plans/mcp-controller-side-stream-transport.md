# Follow-up plan: authenticated controller-side streaming transport for MCP

Status: **PENDING**, not covered by PR531. This is the committed follow-up
issue/plan for the path-B hold. No full-coverage or publication claim is made
while these operations are pending.

## Why pending

The standalone `carbonpanel-mcp` binary talks to the controller only through
generated Connect RPCs. It does not own panel/node storage and must never
share that storage with the gateway process. The earlier `scoped_stream_*`
prototype held byte data inside the gateway and injected a local
`storageworker.StreamingFS`; that cannot be a production path. Advertising
those tools would mount tools that always fail with `unavailable`, so they are
absent from the generated catalog and from `NewServer`.

## Exact pending inventory

`internal/mcp/integration_pending.go` is authoritative: 9 operations
(7 RPCs + 2 non-RPC surfaces) covering `DownloadArchive`, `ExportWorld`,
`GetFile`, `InitFileDownload`, `SaveUploadedFile`, `InitUpload`,
`UploadChunk`, `rest_upload_stream`, `rest_download_stream`.

## Required work

1. Add controller-side authenticated streaming RPCs that carry bounded frames
   (256 KiB W17 bound, cumulative SHA-256, 16 handles/session, expiry) through
   the existing `rpc.NewAuthInterceptor`; ownership is derived from the caller's
   dp_ token, never from a tool argument.
2. Fresh server-scoped write authorization (`files:create[server_id]`) at
   commit, with upload session ownership and consumption limits (PR537).
3. Replace `context.WithoutCancel` with a bounded session context that keeps
   deadlines and ties cancellation to the owning request/session.
4. Define and test the session policy on permission change: re-authorize each
   frame or a short-lived lease with explicit TTL; refused mid-session with
   zero writes/bytes.
5. Add generated descriptors/RBAC scopes, regenerate the catalog, and drive all
   9 operations through the same executable `tools/call` evidence harness.
6. Only then remove entries from `PendingInventory` and claim full coverage.

## Size contract (decided)

`OpenStreamRequest.total_size` and the echoed `OpenStreamResponse.size` are an
**admission upper bound**, not an exact byte count. The controller refuses to
read past the bound, but the source may be shorter. The authoritative final
byte count is `ReadStreamFrameResponse.offset` on the frame with `done=true`;
its `sha256` is computed over exactly those bytes. Composed upload/commit uses
that actual count (`len(accumulated frames)`), never the declared bound. A
declared size of `0` is valid for opening and produces a `done` frame with the
empty digest, but the composed commit explicitly refuses a zero-byte stream.

Rationale: callers often declare a cap rather than a precomputed size; treating
it as exact would produce false `Done` failures for legitimate short sources.
Deriving the actual size from transferred bytes keeps SHA, `Done` and commit
honest. `TestIntegrationControllerStreamSizeContract` drives a 64 KiB source
declared as 1 MiB through real `tools/call` and asserts the actual size/SHA and
the composed upload size, plus the zero-byte refusal.
