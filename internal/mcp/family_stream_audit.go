// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// This file is the OSS-374 full-coverage lane handoff. It inventories every
// non-generated mounted surface on the pinned main tree and records an
// explicit decision: SUPPORTED WORKFLOW (an exposed tool or a composed path of
// typed Connect RPCs) or PRIVATE/TRANSPORT MECHANIC (raw bytes, opaque session
// ids, browser handshakes, discovery, public probes and static assets).
//
// It is deliberately additive. catalog.go, catalog_data.go, tools.go,
// server.go, client.go and errors.go belong to the integration owner. The
// exported accessor functions below are the machine-readable handoff used to
// generate the combined catalog and to assert that no raw HTTP surface is
// ever promoted to an agent tool.
package mcp

// Decision values. The two PRIVATE/TRANSPORT values together form the
// "private or transport mechanic" half of the required binary split.
const (
	FamilyStreamDecisionWorkflow  = "supported_workflow"
	FamilyStreamDecisionTransport = "transport_mechanic"
	FamilyStreamDecisionPrivate   = "private_mechanic"
)

// Stable reason codes for a decision.
const (
	StreamReasonByteTransport       = "byte_transport"
	StreamReasonSharedOperation     = "shared_operation_adapter"
	StreamReasonDiscovery           = "automatic_discovery"
	StreamReasonStudioAuthoring     = "studio_authoring"
	StreamReasonBootstrapMetadata   = "bootstrap_metadata"
	StreamReasonOperatorMaintenance = "operator_maintenance"
	StreamReasonTelemetry           = "rest_only_telemetry"
	StreamReasonDocs                = "public_docs"
	StreamReasonBrowserAsset        = "browser_asset_channel"
	StreamReasonBrowserHandshake    = "browser_handshake"
	StreamReasonPublicProbe         = "public_probe"
	StreamReasonReflection          = "stale_reflection"
	StreamReasonFrontend            = "static_frontend"
)

// FamilyStreamSurface is one audited non-generated mounted surface. Sources
// are exact "file:line:anchor" triples so a reviewer can re-verify the mount,
// the ACL and the classification decision against the pinned tree.
type FamilyStreamSurface struct {
	Route           string   `json:"route"`
	Mount           string   `json:"mount"`
	MountCondition  string   `json:"mount_condition,omitempty"`
	Decision        string   `json:"decision"`
	ReasonCode      string   `json:"reason_code"`
	Reason          string   `json:"reason"`
	ACL             string   `json:"acl"`
	SessionBounded  bool     `json:"session_bounded,omitempty"`
	TypedEquivalent []string `json:"typed_equivalent,omitempty"`
	Sources         []string `json:"sources"`
}

// IsSupported reports whether the surface has a tool or composed typed path.
func (s FamilyStreamSurface) IsSupported() bool { return s.Decision == FamilyStreamDecisionWorkflow }

// FamilyStreamCheck identifies the fixture mechanism used to verify a surface.
type FamilyStreamCheck string

const (
	StreamCheckTypedRPC FamilyStreamCheck = "typed_rpc"
	StreamCheckStream   FamilyStreamCheck = "stream_handler"
	StreamCheckProbe    FamilyStreamCheck = "public_probe"
	StreamCheckSource   FamilyStreamCheck = "source_and_catalog"
)

// FamilyStreamEvidence is one fixture-evidence row. Success and Denied are
// independent: private surfaces are verified by a structured denial plus the
// absence of any exposed tool, public probes by an unauthenticated success.
type FamilyStreamEvidence struct {
	Route          string            `json:"route"`
	Check          FamilyStreamCheck `json:"check"`
	TypedProcedure string            `json:"typed_procedure,omitempty"`
	Success        bool              `json:"success"`
	Denied         bool              `json:"denied"`
	Detail         string            `json:"detail"`
}

// Counts are pinned by TestFamilyStreamAuditIntegrity so the handoff cannot
// silently lose a surface. LISTED=22, workflow=3, transport=5, private=14.
const (
	FamilyStreamListedCount    = 22
	FamilyStreamWorkflowCount  = 3
	FamilyStreamTransportCount = 5
	FamilyStreamPrivateCount   = 14
	FamilyStreamEvidenceCount  = 22
)

