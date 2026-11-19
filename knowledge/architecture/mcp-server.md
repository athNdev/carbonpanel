---
type: Architecture
title: MCP tool gateway
description: A stdio gateway exposes bounded, fail-closed operator workflows through generated Connect clients and a shared coverage drift gate.
status: stable
owners: [oss-runtime]
code_refs:
  - cmd/carbonpanel-mcp
  - internal/mcp
  - internal/rpc/auth_interceptor.go
  - internal/rbac/mapping.go
sources:
  - title: MCP OSS server lane
    resource: https://carbonpanel.atlassian.net/browse/OSS-352
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
mounts the combined OSS-370 catalog: 211 tools covering 201 SUPPORTED operations
(0 PENDING) from the OSS-375 audit matrix. The byte-transport lanes add five
download tools and four upload/commit tools to the earlier combined set. The 30 first-slice declarations are
the highest-precedence source; family descriptors (server, content, admin),
stream typed equivalents and integration-owned additions are merged by
`internal/mcp/cataloggen` into one generated `exposedTools` list. The 61
excluded operations stay unmounted with exact source reasons and supported
equivalent flows in `family_audit_matrix.json`; free-form commands, node
enrollment/TLS dumps, secret bootstrap and third-party egress are never tools.
Tools are high-level, bounded workflows: mutations carry a `confirm=true` gate,
paths are instance-relative, and result limits are capped in Go as well as in
the schema.

Before tools are exposed, `Clients.Verify` checks that the controller has an
enabled auth provider and that the bearer token resolves to a concrete active
user. A well-formed token against a no-auth installation is refused, and every
call uses a bounded HTTP client timeout.

The tool catalog is stable across controller builds. `internal/mcp/catalog.go`
holds the pinned first-slice declarations and the 27-name mount list mirroring
`internal/rpc/server.go registerServices`; `catalog_data.go` is generated from
proto descriptors and the live RBAC map. The single generator also emits
`integration_catalog.go`, resolving duplicate tools by source precedence so
`list_files`/`get_file` and similar collisions are never double-registered. The
coverage drift test still enumerates the 235 reachable RPCs and fails on a
missing or extra declared tuple, a scope that no longer matches `internal/rbac`,
a changed descriptor fingerprint, an accidentally mounted `NodeControlService`,
or a missing `WorldViewService`. `family_content_descriptors.json` is compared
byte-for-byte by `TestContentDescriptorArtifact`, so it is pinned to LF in
`.gitattributes`: without the pin a Windows checkout with `core.autocrlf=true`
converts it to CRLF and fails the equivalence check with no descriptor drift.
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

The nine formerly PENDING operations are now mounted by production
`NewServer` / `registerCombinedTools` and carry executable `tools/call`
evidence, so `PendingInventory` is empty and the STRICT publication gate is
green (201/201 SUPPORTED operations LISTED, REACHABLE and VERIFIED). The same
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

# Read next

- [Capability qualification](../decisions/mcp-capability-qualification.md)
- [Fail-closed authorization](../invariants/authz-fail-closed.md)
- [RPC contract](rpc-contract.md)
