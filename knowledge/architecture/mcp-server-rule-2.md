---
type: Architecture
title: "MCP tool gateway — Rule (part 2)"
description: "The tool catalog is stable across controller builds. internal/mcp/catalog.go"
status: stable
owners: [oss-runtime]
code_refs: [cmd/carbonpanel-mcp, internal/mcp, internal/rpc/auth_interceptor.go, internal/rbac/mapping.go]
sources:
  - title: Parent concept
    resource: mcp-server.md
---
The tool catalog is stable across controller builds. `internal/mcp/catalog.go`
holds the pinned first-slice declarations and the 27-name mount list mirroring
`internal/rpc/server.go registerServices`; `catalog_data.go` is generated from
proto descriptors and the live RBAC map. The single generator also emits
`integration_catalog.go`, resolving duplicate tools by source precedence so
`list_files`/`get_file` and similar collisions are never double-registered. The
coverage drift test still enumerates the 245 mounted RPCs and fails on a
missing or extra declared tuple, a scope that no longer matches `internal/rbac`,
a changed descriptor fingerprint, an accidentally mounted `NodeControlService`,
or a missing `WorldViewService`. `family_content_descriptors.json` is compared
byte-for-byte by `TestContentDescriptorArtifact`, so it is pinned to LF in
`.gitattributes`: without the pin a Windows checkout with `core.autocrlf=true`
converts it to CRLF and fails the equivalence check with no descriptor drift.
Refresh the generated catalogue when a mounted RPC response changes. Its tool
descriptions identify the configured save folder on world-binding reads and
the provider's Minecraft versions on modpack release reads.
`family_audit_test.go` recomputes LISTED and
REACHABLE from live source, but VERIFIED comes only from executable invocation
evidence: a real `tools/call` over the SDK stdio wire must reach the typed
Connect method with exact typed-field assertions on ids, booleans, enums,
limits, bytes and repeated/map values, observe the real handler status and
exercise the resource scope (including a denied ungranted dp_ token). Negative
fixtures prove wrong fields, missing ids, conflicting arguments and out-of-
range bounds are refused before any typed RPC.

OSS-387 added an authenticated controller-side bounded-frame transport. The four
`scoped_stream_*` tools are advertised from the production `NewServer` wiring;
they move bytes only through `OpenStream`/`ReadStreamFrame`/`CloseStream` typed
RPCs behind the production auth interceptor. The gateway holds an opaque handle
and a cumulative SHA-256 only; panel/node storage is never shared. Every frame
and close is re-authorized with `files/read[server_id]` and bound to the exact
server the session was opened for. Session contexts are real bounded contexts
owned by the service (not `context.WithoutCancel`); frames are idempotent by
index, partial/over-cap reads terminate the session, and a bounded reaper plus
`Server.Shutdown -> FileService.Stop -> StopStreams` release handles on expiry
and shutdown. See [controller stream session semantics](../decisions/mcp-controller-stream-sessions.md).

The download lane adds the staged-download byte path for the four FileService
download RPCs and `surface:rest_download_stream`. `OpenStreamRequest.download_session_id`
lets the same controller-owned, bounded `ReadStreamFrame`/`CloseStream` RPCs
carry bytes from a file that `InitFileDownload`, `DownloadArchive` or
`ExportWorld` staged on the controller; the gateway holds no storage handle and
never uses a hand-rolled REST client. `download_get_file`,
`download_init_file`, `download_archive_bundle`,
`download_export_world_bundle` and `download_stream` are exported through
`DownloadToolSpecs`/`DownloadToolDescriptors` for the integration owner. See
[staged-download byte transport](../decisions/mcp-download-byte-transport.md).