// FamilyStreamAudit is the complete machine-readable inventory.
var FamilyStreamAudit = []FamilyStreamSurface{
	{
		Route: "PUT /api/v1/upload/{sessionId}", Mount: "internal/rpc/server.go:296",
		Decision: FamilyStreamDecisionTransport, ReasonCode: StreamReasonByteTransport,
		Reason: "Raw request-body byte stream into an opaque upload session. Session id and temp path are secrets, never tool arguments (upload.go:105-111); the typed UploadService/FileService workflow performs the same administration.",
		ACL:    "uploads/create on global object *", SessionBounded: true,
		TypedEquivalent: []string{
			"/carbonpanel.v1.UploadService/InitUpload", "/carbonpanel.v1.UploadService/UploadChunk",
			"/carbonpanel.v1.UploadService/GetUploadStatus", "/carbonpanel.v1.UploadService/CancelUpload",
			"/carbonpanel.v1.FileService/SaveUploadedFile",
		},
		Sources: []string{
			"internal/rpc/server.go:291:mux.Handle(\"/api/v1/upload/\"",
			"internal/rpc/handlers/upload.go:70:rbac.ResourceUploads, rbac.ActionCreate",
			"internal/rpc/handlers/upload.go:124:Deliberately NOT returning the server-local temp path",
		},
	},
	{
		Route: "GET /api/v1/download/{sessionId}", Mount: "internal/rpc/server.go:299",
		Decision: FamilyStreamDecisionTransport, ReasonCode: StreamReasonByteTransport,
		Reason: "Raw file bytes keyed by an opaque download session id. The handler looks the session up first and enforces files/read on the session's owning server (download.go:54-68,100-118); the typed FileService calls produce a bounded session for the agent.",
		ACL:    "files/read on session.ServerID (or global * for legacy sessions)", SessionBounded: true,
		TypedEquivalent: []string{
			"/carbonpanel.v1.FileService/ExportWorld", "/carbonpanel.v1.FileService/DownloadArchive",
			"/carbonpanel.v1.FileService/InitFileDownload", "/carbonpanel.v1.FileService/GetFile",
		},
		Sources: []string{
			"internal/rpc/server.go:294:mux.Handle(\"/api/v1/download/\"",
			"internal/rpc/handlers/download.go:65:authorizeDownload(enforcer, user, session)",
			"internal/rpc/handlers/download.go:117:rbac.ResourceFiles, rbac.ActionRead",
		},
	},
	{
		Route: "POST /api/v1/settings/validate-key", Mount: "internal/rpc/server.go:305",
		Decision: FamilyStreamDecisionPrivate, ReasonCode: StreamReasonSharedOperation,
		Reason:          "The REST and generated ConfigService operations share provider validation. The settings/read operation handles credentials and contacts the provider. Use the panel settings workflow.",
		ACL:             "settings/read on global object *",
		TypedEquivalent: []string{"/carbonpanel.v1.ConfigService/ValidateProviderKey"},
		Sources: []string{
			"internal/rpc/server.go:300:mux.Handle(\"/api/v1/settings/validate-key\"",
			"internal/rpc/handlers/key_validator.go:35:generated ConfigService.ValidateProviderKey RPC",
			"internal/rpc/handlers/key_validator.go:45:rbac.ResourceSettings, rbac.ActionRead",
		},
	},
	{
		Route: "GET /api/v1/nodes/scan", Mount: "internal/rpc/server.go:308",
		Decision: FamilyStreamDecisionPrivate, ReasonCode: StreamReasonDiscovery,
		Reason:          "Automatic Docker daemon discovery across local interfaces and TCP 2375/2376 (node_scan.go:30-37). Explicitly outside the catalog; agents use known configured node ids and PingNode only.",
		ACL:             "nodes/read on global object *",
		TypedEquivalent: []string{"/carbonpanel.v1.NodeService/PingNode"},
		Sources: []string{
			"internal/rpc/server.go:303:mux.Handle(\"/api/v1/nodes/scan\"",
			"internal/rpc/handlers/node_scan.go:50:rbac.ResourceNodes, rbac.ActionRead",
		},
	},
	{
		Route: "GET /api/v1/servers/{id}/mods/search", Mount: "internal/rpc/server.go:313",
		Decision: FamilyStreamDecisionWorkflow, ReasonCode: StreamReasonSharedOperation,
		Reason:          "Legacy JSON adapter and ModBrowseService share one bounded search operation (mod_search.go:118-125,229-231). The generated SearchMods tool is the supported path; the raw adapter is retained only for the ADR 0012 deprecation window.",
		ACL:             "mods/read on server_id (global * when id is 'none')",
		TypedEquivalent: []string{"/carbonpanel.v1.ModBrowseService/SearchMods"},
		Sources: []string{
			"internal/rpc/server.go:308:mux.Handle(\"/api/v1/servers/\"",
			"internal/rpc/handlers/mod_search.go:300:func (m *ModOnlineManager) Search(ctx context.Context",
			"internal/rpc/handlers/mod_search.go:210:rbac.ResourceMods, rbacAction, objectID",
		},
	},
	{
		Route: "GET /api/v1/servers/{id}/mods/{slug}/versions", Mount: "internal/rpc/server.go:313",
		Decision: FamilyStreamDecisionWorkflow, ReasonCode: StreamReasonSharedOperation,
		Reason:          "Same shared browse operation as search (mod_search.go:238-240); the generated GetModVersions tool is the supported bounded lookup.",
		ACL:             "mods/read on server_id (global * when id is 'none')",
		TypedEquivalent: []string{"/carbonpanel.v1.ModBrowseService/GetModVersions"},
		Sources: []string{
			"internal/rpc/server.go:308:mux.Handle(\"/api/v1/servers/\"",
			"internal/rpc/handlers/mod_search.go:210:rbac.ResourceMods, rbacAction, objectID",
		},
	},
	{
		Route: "POST /api/v1/servers/{id}/mods/install", Mount: "internal/rpc/server.go:313",
		Decision: FamilyStreamDecisionWorkflow, ReasonCode: StreamReasonSharedOperation,
		Reason:          "POST maps to mods/create (mod_search.go:205-212) and the same shared install operation; InstallMods is a confirm-gated typed tool. Agents never post raw download URLs to the legacy adapter.",
		ACL:             "mods/create on server_id (global * when id is 'none')",
		TypedEquivalent: []string{"/carbonpanel.v1.ModBrowseService/InstallMods"},
		Sources: []string{
			"internal/rpc/server.go:308:mux.Handle(\"/api/v1/servers/\"",
			"internal/rpc/handlers/mod_search.go:210:rbac.ResourceMods, rbacAction, objectID",
		},
	},
	{
		Route: "ANY /api/v1/packwiz/packs/*", Mount: "internal/rpc/server.go:320",
		Decision: FamilyStreamDecisionPrivate, ReasonCode: StreamReasonStudioAuthoring,
		Reason: "Packwiz inventory reads have typed packwiz_list_packs and packwiz_get_pack tools. This mixed Studio route also serves update checks, mutations, multipart imports, exports and raw assets outside that bounded read contract.",
		ACL:    "modpacks/read or modpacks/create on global object *",
		Sources: []string{
			"internal/rpc/server.go:314:mux.Handle(\"/api/v1/packwiz/\"",
			"internal/rpc/handlers/packwiz.go:133:rbac.ResourceModpacks, action",
		},
	},
	{
		Route: "GET /api/v1/packwiz/loaders/{loader}/versions", Mount: "internal/rpc/server.go:320",
		Decision: FamilyStreamDecisionTransport, ReasonCode: StreamReasonBootstrapMetadata,
		Reason:          "Unauthenticated loader/version metadata bootstrap for the Studio UI (packwiz.go:72-83). It is not a product-data workflow; the authenticated typed MinecraftService.GetLoaderVersions is the agent-facing equivalent.",
		ACL:             "none (public Studio metadata bootstrap)",
		TypedEquivalent: []string{"/carbonpanel.v1.MinecraftService/GetLoaderVersions"},
		Sources: []string{
			"internal/rpc/server.go:314:mux.Handle(\"/api/v1/packwiz/\"",
			"internal/rpc/handlers/packwiz.go:99:Route /api/v1/packwiz/loaders",
		},
	},
	{
		Route: "GET /api/v1/packwiz/{id}/pack.toml", Mount: "internal/rpc/server.go:320",
		Decision: FamilyStreamDecisionTransport, ReasonCode: StreamReasonBootstrapMetadata,
		Reason: "Raw pack file bytes for container bootstrapping, deliberately unauthenticated (packwiz.go:66-97). A raw file path is never a tool argument; ModpackService config/file metadata is the typed equivalent.",
		ACL:    "none (unauthenticated container bootstrap file)",
		TypedEquivalent: []string{
			"/carbonpanel.v1.ModpackService/GetModpackConfig", "/carbonpanel.v1.ModpackService/GetModpackFiles",
		},
		Sources: []string{
			"internal/rpc/server.go:314:mux.Handle(\"/api/v1/packwiz/\"",
			"internal/rpc/handlers/packwiz.go:94:Raw pack file serving for container bootstrapping",
			"internal/rpc/handlers/packwiz.go:115:ServePackFile(packID, relPath)",
		},
	},
	{
		Route: "GET /api/v1/staged-config/{serverID}/staged", Mount: "internal/rpc/server.go:323",
		Decision: FamilyStreamDecisionPrivate, ReasonCode: StreamReasonOperatorMaintenance,
		Reason: "Legacy staged-diff reader shares the operation with StagedConfigService.ListStagedChanges. MCP exposes bounded rollout metadata through list_staged_config_changes.",
		ACL:    "server_config/read on serverID",
		Sources: []string{
			"internal/rpc/server.go:317:mux.Handle(\"/api/v1/staged-config/\"",
			"internal/rpc/handlers/staged_config.go:85:rbac.ResourceServerConfig, action, serverID",
		},
	},
	{
		Route: "POST /api/v1/staged-config/{serverID}/staged", Mount: "internal/rpc/server.go:323",
		Decision: FamilyStreamDecisionPrivate, ReasonCode: StreamReasonOperatorMaintenance,
		Reason: "The adapter and StagedConfigService.StageConfigChange stage a diff under the same server_config/create grant. ConfigService.UpdateServerConfig uses a different operation and grant.",
		ACL:    "server_config/create on serverID",
		Sources: []string{
			"internal/rpc/server.go:317:mux.Handle(\"/api/v1/staged-config/\"",
			"internal/rpc/handlers/staged_config.go:85:rbac.ResourceServerConfig, action, serverID",
		},
	},
	{
		Route: "POST|DELETE /api/v1/staged-config/{serverID}/staged/{id}[/apply|/rollback]", Mount: "internal/rpc/server.go:323",
		Decision: FamilyStreamDecisionPrivate, ReasonCode: StreamReasonOperatorMaintenance,
		Reason: "Apply, rollback and discard are shared with StagedConfigService. Both adapters check the record's owner before attempting the change. Use the panel staged changes workflow.",
		ACL:    "server_config/create on serverID and on the staged record's owner",
		Sources: []string{
			"internal/rpc/server.go:317:mux.Handle(\"/api/v1/staged-config/\"",
			"internal/rpc/handlers/staged_config.go:99:rbac.ResourceServerConfig, action, ownerID",
			"internal/rpc/handlers/staged_config.go:112:parts[3] == \"apply\"",
		},
	},
	{
		Route: "GET /api/v2/metrics/host", Mount: "internal/rpc/server.go:331",
		Decision: FamilyStreamDecisionPrivate, ReasonCode: StreamReasonTelemetry,
		Reason: "Prometheus exposition for monitoring. Use an authenticated host metrics scrape for telemetry.",
		ACL:    "servers/read on global object * (fail-closed via requireRequestObject)",
		Sources: []string{
			"internal/rpc/server.go:325:mux.Handle(\"/api/v2/metrics/\"",
			"internal/rpc/handlers/metrics.go:65:\"/api/v2/metrics/host\"",
			"internal/rpc/handlers/metrics.go:75:rbac.ResourceServers, rbac.ActionRead, objectID",
		},
	},
	{
		Route: "GET /api/v2/metrics/servers/{id}", Mount: "internal/rpc/server.go:331",
		Decision: FamilyStreamDecisionPrivate, ReasonCode: StreamReasonTelemetry,
		Reason: "Per-server Prometheus exposition for monitoring. GetServer includes the latest cached operational metrics for a scoped server.",
		ACL:    "servers/read on id",
		Sources: []string{
			"internal/rpc/server.go:325:mux.Handle(\"/api/v2/metrics/\"",
			"internal/rpc/handlers/metrics.go:61:/api/v2/metrics/servers/",
			"internal/rpc/handlers/metrics.go:75:rbac.ResourceServers, rbac.ActionRead, objectID",
		},
	},
	{
		Route: "GET /api/v1/openapi.yaml", Mount: "internal/rpc/server.go:302",
		Decision: FamilyStreamDecisionPrivate, ReasonCode: StreamReasonDocs,
		Reason: "Public generated documentation and discovery spec (openapi.go:25-28,152-170). The catalog source is the proto descriptor table, never this spec; there is no product data and no tool to expose.",
		ACL:    "none (public documentation)",
		Sources: []string{
			"internal/rpc/server.go:297:mux.HandleFunc(\"/api/v1/openapi.yaml\"",
			"internal/rpc/handlers/openapi.go:28:func NewOpenAPIHandler(",
		},
	},
	{
		Route: "GET /api/v1/worldview/map/{key}/{asset}", Mount: "internal/rpc/server.go:337",
		MountCondition: "mounted only when a world-view proxy is configured",
		Decision:       FamilyStreamDecisionTransport, ReasonCode: StreamReasonBrowserAsset,
		Reason: "Same-origin reverse proxy for renderer tiles (config.go:24-35, proxy.go:98-158). Cookie auth, per-user opaque session key, world_view/read on the session's server and subuser fallback are all enforced before upstream (proxy.go:123-181). Browser asset bytes and session keys are never tool arguments.",
		ACL:    "world_view/read on session.ServerID (session key bound to its creator)", SessionBounded: true,
		TypedEquivalent: []string{
			"/carbonpanel.v1.WorldViewService/GetWorldViewSession",
			"/carbonpanel.v1.WorldViewService/GetWorldViewStatus",
			"/carbonpanel.v1.WorldViewService/ListCapabilities",
			"/carbonpanel.v1.WorldViewService/ListMapOverlays",
		},
		Sources: []string{
			"internal/rpc/server.go:331:mux.Handle(worldview.ProxyPrefix",
			"internal/worldview/config.go:28:ProxyPrefix = \"/api/v1/worldview/map/\"",
			"internal/worldview/proxy.go:152:h.deps.Sessions.Resolve(key)",
			"internal/worldview/proxy.go:184:rbac.ResourceWorldView, rbac.ActionRead, serverID",
		},
	},
	{
		Route: "GET|POST /api/v1/auth/oidc/{login,callback,exchange}", Mount: "internal/rpc/server.go:290-292",
		MountCondition: "mounted only when OIDC is enabled",
		Decision:       FamilyStreamDecisionPrivate, ReasonCode: StreamReasonBrowserHandshake,
		Reason: "Browser IdP handshake; the exchange redeems a one-time code for a SPA session token with Cache-Control: no-store (oidc.go HandleExchange). A session token is not an MCP credential and must never be a tool argument or result.",
		ACL:    "none (browser enrollment / session bootstrap)",
		Sources: []string{
			"internal/rpc/server.go:287:mux.HandleFunc(\"/api/v1/auth/oidc/exchange\"",
			"internal/auth/oidc.go:693:func (h *OIDCHandler) HandleExchange",
			"internal/auth/oidc.go:713:h.exchangeStore.consume(req.Code, clientIP(r))",
		},
	},
	{
		Route: "/ws", Mount: "internal/rpc/server.go:278",
		Decision: FamilyStreamDecisionPrivate, ReasonCode: StreamReasonBrowserHandshake,
		Reason: "Browser console/log/event hub WebSocket upgrade. Private handshake; agents use typed RPC reads and events instead of a raw upgrade socket.",
		ACL:    "authenticated WebSocket upgrade and refresh; servers/read and servers/command on the subscribed or commanded server",
		Sources: []string{
			"internal/rpc/server.go:273:mux.Handle(\"/ws\"",
		},
	},
	{
		Route: "/healthz, /readyz, /health/detail", Mount: "internal/rpc/server.go:282-284",
		Decision: FamilyStreamDecisionPrivate, ReasonCode: StreamReasonPublicProbe,
		Reason: "Unauthenticated liveness/readiness and layered health probes (health.go:17-69). Read-only deployment signals with no product data and no typed client; no tool is needed.",
		ACL:    "none (public probes)",
		Sources: []string{
			"internal/rpc/server.go:277:mux.HandleFunc(\"/healthz\"",
			"internal/rpc/server.go:279:mux.HandleFunc(\"/health/detail\"",
			"internal/rpc/handlers/health.go:23:func NewHealthHandler(",
		},
	},
	{
		Route: "gRPC reflection v1 / v1alpha", Mount: "internal/rpc/server.go:274-275",
		Decision: FamilyStreamDecisionPrivate, ReasonCode: StreamReasonReflection,
		Reason: "Reflection describes the same registered Connect service list the server checks against its mounted handlers.",
		ACL:    "none (discovery endpoint)",
		Sources: []string{
			"internal/rpc/server.go:269:grpcreflect.NewHandlerV1",
			"internal/rpc/server.go:270:grpcreflect.NewHandlerV1Alpha",
		},
	},
	{
		Route: "/ (frontend catch-all)", Mount: "internal/rpc/server.go:341,616",
		Decision: FamilyStreamDecisionPrivate, ReasonCode: StreamReasonFrontend,
		Reason: "Static SvelteKit assets served for non-RPC routes (server.go:340-341,551-615). It is a transport fallback, not product data; raw arbitrary URLs are never exposed to the model.",
		ACL:    "none (static assets)",
		Sources: []string{
			"internal/rpc/server.go:335:s.setupFrontend(mux)",
			"internal/rpc/server.go:639:mux.Handle(\"/\", s.createFrontendHandler(fs))",
		},
	},
}

// FamilyStreamEvidenceMatrix is the machine-readable fixture evidence for the
// handoff. Every listed surface has exactly one row.
var FamilyStreamEvidenceMatrix = []FamilyStreamEvidence{
	{"PUT /api/v1/upload/{sessionId}", StreamCheckStream, "", true, true, "bounded upload session: PUT succeeds for uploads/create, 403 for files/read-only token, 401 without credentials"},
	{"GET /api/v1/download/{sessionId}", StreamCheckStream, "", true, true, "bounded session bound to srv-1: 200 for files/read[srv-1], 403 cross-server, 401 without credentials"},
	{"POST /api/v1/settings/validate-key", StreamCheckTypedRPC, "/carbonpanel.v1.ConfigService/ValidateProviderKey", true, true, "typed soft result succeeds for settings/read; structured permission_denied for servers/read-only; legacy adapter 200/403/401"},
	{"GET /api/v1/nodes/scan", StreamCheckTypedRPC, "/carbonpanel.v1.NodeService/PingNode", true, true, "typed PingNode succeeds for nodes/read[id]; scan handler denies 403/401 before any probe (no live Docker)"},
	{"GET /api/v1/servers/{id}/mods/search", StreamCheckTypedRPC, "/carbonpanel.v1.ModBrowseService/SearchMods", true, true, "typed search succeeds for mods/read; structured denial for servers/read-only"},
	{"GET /api/v1/servers/{id}/mods/{slug}/versions", StreamCheckTypedRPC, "/carbonpanel.v1.ModBrowseService/GetModVersions", true, true, "typed versions succeeds for mods/read; structured denial for servers/read-only"},
	{"POST /api/v1/servers/{id}/mods/install", StreamCheckTypedRPC, "/carbonpanel.v1.ModBrowseService/InstallMods", true, true, "typed install succeeds for mods/create; structured denial for mods/read-only"},
	{"ANY /api/v1/packwiz/packs/*", StreamCheckStream, "", false, true, "Studio authoring adapter denies 403 for servers/read-only and 401 without credentials; no tool exposes raw pack routes"},
	{"GET /api/v1/packwiz/loaders/{loader}/versions", StreamCheckTypedRPC, "/carbonpanel.v1.MinecraftService/GetLoaderVersions", true, true, "typed loader versions succeeds for authenticated caller; structured denial for invalid token; raw bootstrap route stays out of catalog"},
	{"GET /api/v1/packwiz/{id}/pack.toml", StreamCheckTypedRPC, "/carbonpanel.v1.ModpackService/GetModpackConfig", true, true, "typed modpack config succeeds for modpacks/read; structured denial for invalid token; raw pack bytes stay out of catalog"},
	{"GET /api/v1/staged-config/{serverID}/staged", StreamCheckStream, "", true, true, "legacy list returns 200 with server_config/read, 403 without that grant and 401 without credentials"},
	{"POST /api/v1/staged-config/{serverID}/staged", StreamCheckSource, "", false, false, "adapter requires server_config/create before staging; generated StagedConfigService shares the operation"},
	{"POST|DELETE /api/v1/staged-config/{serverID}/staged/{id}[/apply|/rollback]", StreamCheckStream, "", false, true, "apply/rollback/discard deny 403 for a token without server_config/create; no exposed tool"},
	{"GET /api/v2/metrics/host", StreamCheckStream, "", true, true, "Prometheus exposition 200 for servers/read on *; 403 for scoped token and for missing credentials (adapter collapses auth failure into 403)"},
	{"GET /api/v2/metrics/servers/{id}", StreamCheckStream, "", true, true, "per-server exposition 200 for servers/read[id]; 403 for another server id and for missing credentials (fail-closed 403)"},
	{"GET /api/v1/openapi.yaml", StreamCheckProbe, "", true, false, "public spec handler responds without credentials; docs/discovery stays out of catalog"},
	{"GET /api/v1/worldview/map/{key}/{asset}", StreamCheckTypedRPC, "/carbonpanel.v1.WorldViewService/GetWorldViewStatus", true, true, "typed world-view status succeeds for world_view/read[id]; structured denial cross-server and unauthenticated; raw proxy denial is covered by internal/rpc/worldview_proxy_test.go"},
	{"GET|POST /api/v1/auth/oidc/{login,callback,exchange}", StreamCheckSource, "", false, false, "verified from exact mount and one-time exchange source; no tool exposes codes, tokens or the handshake"},
	{"/ws", StreamCheckSource, "", false, false, "verified from exact mount; no exposed tool performs a WebSocket upgrade"},
	{"/healthz, /readyz, /health/detail", StreamCheckProbe, "", true, false, "public health handler returns 200 without credentials; no tool required"},
	{"gRPC reflection v1 / v1alpha", StreamCheckSource, "", false, false, "verified from exact mounts; reflection is never used as a catalog source"},
	{"/ (frontend catch-all)", StreamCheckSource, "", false, false, "verified from exact mounts; no arbitrary-URL or static-asset tool is exposed"},
}

// FamilyStreamIncludedSurfaces returns the surfaces with a supported tool or
// composed typed path.
func FamilyStreamIncludedSurfaces() []FamilyStreamSurface {
	out := make([]FamilyStreamSurface, 0, FamilyStreamWorkflowCount)
	for _, s := range FamilyStreamAudit {
		if s.IsSupported() {
			out = append(out, s)
		}
	}
	return out
}

// FamilyStreamMechanicalSurfaces returns the PRIVATE/TRANSPORT half of the
// inventory (transport mechanics and private mechanics together).
func FamilyStreamMechanicalSurfaces() []FamilyStreamSurface {
	out := make([]FamilyStreamSurface, 0, FamilyStreamTransportCount+FamilyStreamPrivateCount)
	for _, s := range FamilyStreamAudit {
		if !s.IsSupported() {
			out = append(out, s)
		}
	}
	return out
}

// FamilyStreamSurfaceFor looks up one audited surface by exact route.
func FamilyStreamSurfaceFor(route string) (FamilyStreamSurface, bool) {
	for _, s := range FamilyStreamAudit {
		if s.Route == route {
			return s, true
		}
	}
	return FamilyStreamSurface{}, false
}

// FamilyStreamEvidenceFor looks up the fixture evidence row for a route.
func FamilyStreamEvidenceFor(route string) (FamilyStreamEvidence, bool) {
	for _, e := range FamilyStreamEvidenceMatrix {
		if e.Route == route {
			return e, true
		}
	}
	return FamilyStreamEvidence{}, false
}

// FamilyStreamTypedProcedures returns every distinct typed equivalent named
// by the audit, in declaration order, for catalog drift checks.
func FamilyStreamTypedProcedures() []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range FamilyStreamAudit {
		for _, p := range s.TypedEquivalent {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
