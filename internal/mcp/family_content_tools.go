// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Content-family MCP tools (OSS-372).
//
// This file is deliberately self-contained: it adds no entry to the shared
// catalog and does not change the shared Clients struct. ContentToolDescriptors
// is the machine-readable hand-off artifact from which the integration owner
// generates catalog entries and exposed tool specs. Scopes, service names,
// client constructors, methods, procedures and build qualifications are
// derived from the generated rawCatalogEntries table so they cannot drift from
// the live RBAC map; only the local exposure decision is declared here.
//
// Exclusions are intentional and documented on each descriptor: raw chunked
// byte transport (UploadService, SaveUploadedFile, ImportUploadedModpack is a
// session import and is kept, but no RPC that accepts raw bytes is exposed),
// arbitrary URL/credential egress (ImportRemoteModpack, DownloadRemoteArchive,
// GetModpackByURL), and file mutations (the first slice is read-first and this
// family adds only bounded READ/EXPORT operations).
package mcp

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	cpconnect "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ContentToolDescriptor is the machine-readable exposure decision for one
// mounted RPC in a content family. Tools with ClassExclude have an empty Tool
// name and are descriptor-only: the integration owner still emits a catalog
// tuple for them, but no MCP tool is registered.
type ContentToolDescriptor struct {
	Tool           string     `json:"tool"`
	Procedure      string     `json:"procedure"`
	Service        string     `json:"service"`
	Method         string     `json:"method"`
	Client         string     `json:"client"`
	Scope          string     `json:"scope"`
	Class          Class      `json:"class"`
	Access         string     `json:"access"` // "read" or "mutation"
	Confirm        bool       `json:"confirm"`
	Build          PanelBuild `json:"build"`
	Transport      string     `json:"transport"`     // "rpc" or "rest"
	RegisteredBy   string     `json:"registered_by"` // "content", "first_slice" or "integration"
	BoundedArgs    string     `json:"bounded_args"`
	SourceRef      string     `json:"source_ref,omitempty"`      // exact file:line for REST surfaces
	EquivalentFlow string     `json:"equivalent_flow,omitempty"` // supported typed-RPC equivalent
	Description    string     `json:"description,omitempty"`
	ExcludeReason  string     `json:"exclude_reason,omitempty"`
}

type contentDecision struct {
	procedure      string
	tool           string
	class          Class
	access         string
	confirm        bool
	boundedArgs    string
	description    string
	excludeReason  string
	transport      string
	registeredBy   string
	sourceRef      string
	equivalentFlow string
	scopeOverride  string
}

// contentDecisions is the local exposure decision table. It is intentionally
// small: every factual field (scope, client, method, build) is joined from the
// generated catalog below.
var contentDecisions = []contentDecision{
	// --- ModService (mods/*[server_id]) -----------------------------------
	{procedure: "/carbonpanel.v1.ModService/ListMods", tool: "list_mods", class: ClassSafe, access: "read", boundedArgs: "server_id (exact id) only", description: "List installed mods for one server. Read-only."},
	{procedure: "/carbonpanel.v1.ModService/GetMod", tool: "get_mod", class: ClassSafe, access: "read", boundedArgs: "server_id + single mod_id", description: "Get one installed mod by exact id. Read-only."},
	{procedure: "/carbonpanel.v1.ModService/GetFabricOptimizationStack", tool: "get_fabric_optimization_stack", class: ClassSafe, access: "read", boundedArgs: "server_id only", description: "Report Fabric optimization stack status and compatibility. Read-only."},
	{procedure: "/carbonpanel.v1.ModService/ImportUploadedMod", tool: "import_uploaded_mod", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + single upload_session_id; display_name/description <= 256 chars; stage", description: "Import a previously uploaded jar session into one server's mods via the staged safe path (compatibility-sensitive uploads are never live-installed). Requires confirm=true. No raw bytes are carried by the tool."},
	{procedure: "/carbonpanel.v1.ModService/UpdateMod", tool: "update_mod", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + single mod_id; optional metadata <= 256 chars", description: "Update mod metadata on one server. Requires confirm=true."},
	{procedure: "/carbonpanel.v1.ModService/DeleteMod", tool: "delete_mod", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + single mod_id", description: "Delete one installed mod. Destructive; requires confirm=true."},
	{procedure: "/carbonpanel.v1.ModService/InstallFabricOptimizationStack", tool: "install_fabric_optimization_stack", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + at most 4 mod_ids from the known optimization allowlist", description: "Install or update the known Fabric optimization mods. Requires confirm=true; mod ids are allowlisted."},

	// --- ModBrowseService (global mods/read|create; post-v1.0.40) ----------
	{procedure: "/carbonpanel.v1.ModBrowseService/SearchMods", tool: "search_mods", class: ClassSafe, access: "read", boundedArgs: "query <= 256 chars; platform/loader/mc_version/side <= 32 chars; optional server_id", description: "Search the configured mod index using the existing mods/read grant. Read-only; no credentials in the request or response."},
	{procedure: "/carbonpanel.v1.ModBrowseService/GetModVersions", tool: "get_mod_versions", class: ClassSafe, access: "read", boundedArgs: "single project id; platform/loader/mc_version <= 32 chars", description: "List installable versions for one mod project. Read-only."},
	{procedure: "/carbonpanel.v1.ModBrowseService/InstallMods", tool: "install_mods", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + at most 10 entries; each name/file_name <= 128 chars; https download_url without userinfo", description: "Install previously browsed mod jars on one server. Requires confirm=true; download URLs are https-only and carry no credentials."},

	// --- ModpackService (modpacks/*) --------------------------------------
	{procedure: "/carbonpanel.v1.ModpackService/SearchModpacks", tool: "search_modpacks", class: ClassSafe, access: "read", boundedArgs: "query/game_version/mod_loader/indexer <= 64 chars; page <= 1000; page_size 1..100", description: "Search the configured modpack index. Read-only."},
	{procedure: "/carbonpanel.v1.ModpackService/GetModpack", tool: "get_modpack", class: ClassSafe, access: "read", boundedArgs: "single id", description: "Get one indexed modpack by exact id. Read-only."},
	{procedure: "/carbonpanel.v1.ModpackService/GetModpackBySlug", tool: "get_modpack_by_slug", class: ClassSafe, access: "read", boundedArgs: "single slug <= 128 chars", description: "Get one indexed modpack by slug. Read-only."},
	{procedure: "/carbonpanel.v1.ModpackService/ListFavorites", tool: "list_favorites", class: ClassSafe, access: "read", boundedArgs: "no arguments", description: "List favorite modpacks. Read-only."},
	{procedure: "/carbonpanel.v1.ModpackService/GetIndexerStatus", tool: "get_indexer_status", class: ClassSafe, access: "read", boundedArgs: "no arguments", description: "Report modpack indexer availability. Read-only."},
	{procedure: "/carbonpanel.v1.ModpackService/GetModpackConfig", tool: "get_modpack_config", class: ClassSafe, access: "read", boundedArgs: "single id", description: "Get a modpack's configuration map. Read-only."},
	{procedure: "/carbonpanel.v1.ModpackService/GetModpackFiles", tool: "get_modpack_files", class: ClassSafe, access: "read", boundedArgs: "single id", description: "List a modpack's version files. Read-only."},
	{procedure: "/carbonpanel.v1.ModpackService/GetModpackVersions", tool: "get_modpack_versions", class: ClassSafe, access: "read", boundedArgs: "single id; game_version/mod_loader <= 32 chars", description: "List available releases for one modpack, including Minecraft versions reported for each release. Read-only."},
	{procedure: "/carbonpanel.v1.ModpackService/ToggleFavorite", tool: "toggle_favorite", class: ClassSafe, access: "mutation", boundedArgs: "single id", description: "Toggle the local favorite flag on one modpack. Idempotent local preference; no confirm gate."},
	{procedure: "/carbonpanel.v1.ModpackService/SyncModpacks", tool: "sync_modpacks", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "query/game_version/mod_loader/indexer <= 64 chars", description: "Refresh the modpack index from configured sources. Requires confirm=true."},
	{procedure: "/carbonpanel.v1.ModpackService/ImportUploadedModpack", tool: "import_uploaded_modpack", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "single upload_session_id; name/description <= 256 chars", description: "Import a previously uploaded modpack session. Requires confirm=true; no raw bytes in the tool."},
	{procedure: "/carbonpanel.v1.ModpackService/DeleteModpack", tool: "delete_modpack", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "single id", description: "Remove one indexed modpack. Destructive; requires confirm=true."},
	{procedure: "/carbonpanel.v1.ModpackService/SyncModpackFiles", tool: "sync_modpack_files", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "single id", description: "Refresh version files for one modpack. Requires confirm=true."},
	{procedure: "/carbonpanel.v1.ModpackService/GetModpackByURL", tool: "get_modpack_by_url", class: ClassSafe, access: "read", boundedArgs: "single absolute https URL without userinfo; <= 512 chars", description: "Look up one modpack by website URL. The URL is validated as https without embedded credentials; the controller applies its own redirect and host policy."},
	{procedure: "/carbonpanel.v1.ModpackService/ImportRemoteModpack", tool: "import_remote_modpack", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "url (https, no userinfo) + name/description/mc_version/mod_loader <= 128 chars; auth_token and allow_private_network are intentionally not accepted", description: "Import a modpack from an operator-supplied https URL. Requires confirm=true. The proto credential fields auth_token (modpack.proto:176) and allow_private_network (modpack.proto:177) are deliberately not exposed to MCP arguments."},

	// PackwizService reads the Studio authoring library, separate from the provider index.
	{procedure: cpconnect.PackwizServiceListPacksProcedure, tool: packwizListTool, class: ClassSafe, access: "read", boundedArgs: "non-negative offset; 1..50 entries; 4 MiB controller response limit", description: "List Packwiz Studio packs. Requires global modpacks/read. Use next_offset to page through pack summaries."},
	{procedure: cpconnect.PackwizServiceGetPackProcedure, tool: packwizGetTool, class: ClassSafe, access: "read", boundedArgs: "one exact pack_id; non-negative offset; 1..50 entries; 4 MiB controller response limit", description: "Read Packwiz pack metadata and a page of content across mods, datapacks, resource packs and shaders. Requires global modpacks/read. Omits download URLs, paths and raw options."},

	// --- DatapackService (mods/*[server_id]) ------------------------------
	{procedure: "/carbonpanel.v1.DatapackService/SearchDatapacks", tool: "search_datapacks", class: ClassSafe, access: "read", boundedArgs: "query/game_version/indexer <= 64 chars; offset <= 10000; limit 1..100", description: "Search a datapack index using the existing mods/read grant. Read-only."},
	{procedure: "/carbonpanel.v1.DatapackService/ListDatapacks", tool: "list_datapacks", class: ClassSafe, access: "read", boundedArgs: "server_id only", description: "List datapacks installed in one server's world. Read-only."},
	{procedure: "/carbonpanel.v1.DatapackService/InstallDatapack", tool: "install_datapack", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + single datapack_id; game_version/file_name/indexer <= 128 chars; stage", description: "Install one indexed datapack into a world. Requires confirm=true."},
	{procedure: "/carbonpanel.v1.DatapackService/UploadDatapack", tool: "upload_datapack", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + single upload_session_id; file_name <= 128 chars; stage", description: "Import a previously uploaded datapack session. Requires confirm=true; no raw bytes in the tool."},
	{procedure: "/carbonpanel.v1.DatapackService/DeleteDatapack", tool: "delete_datapack", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + single file_name (no path separators)", description: "Delete one installed datapack. Destructive; requires confirm=true."},

	// --- ModuleService (module_templates/* + modules/*) -------------------
	{procedure: "/carbonpanel.v1.ModuleService/ListModuleTemplates", tool: "list_module_templates", class: ClassSafe, access: "read", boundedArgs: "optional type enum name + category <= 64 chars", description: "List module templates, optionally filtered. Read-only."},
	{procedure: "/carbonpanel.v1.ModuleService/GetModuleTemplate", tool: "get_module_template", class: ClassSafe, access: "read", boundedArgs: "single id", description: "Get one module template by exact id. Read-only."},
	{procedure: "/carbonpanel.v1.ModuleService/ListModules", tool: "list_modules", class: ClassSafe, access: "read", boundedArgs: "optional server_id and template_id", description: "List module instances, optionally filtered. Read-only."},
	{procedure: "/carbonpanel.v1.ModuleService/GetModule", tool: "get_module", class: ClassSafe, access: "read", boundedArgs: "single id", description: "Get one module instance by exact id. Read-only."},
	{procedure: "/carbonpanel.v1.ModuleService/GetModuleLogs", tool: "get_module_logs", class: ClassSafe, access: "read", boundedArgs: "single id + tail 1..100", description: "Get bounded recent log lines for one module. Read-only."},
	{procedure: "/carbonpanel.v1.ModuleService/GetNextAvailableModulePort", tool: "get_next_available_module_port", class: ClassSafe, access: "read", boundedArgs: "optional server_id", description: "Report the next allocatable module port. Read-only."},
	{procedure: "/carbonpanel.v1.ModuleService/GetAvailableAliases", tool: "get_available_aliases", class: ClassSafe, access: "read", boundedArgs: "optional server_id and module_id", description: "List available template aliases. Read-only."},
	{procedure: "/carbonpanel.v1.ModuleService/GetResolvedAliases", tool: "get_resolved_aliases", class: ClassSafe, access: "read", boundedArgs: "optional server_id and module_id", description: "List aliases resolved for a context. Read-only."},
	{procedure: "/carbonpanel.v1.ModuleService/CreateModuleTemplate", tool: "create_module_template", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "name/docker_image/category/icon/health_check_path <= 128 chars; port 0..65535; no free-form schema/env/volumes/hooks", description: "Create a custom module template from bounded display fields. Requires confirm=true; free-form config, env, volumes and hooks are intentionally not accepted."},
	{procedure: "/carbonpanel.v1.ModuleService/UpdateModuleTemplate", tool: "update_module_template", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "single id + bounded optional display fields", description: "Update a custom module template's bounded display fields. Requires confirm=true."},
	{procedure: "/carbonpanel.v1.ModuleService/DeleteModuleTemplate", tool: "delete_module_template", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "single id", description: "Delete one custom module template. Destructive; requires confirm=true."},
	{procedure: "/carbonpanel.v1.ModuleService/CreateModule", tool: "create_module", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + template_id + name; memory 0..65536 MB; no free-form config/env/volumes/hooks", description: "Create one module instance from a template. Requires confirm=true; free-form config, env, volumes and hooks are intentionally not accepted."},
	{procedure: "/carbonpanel.v1.ModuleService/UpdateModule", tool: "update_module", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "single id + bounded name/memory/flags; stage", description: "Update bounded fields on one module. Requires confirm=true; free-form config/env/volumes/hooks are not accepted."},
	{procedure: "/carbonpanel.v1.ModuleService/DeleteModule", tool: "delete_module", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "single id", description: "Delete one module instance and its container. Destructive; requires confirm=true."},
	{procedure: "/carbonpanel.v1.ModuleService/StartModule", tool: "start_module", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "single id", description: "Start one stopped module. Requires confirm=true."},
	{procedure: "/carbonpanel.v1.ModuleService/StopModule", tool: "stop_module", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "single id", description: "Stop one running module. Requires confirm=true."},
	{procedure: "/carbonpanel.v1.ModuleService/RestartModule", tool: "restart_module", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "single id", description: "Restart one module. Requires confirm=true."},
	{procedure: "/carbonpanel.v1.ModuleService/RecreateModule", tool: "recreate_module", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "single id", description: "Destroy and recreate one module's container. Destructive; requires confirm=true."},

	// --- FileService: first-slice reads stay owned by PR522 ----------------
	{procedure: "/carbonpanel.v1.FileService/ListFiles", tool: "list_files", class: ClassSafe, access: "read", boundedArgs: "server_id + optional bounded path", registeredBy: "first_slice", description: "Already registered by PR522; listed here only for full-family coverage accounting."},
	{procedure: "/carbonpanel.v1.FileService/GetFile", tool: "get_file", class: ClassSafe, access: "read", boundedArgs: "server_id + bounded path", registeredBy: "first_slice", description: "Already registered by PR522; listed here only for full-family coverage accounting."},

	// --- FileService: this family adds all remaining RPCs -----------------
	{procedure: "/carbonpanel.v1.FileService/GetExtractionStatus", tool: "get_extraction_status", class: ClassSafe, access: "read", boundedArgs: "single operation_id", description: "Poll the status of one bounded extraction operation. Read-only."},
	{procedure: "/carbonpanel.v1.FileService/GetRemoteArchiveProgress", tool: "get_remote_archive_progress", class: ClassSafe, access: "read", boundedArgs: "server_id + single task_id", description: "Poll one remote archive task. Read-only."},
	{procedure: "/carbonpanel.v1.FileService/InitFileDownload", tool: "init_file_download", class: ClassReview, access: "read", boundedArgs: "server_id + single server-relative path <= 512 chars", description: "Open a bounded single-file download session. The tool returns session metadata; the byte stream is the scoped GET /api/v1/download/{sessionId} flow."},
	{procedure: "/carbonpanel.v1.FileService/DownloadArchive", tool: "download_archive", class: ClassReview, access: "read", boundedArgs: "server_id + at most 20 bounded server-relative paths", description: "Open a bounded archive download session for the scoped download byte flow."},
	{procedure: "/carbonpanel.v1.FileService/ExportWorld", tool: "export_world", class: ClassReview, access: "read", boundedArgs: "server_id only", description: "Prepare a world export session; the world directory stays in place."},
	{procedure: "/carbonpanel.v1.FileService/UpdateFile", tool: "update_file", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + bounded path + base64 content <= 256 KiB + stage", description: "Write bounded content to one server-relative file. Requires confirm=true; stage=true keeps bytes out of the live path."},
	{procedure: "/carbonpanel.v1.FileService/DeleteFile", tool: "delete_file", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + single path (no bulk paths in this tool) + stage", description: "Delete one server-relative file or directory. Requires confirm=true; stage records the deletion without touching live bytes."},
	{procedure: "/carbonpanel.v1.FileService/RenameFile", tool: "rename_file", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + bounded path + new_name (no separators)", description: "Rename one server-relative file. Requires confirm=true."},
	{procedure: "/carbonpanel.v1.FileService/ExtractArchive", tool: "extract_archive", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + single bounded archive path", description: "Start a bounded archive extraction. Requires confirm=true; poll with get_extraction_status."},
	{procedure: "/carbonpanel.v1.FileService/CreateFolder", tool: "create_folder", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + single bounded path", description: "Create one server-relative folder. Requires confirm=true."},
	{procedure: "/carbonpanel.v1.FileService/MoveFile", tool: "move_file", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + two bounded paths", description: "Move one server-relative file or directory. Requires confirm=true."},
	{procedure: "/carbonpanel.v1.FileService/CopyFile", tool: "copy_file", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + two bounded paths", description: "Copy one server-relative file or directory. Requires confirm=true."},
	{procedure: "/carbonpanel.v1.FileService/CreateArchive", tool: "create_archive", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + at most 20 bounded paths", description: "Create one archive from bounded paths. Requires confirm=true."},
	{procedure: "/carbonpanel.v1.FileService/DownloadRemoteArchive", tool: "download_remote_archive", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + https url (no userinfo) + bounded destination_path + sha256_checksum; auto_extract", description: "Start a checksum-verified remote archive download. Requires confirm=true; the controller applies its own redirect/host policy."},
	{procedure: "/carbonpanel.v1.FileService/SaveUploadedFile", tool: "save_uploaded_file", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "server_id + single upload_session_id + bounded destination_path/filename", description: "Complete a scoped upload session into one server path. Requires confirm=true; consumes an upload session created by init_upload."},

	// OSS-387 controller-owned bounded-frame transport primitives. These are
	// descriptor-only: they are never standalone MCP tools. The gateway holds
	// no panel/node storage; each frame is an authenticated typed RPC and the
	// controller owns the read handle behind rpc.NewAuthInterceptor.
	{procedure: "/carbonpanel.v1.FileService/OpenStream", class: ClassSafe, access: "read", transport: "controller_stream", registeredBy: "integration", sourceRef: "internal/rpc/services/file_stream.go:OpenStream", equivalentFlow: "scoped_stream_open (tools/call): opens an owned controller session; auth via files/read[server_id]", description: "Descriptor-only: controller-owned bounded read session primitive."},
	{procedure: "/carbonpanel.v1.FileService/ReadStreamFrame", class: ClassSafe, access: "read", transport: "controller_stream", registeredBy: "integration", sourceRef: "internal/rpc/services/file_stream.go:ReadStreamFrame", equivalentFlow: "scoped_stream_chunk (tools/call): one bounded frame with cumulative SHA-256", description: "Descriptor-only: one bounded authenticated frame."},
	{procedure: "/carbonpanel.v1.FileService/CloseStream", class: ClassSafe, access: "read", transport: "controller_stream", registeredBy: "integration", sourceRef: "internal/rpc/services/file_stream.go:CloseStream", equivalentFlow: "scoped_stream_cancel (tools/call): releases only the owned session", description: "Descriptor-only: release of an owned controller session."},

	// --- UploadService: scoped chunked upload workflow --------------------
	{procedure: "/carbonpanel.v1.UploadService/InitUpload", tool: "init_upload", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "filename <= 128 chars; total_size <= 4 GiB; chunk_size <= 256 KiB", description: "Create a bounded upload session. Requires confirm=true; follow with upload_chunk and get_upload_status."},
	{procedure: "/carbonpanel.v1.UploadService/GetUploadStatus", tool: "get_upload_status", class: ClassSafe, access: "read", boundedArgs: "single session_id", description: "Read bounded progress for one upload session. Read-only."},
	{procedure: "/carbonpanel.v1.UploadService/UploadChunk", tool: "upload_chunk", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "single session_id + chunk_index <= 100000 + base64 data <= 256 KiB", description: "Write one bounded chunk into an upload session. Requires confirm=true; 256 KiB matches the scoped streaming frame cap."},
	{procedure: "/carbonpanel.v1.UploadService/CancelUpload", tool: "cancel_upload", class: ClassReview, access: "mutation", confirm: true, boundedArgs: "single session_id", description: "Cancel one upload session and remove its temporary bytes. Requires confirm=true."},

	// --- REST / scoped streaming surfaces (descriptor-only) ---------------
	// These are real fail-closed HTTP surfaces mounted alongside the RPCs.
	// They are listed for full coverage; their typed-RPC equivalents above
	// are the MCP-callable tools. No hand-rolled product HTTP is added here.
	{procedure: "REST PUT /api/v1/upload/{sessionId}", tool: "", class: ClassSafe, access: "mutation", scopeOverride: "uploads/create", transport: "rest", registeredBy: "integration", boundedArgs: "single session id; bounded raw byte body; optional X-Upload-Offset", sourceRef: "internal/rpc/server.go:296; internal/rpc/handlers/upload.go:42,59,105-111", equivalentFlow: "init_upload -> upload_chunk -> get_upload_status -> save_uploaded_file / import_uploaded_mod / upload_datapack / import_uploaded_modpack", description: "Scoped streaming upload byte channel, fail-closed at uploads/create with the server-local temp path deliberately withheld."},
	{procedure: "REST GET /api/v1/download/{sessionId}", tool: "", class: ClassSafe, access: "read", scopeOverride: "files/read[session.server_id]", transport: "rest", registeredBy: "integration", boundedArgs: "single session id; optional Range header", sourceRef: "internal/rpc/server.go:299; internal/rpc/handlers/download.go:29,65,109-119", equivalentFlow: "init_file_download / download_archive / export_world -> scoped GET by browser or CLI", description: "Scoped streaming download byte channel, fail-closed on the session's own server id."},
	{procedure: "REST /api/v1/servers/{id}/mods/*", tool: "", class: ClassSafe, access: "mutation", scopeOverride: "mods/read|create[server_id]", transport: "rest", registeredBy: "integration", boundedArgs: "server id + bounded mod query/install arguments", sourceRef: "internal/rpc/server.go:310-313; internal/rpc/handlers/mod_search.go:209", equivalentFlow: "search_mods / get_mod_versions / install_mods", description: "Legacy JSON adapter sharing one operation with ModBrowseService; retained for the ADR 0012 deprecation window."},
	{procedure: "REST /api/v1/packwiz/*", tool: "", class: ClassSafe, access: "mutation", scopeOverride: "modpacks/*", transport: "rest", registeredBy: "integration", boundedArgs: "operator Studio arguments; fail-closed policy at packwiz.go:106", sourceRef: "internal/rpc/server.go:315-320; internal/rpc/handlers/packwiz.go:106", equivalentFlow: "packwiz_list_packs / packwiz_get_pack for inventory reads only", description: "Packwiz Studio authoring and deployment REST surface. Mutations, raw content and update checks remain outside the MCP read tools."},
}

// ContentToolDescriptors is the exported hand-off list. All factual tuple
// fields are joined from the generated catalog so the integration owner can
// generate catalog_data.go / exposedTools from this single artifact.
var ContentToolDescriptors = buildContentDescriptors()

func buildContentDescriptors() []ContentToolDescriptor {
	base := make(map[string]CatalogEntry, len(rawCatalogEntries))
	for _, e := range rawCatalogEntries {
		base[e.Procedure] = e
	}
	out := make([]ContentToolDescriptor, 0, len(contentDecisions))
	for _, d := range contentDecisions {
		entry := ContentToolDescriptor{
			Tool:           d.tool,
			Procedure:      d.procedure,
			Scope:          d.scopeOverride,
			Class:          d.class,
			Access:         d.access,
			Confirm:        d.confirm,
			Build:          PanelBuildStableV140,
			Transport:      d.transport,
			RegisteredBy:   d.registeredBy,
			BoundedArgs:    d.boundedArgs,
			SourceRef:      d.sourceRef,
			EquivalentFlow: d.equivalentFlow,
			Description:    d.description,
			ExcludeReason:  d.excludeReason,
		}
		if e, ok := base[d.procedure]; ok {
			entry.Procedure = e.Procedure
			entry.Service = e.Service
			entry.Method = e.Method
			entry.Client = e.Client
			entry.Scope = e.Scope
			entry.Build = e.Build
		} else if entry.Scope == "" {
			panic("mcp content: non-catalog decision has no scope override: " + d.procedure)
		}
		if entry.Transport == "" {
			entry.Transport = "rpc"
		}
		if entry.RegisteredBy == "" {
			entry.RegisteredBy = "content"
		}
		out = append(out, entry)
	}
	return out
}

// ContentToolCount is the number of RPC tools newly registered by this family.
// Procedures already registered by PR522 and REST workflow descriptors are
// counted separately by ContentCoverageCounts.
func ContentToolCount() int {
	n := 0
	for _, d := range ContentToolDescriptors {
		if d.Transport == "rpc" && d.RegisteredBy == "content" {
			n++
		}
	}
	return n
}

// ContentCoverageCounts reports the three coverage numbers independently:
// listed descriptors, reachable RPCs (this family plus the first slice), and
// tools verified by the fixture tests. REST workflow descriptors are listed
// but are not callable MCP tools; their typed-RPC equivalents are.
type ContentCoverageCounts struct {
	ListedDescriptors int
	ListedRPCs        int
	ListedREST        int
	ReachableRPCs     int
	NewContentTools   int
	FirstSliceTools   int
	VerifiedTools     int
}

func CoverageCounts() ContentCoverageCounts {
	var c ContentCoverageCounts
	for _, d := range ContentToolDescriptors {
		c.ListedDescriptors++
		if d.Transport == "rpc" {
			c.ListedRPCs++
			if d.RegisteredBy == "content" {
				c.NewContentTools++
			} else {
				c.FirstSliceTools++
			}
			c.ReachableRPCs++
		} else {
			c.ListedREST++
		}
	}
	return c
}

var contentSpecs = func() map[string]ToolSpec {
	m := make(map[string]ToolSpec, len(ContentToolDescriptors))
	for _, d := range ContentToolDescriptors {
		if d.Transport != "rpc" || d.RegisteredBy != "content" {
			continue
		}
		m[d.Tool] = ToolSpec{
			Name: d.Tool, Procedure: d.Procedure, Class: d.Class,
			Confirm: d.Confirm, Description: d.Description,
		}
	}
	return m
}()

func contentSpecFor(name string) ToolSpec { return contentSpecs[name] }

func contentBuildFor(name string) PanelBuild {
	for _, d := range ContentToolDescriptors {
		if d.Tool == name {
			return d.Build
		}
	}
	return PanelBuildStableV140
}

// ContentClients owns typed generated Connect clients for the content
// families. It is separate from Clients so the first slice and the shared
// files stay untouched; the integration owner constructs it alongside the
// existing clients and passes it to registerContentTools.
type ContentClients struct {
	Mod       cpconnect.ModServiceClient
	ModBrowse cpconnect.ModBrowseServiceClient
	Modpack   cpconnect.ModpackServiceClient
	Packwiz   cpconnect.PackwizServiceClient
	Datapack  cpconnect.DatapackServiceClient
	Module    cpconnect.ModuleServiceClient
	File      cpconnect.FileServiceClient
	Upload    cpconnect.UploadServiceClient
}

// NewContentClients constructs the typed clients with the same bearer
// interceptor as the first-slice clients. An empty token is refused.
func NewContentClients(httpClient connect.HTTPClient, baseURL, token string) (*ContentClients, error) {
	if httpClient == nil {
		httpClient = newHTTPClient()
	}
	if token == "" {
		return nil, fmt.Errorf("API token is required for content clients")
	}
	opts := []connect.ClientOption{connect.WithInterceptors(bearerInterceptor(token))}
	return &ContentClients{
		Mod:       cpconnect.NewModServiceClient(httpClient, baseURL, opts...),
		ModBrowse: cpconnect.NewModBrowseServiceClient(httpClient, baseURL, opts...),
		Modpack:   cpconnect.NewModpackServiceClient(httpClient, baseURL, opts...),
		Packwiz:   cpconnect.NewPackwizServiceClient(httpClient, baseURL, append(opts, connect.WithReadMaxBytes(packwizReadMaxBytes))...),
		Datapack:  cpconnect.NewDatapackServiceClient(httpClient, baseURL, opts...),
		Module:    cpconnect.NewModuleServiceClient(httpClient, baseURL, opts...),
		File:      cpconnect.NewFileServiceClient(httpClient, baseURL, opts...),
		Upload:    cpconnect.NewUploadServiceClient(httpClient, baseURL, opts...),
	}, nil
}

// contentFailure converts any error to the shared structured ToolOutput. The
// minimum build comes from our own descriptor because these tools are not yet
// in the shared catalog that DeclaredBuild consults.
func contentFailure(spec ToolSpec, err error) (*sdkmcp.CallToolResult, ToolOutput, error) {
	te := toToolError(spec, err)
	if te.Kind == ErrKindCapability {
		build := contentBuildFor(spec.Name)
		te.MinimumPanelBuild = string(build)
		te.Message = fmt.Sprintf(
			"tool %q requires panel build %s containing %s; the current panel reported an unimplemented procedure (%s). The tool is available in every build of this MCP server; upgrade the controller to use it.",
			spec.Name, build, spec.Procedure, te.Code,
		)
	}
	out := ToolOutput{Status: "error", Error: &te}
	return &sdkmcp.CallToolResult{
		IsError: true,
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: te.Message}},
	}, out, nil
}

// addContentTool binds one typed tool with a confirm gate and structured
// failures. It mirrors the first-slice addTool without depending on the shared
// exposedTools list.
func addContentTool[In any](s *sdkmcp.Server, name string, cl *ContentClients,
	run func(context.Context, *ContentClients, In) (any, error)) {
	if !toolAllowedOn(s, name) {
		return
	}
	spec := contentSpecFor(name)
	sdkmcp.AddTool(s, &sdkmcp.Tool{Name: spec.Name, Description: spec.Description},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in In) (*sdkmcp.CallToolResult, ToolOutput, error) {
			if spec.Confirm {
				if c, ok := any(in).(interface{ Confirmation() bool }); !ok || !c.Confirmation() {
					return contentFailure(spec, &ToolError{
						Kind:      ErrKindConfirmationRequired,
						Tool:      spec.Name,
						Procedure: spec.Procedure,
						Message:   spec.Name + " is a mutation: set confirm=true with the exact target id to proceed",
					})
				}
			}
			result, err := run(ctx, cl, in)
			if err != nil {
				return contentFailure(spec, err)
			}
			return nil, ToolOutput{Status: "ok", Result: result}, nil
		})
}

// --- bounded argument helpers --------------------------------------------

const (
	contentMaxItems    = 10
	contentMaxPaths    = 20
	contentMaxList     = 50
	contentMaxOffset   = 10000
	contentMaxPort     = 65535
	contentMaxMemoryMB = 65536

	// Scoped streaming caps mirror the storage-streaming contract: 256 KiB
	// frames and a bounded total upload of 4 GiB. These are enforced in Go.
	contentChunkBytes     = 256 * 1024
	contentMaxChunkIndex  = 100000
	contentMaxUploadBytes = int64(4) * 1024 * 1024 * 1024
)

var fabricOptimizationIDs = map[string]bool{
	"lithium": true, "ferrite-core": true, "modernfix": true, "c2me-fabric": true,
}

func contentOptionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func contentError(tool, field, rule string) error {
	return &ToolError{
		Kind:      ErrKindInvalidArguments,
		Tool:      tool,
		Procedure: contentSpecFor(tool).Procedure,
		Message:   fmt.Sprintf("%s for tool %q: %s", field, tool, rule),
	}
}

func contentBoundList(tool, field string, n, max int) error {
	if n > max {
		return contentError(tool, field, fmt.Sprintf("at most %d entries are allowed", max))
	}
	return nil
}

func contentOffset(tool string, v int32) error {
	if v < 0 || v > contentMaxOffset {
		return contentError(tool, "offset", fmt.Sprintf("must be between 0 and %d", contentMaxOffset))
	}
	return nil
}

func contentPort(tool, field string, v int32) error {
	if v < 0 || v > contentMaxPort {
		return contentError(tool, field, fmt.Sprintf("must be between 0 and %d", contentMaxPort))
	}
	return nil
}

func contentFilename(tool, field, value string) error {
	if err := requireID(tool, field, value); err != nil {
		return err
	}
	if strings.ContainsAny(value, "/\\") {
		return contentError(tool, field, "must be a file name, not a path")
	}
	return nil
}

// contentHTTPSURL accepts only absolute https URLs without embedded userinfo,
// so an MCP argument can never carry credentials or a private-network hint.
func contentHTTPSURL(tool, field, value string) error {
	if err := requireText(tool, field, value, 512); err != nil {
		return err
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
		return contentError(tool, field, "must be an absolute https URL without embedded credentials")
	}
	return nil
}

func contentEnum(tool, field, value string, values map[string]int32) (int32, error) {
	if value == "" {
		return 0, nil
	}
	n, ok := values[strings.ToUpper(value)]
	if !ok {
		return 0, contentError(tool, field, "unknown enum name")
	}
	return n, nil
}

// --- argument structs -----------------------------------------------------

type contentServerArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
}

type contentOptionalServerArgs struct {
	ServerID string `json:"server_id,omitempty" jsonschema:"optional exact server id, at most 128 characters"`
}

type contentServerIDArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	ID       string `json:"id" jsonschema:"exact resource id, 1 to 128 characters"`
}

type contentIDArgs struct {
	ID string `json:"id" jsonschema:"exact resource id, 1 to 128 characters"`
}

type contentSlugArgs struct {
	Slug string `json:"slug" jsonschema:"exact slug, 1 to 128 characters"`
}

type contentOperationArgs struct {
	OperationID string `json:"operation_id" jsonschema:"exact operation id, 1 to 128 characters"`
}

type contentServerTaskArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	TaskID   string `json:"task_id" jsonschema:"exact task id, 1 to 128 characters"`
}

type contentServerPathArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	Path     string `json:"path" jsonschema:"server-relative file path, 1 to 512 characters"`
}

type contentServerPathsArgs struct {
	ServerID string   `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	Paths    []string `json:"paths" jsonschema:"at most 20 server-relative paths, each at most 512 characters"`
}

type contentQueryArgs struct {
	Query       string `json:"query,omitempty" jsonschema:"text filter, at most 64 characters"`
	GameVersion string `json:"game_version,omitempty" jsonschema:"game version, at most 64 characters"`
	Indexer     string `json:"indexer,omitempty" jsonschema:"indexer name, at most 64 characters"`
	Offset      int32  `json:"offset,omitempty" jsonschema:"result offset from 0 to 10000"`
	Limit       int32  `json:"limit,omitempty" jsonschema:"result limit from 1 to 100, default 20"`
}

type contentModpackSearchArgs struct {
	Query       string `json:"query,omitempty" jsonschema:"text filter, at most 64 characters"`
	GameVersion string `json:"game_version,omitempty" jsonschema:"game version, at most 64 characters"`
	ModLoader   string `json:"mod_loader,omitempty" jsonschema:"mod loader, at most 64 characters"`
	Indexer     string `json:"indexer,omitempty" jsonschema:"indexer name, at most 64 characters"`
	Page        int32  `json:"page,omitempty" jsonschema:"page number from 0 to 1000"`
	PageSize    int32  `json:"page_size,omitempty" jsonschema:"page size from 1 to 100, default 20"`
}

type contentModpackVersionArgs struct {
	ID          string `json:"id" jsonschema:"exact modpack id, 1 to 128 characters"`
	GameVersion string `json:"game_version,omitempty" jsonschema:"game version, at most 32 characters"`
	ModLoader   string `json:"mod_loader,omitempty" jsonschema:"mod loader, at most 32 characters"`
}

type contentModBrowseSearchArgs struct {
	ServerID  string `json:"server_id,omitempty" jsonschema:"optional exact server id; empty is the Studio none-browse mode"`
	Query     string `json:"query" jsonschema:"search query, 1 to 256 characters"`
	Platform  string `json:"platform,omitempty" jsonschema:"platform, at most 32 characters"`
	Loader    string `json:"loader,omitempty" jsonschema:"loader, at most 32 characters"`
	MCVersion string `json:"mc_version,omitempty" jsonschema:"Minecraft version, at most 32 characters"`
	Side      string `json:"side,omitempty" jsonschema:"client/server side, at most 32 characters"`
}

type contentModVersionsArgs struct {
	ServerID  string `json:"server_id,omitempty" jsonschema:"optional exact server id"`
	Project   string `json:"project" jsonschema:"exact mod project id, 1 to 128 characters"`
	Platform  string `json:"platform,omitempty" jsonschema:"platform, at most 32 characters"`
	Loader    string `json:"loader,omitempty" jsonschema:"loader, at most 32 characters"`
	MCVersion string `json:"mc_version,omitempty" jsonschema:"Minecraft version, at most 32 characters"`
}

type contentInstallModEntry struct {
	Name        string `json:"name" jsonschema:"display name, 1 to 128 characters"`
	FileName    string `json:"file_name" jsonschema:"jar file name, 1 to 128 characters"`
	DownloadURL string `json:"download_url" jsonschema:"absolute https URL without embedded credentials"`
}

type contentInstallModsArgs struct {
	ServerID string                   `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	Items    []contentInstallModEntry `json:"items" jsonschema:"at most 10 jars to install"`
	Confirm  bool                     `json:"confirm,omitempty" jsonschema:"must be true to install mods"`
}

func (a contentInstallModsArgs) Confirmation() bool { return a.Confirm }

type contentModuleTemplateListArgs struct {
	Type     string `json:"type,omitempty" jsonschema:"optional ModuleTemplateType enum name"`
	Category string `json:"category,omitempty" jsonschema:"category filter, at most 64 characters"`
}

type contentModuleFilterArgs struct {
	ServerID   string `json:"server_id,omitempty" jsonschema:"optional exact server id, at most 128 characters"`
	TemplateID string `json:"template_id,omitempty" jsonschema:"optional exact template id, at most 128 characters"`
}

type contentAliasArgs struct {
	ServerID string `json:"server_id,omitempty" jsonschema:"optional exact server id, at most 128 characters"`
	ModuleID string `json:"module_id,omitempty" jsonschema:"optional exact module id, at most 128 characters"`
}

type contentModuleLogsArgs struct {
	ID   string `json:"id" jsonschema:"exact module id, 1 to 128 characters"`
	Tail int32  `json:"tail,omitempty" jsonschema:"log lines from 1 to 100, default 20"`
}

// --- mutation argument structs -------------------------------------------

type contentImportModArgs struct {
	ServerID        string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	UploadSessionID string `json:"upload_session_id" jsonschema:"single completed upload session id, 1 to 128 characters"`
	DisplayName     string `json:"display_name,omitempty" jsonschema:"display name, at most 256 characters"`
	Description     string `json:"description,omitempty" jsonschema:"description, at most 256 characters"`
	Stage           bool   `json:"stage,omitempty" jsonschema:"stage instead of writing the live mods directory"`
	Confirm         bool   `json:"confirm,omitempty" jsonschema:"must be true to import a mod"`
}

func (a contentImportModArgs) Confirmation() bool { return a.Confirm }

type contentUpdateModArgs struct {
	ServerID    string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	ModID       string `json:"mod_id" jsonschema:"exact mod id, 1 to 128 characters"`
	DisplayName string `json:"display_name,omitempty" jsonschema:"display name, at most 256 characters"`
	Description string `json:"description,omitempty" jsonschema:"description, at most 256 characters"`
	Enabled     *bool  `json:"enabled,omitempty" jsonschema:"optional enabled flag"`
	Confirm     bool   `json:"confirm,omitempty" jsonschema:"must be true to update a mod"`
}

func (a contentUpdateModArgs) Confirmation() bool { return a.Confirm }

type contentDeleteModArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	ModID    string `json:"mod_id" jsonschema:"exact mod id, 1 to 128 characters"`
	Confirm  bool   `json:"confirm,omitempty" jsonschema:"must be true to delete a mod"`
}

func (a contentDeleteModArgs) Confirmation() bool { return a.Confirm }

type contentFabricInstallArgs struct {
	ServerID string   `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	ModIDs   []string `json:"mod_ids,omitempty" jsonschema:"at most 4 ids from lithium, ferrite-core, modernfix, c2me-fabric"`
	Confirm  bool     `json:"confirm,omitempty" jsonschema:"must be true to install the optimization stack"`
}

func (a contentFabricInstallArgs) Confirmation() bool { return a.Confirm }

type contentSyncModpacksArgs struct {
	Query       string `json:"query,omitempty" jsonschema:"text filter, at most 64 characters"`
	GameVersion string `json:"game_version,omitempty" jsonschema:"game version, at most 64 characters"`
	ModLoader   string `json:"mod_loader,omitempty" jsonschema:"mod loader, at most 64 characters"`
	Indexer     string `json:"indexer,omitempty" jsonschema:"indexer name, at most 64 characters"`
	Confirm     bool   `json:"confirm,omitempty" jsonschema:"must be true to sync modpacks"`
}

func (a contentSyncModpacksArgs) Confirmation() bool { return a.Confirm }

type contentImportModpackArgs struct {
	UploadSessionID string `json:"upload_session_id" jsonschema:"single completed upload session id, 1 to 128 characters"`
	Name            string `json:"name,omitempty" jsonschema:"name, at most 256 characters"`
	Description     string `json:"description,omitempty" jsonschema:"description, at most 256 characters"`
	Confirm         bool   `json:"confirm,omitempty" jsonschema:"must be true to import a modpack"`
}

func (a contentImportModpackArgs) Confirmation() bool { return a.Confirm }

type contentInstallDatapackArgs struct {
	ServerID    string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	DatapackID  string `json:"datapack_id" jsonschema:"single datapack index id, 1 to 128 characters"`
	GameVersion string `json:"game_version,omitempty" jsonschema:"game version, at most 128 characters"`
	FileName    string `json:"file_name,omitempty" jsonschema:"stored filename, at most 128 characters"`
	Indexer     string `json:"indexer,omitempty" jsonschema:"indexer name, at most 128 characters"`
	Stage       bool   `json:"stage,omitempty" jsonschema:"stage instead of writing the live world"`
	Confirm     bool   `json:"confirm,omitempty" jsonschema:"must be true to install a datapack"`
}

func (a contentInstallDatapackArgs) Confirmation() bool { return a.Confirm }

type contentUploadDatapackArgs struct {
	ServerID        string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	UploadSessionID string `json:"upload_session_id" jsonschema:"single completed upload session id, 1 to 128 characters"`
	FileName        string `json:"file_name,omitempty" jsonschema:"stored filename, at most 128 characters"`
	Stage           bool   `json:"stage,omitempty" jsonschema:"stage instead of writing the live world"`
	Confirm         bool   `json:"confirm,omitempty" jsonschema:"must be true to import a datapack"`
}

func (a contentUploadDatapackArgs) Confirmation() bool { return a.Confirm }

type contentDeleteDatapackArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	FileName string `json:"file_name" jsonschema:"single installed datapack file name, 1 to 128 characters"`
	Confirm  bool   `json:"confirm,omitempty" jsonschema:"must be true to delete a datapack"`
}

func (a contentDeleteDatapackArgs) Confirmation() bool { return a.Confirm }

type contentCreateModuleTemplateArgs struct {
	Name            string `json:"name" jsonschema:"display name, 1 to 128 characters"`
	Description     string `json:"description,omitempty" jsonschema:"description, at most 256 characters"`
	DockerImage     string `json:"docker_image" jsonschema:"docker image reference, 1 to 128 characters"`
	Category        string `json:"category,omitempty" jsonschema:"category, at most 64 characters"`
	Icon            string `json:"icon,omitempty" jsonschema:"icon name, at most 64 characters"`
	HealthCheckPath string `json:"health_check_path,omitempty" jsonschema:"health path, at most 128 characters"`
	HealthCheckPort int32  `json:"health_check_port,omitempty" jsonschema:"health port from 0 to 65535"`
	RequiresServer  bool   `json:"requires_server,omitempty" jsonschema:"require a parent server"`
	SupportsProxy   bool   `json:"supports_proxy,omitempty" jsonschema:"allow proxy routing"`
	Confirm         bool   `json:"confirm,omitempty" jsonschema:"must be true to create a template"`
}

func (a contentCreateModuleTemplateArgs) Confirmation() bool { return a.Confirm }

type contentUpdateModuleTemplateArgs struct {
	ID              string `json:"id" jsonschema:"exact template id, 1 to 128 characters"`
	Name            string `json:"name,omitempty" jsonschema:"display name, at most 128 characters"`
	Description     string `json:"description,omitempty" jsonschema:"description, at most 256 characters"`
	DockerImage     string `json:"docker_image,omitempty" jsonschema:"docker image reference, at most 128 characters"`
	Category        string `json:"category,omitempty" jsonschema:"category, at most 64 characters"`
	Icon            string `json:"icon,omitempty" jsonschema:"icon name, at most 64 characters"`
	HealthCheckPath string `json:"health_check_path,omitempty" jsonschema:"health path, at most 128 characters"`
	Confirm         bool   `json:"confirm,omitempty" jsonschema:"must be true to update a template"`
}

func (a contentUpdateModuleTemplateArgs) Confirmation() bool { return a.Confirm }

type contentIDConfirmArgs struct {
	ID      string `json:"id" jsonschema:"exact resource id, 1 to 128 characters"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true to perform this mutation"`
}

func (a contentIDConfirmArgs) Confirmation() bool { return a.Confirm }

type contentCreateModuleArgs struct {
	Name             string `json:"name" jsonschema:"display name, 1 to 128 characters"`
	ServerID         string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	TemplateID       string `json:"template_id" jsonschema:"exact template id, 1 to 128 characters"`
	Memory           int32  `json:"memory,omitempty" jsonschema:"memory limit in MB from 0 to 65536"`
	AutoStart        bool   `json:"auto_start,omitempty" jsonschema:"start with the parent server"`
	StartImmediately bool   `json:"start_immediately,omitempty" jsonschema:"start after creation"`
	Confirm          bool   `json:"confirm,omitempty" jsonschema:"must be true to create a module"`
}

func (a contentCreateModuleArgs) Confirmation() bool { return a.Confirm }

type contentUpdateModuleArgs struct {
	ID                    string `json:"id" jsonschema:"exact module id, 1 to 128 characters"`
	Name                  string `json:"name,omitempty" jsonschema:"display name, at most 128 characters"`
	Memory                *int32 `json:"memory,omitempty" jsonschema:"memory limit in MB from 0 to 65536"`
	AutoStart             *bool  `json:"auto_start,omitempty" jsonschema:"optional auto-start flag"`
	Detached              *bool  `json:"detached,omitempty" jsonschema:"optional detached flag"`
	FollowServerLifecycle *bool  `json:"follow_server_lifecycle,omitempty" jsonschema:"optional lifecycle flag"`
	Stage                 bool   `json:"stage,omitempty" jsonschema:"stage the change instead of applying it live"`
	Confirm               bool   `json:"confirm,omitempty" jsonschema:"must be true to update a module"`
}

func (a contentUpdateModuleArgs) Confirmation() bool { return a.Confirm }

// --- full-coverage argument structs ---------------------------------------

type contentURLArgs struct {
	URL string `json:"url" jsonschema:"absolute https URL without embedded credentials, at most 512 characters"`
}

type contentImportRemoteModpackArgs struct {
	URL         string `json:"url" jsonschema:"absolute https URL without embedded credentials, at most 512 characters"`
	Name        string `json:"name,omitempty" jsonschema:"name, at most 128 characters"`
	Description string `json:"description,omitempty" jsonschema:"description, at most 256 characters"`
	MCVersion   string `json:"mc_version,omitempty" jsonschema:"Minecraft version, at most 32 characters"`
	ModLoader   string `json:"mod_loader,omitempty" jsonschema:"mod loader, at most 32 characters"`
	Confirm     bool   `json:"confirm,omitempty" jsonschema:"must be true to import a remote modpack"`
}

func (a contentImportRemoteModpackArgs) Confirmation() bool { return a.Confirm }

type contentFileWriteArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	Path     string `json:"path" jsonschema:"server-relative file path, 1 to 512 characters"`
	Content  string `json:"content" jsonschema:"base64-encoded content, at most 256 KiB decoded"`
	Stage    bool   `json:"stage,omitempty" jsonschema:"stage instead of writing the live path"`
	Confirm  bool   `json:"confirm,omitempty" jsonschema:"must be true to write a file"`
}

func (a contentFileWriteArgs) Confirmation() bool { return a.Confirm }

type contentFileDeleteArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	Path     string `json:"path" jsonschema:"single server-relative path, 1 to 512 characters"`
	Stage    bool   `json:"stage,omitempty" jsonschema:"stage instead of deleting live bytes"`
	Confirm  bool   `json:"confirm,omitempty" jsonschema:"must be true to delete a file"`
}

func (a contentFileDeleteArgs) Confirmation() bool { return a.Confirm }

type contentFileRenameArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	Path     string `json:"path" jsonschema:"server-relative file path, 1 to 512 characters"`
	NewName  string `json:"new_name" jsonschema:"new file name without path separators, 1 to 128 characters"`
	Confirm  bool   `json:"confirm,omitempty" jsonschema:"must be true to rename a file"`
}

func (a contentFileRenameArgs) Confirmation() bool { return a.Confirm }

type contentFilePathArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	Path     string `json:"path" jsonschema:"server-relative path, 1 to 512 characters"`
	Confirm  bool   `json:"confirm,omitempty" jsonschema:"must be true to perform this file mutation"`
}

func (a contentFilePathArgs) Confirmation() bool { return a.Confirm }

type contentFileMoveArgs struct {
	ServerID        string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	SourcePath      string `json:"source_path" jsonschema:"server-relative source path, 1 to 512 characters"`
	DestinationPath string `json:"destination_path" jsonschema:"server-relative destination path, 1 to 512 characters"`
	Confirm         bool   `json:"confirm,omitempty" jsonschema:"must be true to move or copy a file"`
}

func (a contentFileMoveArgs) Confirmation() bool { return a.Confirm }

type contentCreateArchiveArgs struct {
	ServerID        string   `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	Paths           []string `json:"paths" jsonschema:"at most 20 server-relative paths"`
	DestinationPath string   `json:"destination_path,omitempty" jsonschema:"server-relative destination, at most 512 characters"`
	ArchiveName     string   `json:"archive_name" jsonschema:"archive file name, 1 to 128 characters"`
	Confirm         bool     `json:"confirm,omitempty" jsonschema:"must be true to create an archive"`
}

func (a contentCreateArchiveArgs) Confirmation() bool { return a.Confirm }

type contentRemoteArchiveArgs struct {
	ServerID        string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	URL             string `json:"url" jsonschema:"absolute https URL without embedded credentials, at most 512 characters"`
	DestinationPath string `json:"destination_path" jsonschema:"server-relative destination, 1 to 512 characters"`
	SHA256Checksum  string `json:"sha256_checksum" jsonschema:"64 hex characters"`
	AutoExtract     bool   `json:"auto_extract,omitempty" jsonschema:"extract after verified download"`
	Confirm         bool   `json:"confirm,omitempty" jsonschema:"must be true to start a remote archive download"`
}

func (a contentRemoteArchiveArgs) Confirmation() bool { return a.Confirm }

type contentSaveUploadedFileArgs struct {
	ServerID        string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	UploadSessionID string `json:"upload_session_id" jsonschema:"single completed upload session id, 1 to 128 characters"`
	DestinationPath string `json:"destination_path" jsonschema:"server-relative target directory, at most 512 characters"`
	Filename        string `json:"filename" jsonschema:"target file name without separators, 1 to 128 characters"`
	Confirm         bool   `json:"confirm,omitempty" jsonschema:"must be true to save an uploaded file"`
}

func (a contentSaveUploadedFileArgs) Confirmation() bool { return a.Confirm }

type contentInitUploadArgs struct {
	Filename  string `json:"filename" jsonschema:"file name without separators, 1 to 128 characters"`
	TotalSize int64  `json:"total_size" jsonschema:"total bytes from 1 to 4294967296 (4 GiB)"`
	ChunkSize int32  `json:"chunk_size" jsonschema:"chunk bytes from 1 to 262144 (256 KiB)"`
	Confirm   bool   `json:"confirm,omitempty" jsonschema:"must be true to create an upload session"`
}

func (a contentInitUploadArgs) Confirmation() bool { return a.Confirm }

type contentSessionArgs struct {
	SessionID string `json:"session_id" jsonschema:"single upload session id, 1 to 128 characters"`
}

type contentUploadChunkArgs struct {
	SessionID  string `json:"session_id" jsonschema:"single upload session id, 1 to 128 characters"`
	ChunkIndex int32  `json:"chunk_index" jsonschema:"chunk index from 0 to 100000"`
	Data       string `json:"data" jsonschema:"base64-encoded chunk bytes, at most 256 KiB decoded"`
	Confirm    bool   `json:"confirm,omitempty" jsonschema:"must be true to upload a chunk"`
}

func (a contentUploadChunkArgs) Confirmation() bool { return a.Confirm }

type contentCancelUploadArgs struct {
	SessionID string `json:"session_id" jsonschema:"single upload session id, 1 to 128 characters"`
	Confirm   bool   `json:"confirm,omitempty" jsonschema:"must be true to cancel an upload session"`
}

func (a contentCancelUploadArgs) Confirmation() bool { return a.Confirm }

// registerContentTools registers every exposed content tool. The integration
// owner calls this once from server construction alongside registerTools.
func registerContentTools(s *sdkmcp.Server, cl *ContentClients) {
	if s == nil || cl == nil {
		panic("mcp.registerContentTools: nil server or clients")
	}

	registerPackwizReadTools(s, cl)

	// --- ModService -------------------------------------------------------
	addContentTool(s, "list_mods", cl, func(ctx context.Context, cl *ContentClients, in contentServerArgs) (any, error) {
		if err := requireID("list_mods", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Mod.ListMods, v1.ListModsRequest{ServerId: in.ServerID})
	})
	addContentTool(s, "get_mod", cl, func(ctx context.Context, cl *ContentClients, in contentServerIDArgs) (any, error) {
		if err := requireID("get_mod", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("get_mod", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Mod.GetMod, v1.GetModRequest{ServerId: in.ServerID, ModId: in.ID})
	})
	addContentTool(s, "get_fabric_optimization_stack", cl, func(ctx context.Context, cl *ContentClients, in contentServerArgs) (any, error) {
		if err := requireID("get_fabric_optimization_stack", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Mod.GetFabricOptimizationStack, v1.GetFabricOptimizationStackRequest{ServerId: in.ServerID})
	})
	addContentTool(s, "import_uploaded_mod", cl, func(ctx context.Context, cl *ContentClients, in contentImportModArgs) (any, error) {
		if err := requireID("import_uploaded_mod", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("import_uploaded_mod", "upload_session_id", in.UploadSessionID); err != nil {
			return nil, err
		}
		if err := requireText("import_uploaded_mod", "display_name", in.DisplayName, 256); err != nil {
			return nil, err
		}
		if err := requireText("import_uploaded_mod", "description", in.Description, 256); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Mod.ImportUploadedMod, v1.ImportUploadedModRequest{
			ServerId: in.ServerID, UploadSessionId: in.UploadSessionID,
			DisplayName: in.DisplayName, Description: in.Description, Stage: in.Stage,
		})
	})
	addContentTool(s, "update_mod", cl, func(ctx context.Context, cl *ContentClients, in contentUpdateModArgs) (any, error) {
		if err := requireID("update_mod", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("update_mod", "mod_id", in.ModID); err != nil {
			return nil, err
		}
		if err := requireText("update_mod", "display_name", in.DisplayName, 256); err != nil {
			return nil, err
		}
		if err := requireText("update_mod", "description", in.Description, 256); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Mod.UpdateMod, v1.UpdateModRequest{
			ServerId: in.ServerID, ModId: in.ModID,
			DisplayName: contentOptionalString(in.DisplayName),
			Description: contentOptionalString(in.Description),
			Enabled:     in.Enabled,
		})
	})
	addContentTool(s, "delete_mod", cl, func(ctx context.Context, cl *ContentClients, in contentDeleteModArgs) (any, error) {
		if err := requireID("delete_mod", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("delete_mod", "mod_id", in.ModID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Mod.DeleteMod, v1.DeleteModRequest{ServerId: in.ServerID, ModId: in.ModID})
	})
	addContentTool(s, "install_fabric_optimization_stack", cl, func(ctx context.Context, cl *ContentClients, in contentFabricInstallArgs) (any, error) {
		if err := requireID("install_fabric_optimization_stack", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := contentBoundList("install_fabric_optimization_stack", "mod_ids", len(in.ModIDs), 4); err != nil {
			return nil, err
		}
		for _, id := range in.ModIDs {
			if !fabricOptimizationIDs[id] {
				return nil, contentError("install_fabric_optimization_stack", "mod_ids", "unknown optimization mod id")
			}
		}
		return invoke(ctx, cl.Mod.InstallFabricOptimizationStack, v1.InstallFabricOptimizationStackRequest{
			ServerId: in.ServerID, ModIds: in.ModIDs,
		})
	})

	// --- ModBrowseService -------------------------------------------------
	addContentTool(s, "search_mods", cl, func(ctx context.Context, cl *ContentClients, in contentModBrowseSearchArgs) (any, error) {
		if err := optionalID("search_mods", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireText("search_mods", "query", in.Query, maxQueryLen); err != nil {
			return nil, err
		}
		if in.Query == "" {
			return nil, contentError("search_mods", "query", "a search query is required")
		}
		for field, value := range map[string]string{
			"platform": in.Platform, "loader": in.Loader, "mc_version": in.MCVersion, "side": in.Side,
		} {
			if err := requireText("search_mods", field, value, 32); err != nil {
				return nil, err
			}
		}
		return invoke(ctx, cl.ModBrowse.SearchMods, v1.SearchModsRequest{
			ServerId: in.ServerID, Query: in.Query, Platform: in.Platform,
			Loader: in.Loader, McVersion: in.MCVersion, Side: in.Side,
		})
	})
	addContentTool(s, "get_mod_versions", cl, func(ctx context.Context, cl *ContentClients, in contentModVersionsArgs) (any, error) {
		if err := optionalID("get_mod_versions", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("get_mod_versions", "project", in.Project); err != nil {
			return nil, err
		}
		for field, value := range map[string]string{
			"platform": in.Platform, "loader": in.Loader, "mc_version": in.MCVersion,
		} {
			if err := requireText("get_mod_versions", field, value, 32); err != nil {
				return nil, err
			}
		}
		return invoke(ctx, cl.ModBrowse.GetModVersions, v1.GetModVersionsRequest{
			ServerId: in.ServerID, Project: in.Project, Platform: in.Platform,
			Loader: in.Loader, McVersion: in.MCVersion,
		})
	})
	addContentTool(s, "install_mods", cl, func(ctx context.Context, cl *ContentClients, in contentInstallModsArgs) (any, error) {
		if err := requireID("install_mods", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := contentBoundList("install_mods", "items", len(in.Items), contentMaxItems); err != nil {
			return nil, err
		}
		if len(in.Items) == 0 {
			return nil, contentError("install_mods", "items", "at least one jar is required")
		}
		items := make([]*v1.InstallModEntry, 0, len(in.Items))
		for i, item := range in.Items {
			if err := contentFilename("install_mods", fmt.Sprintf("items[%d].name", i), item.Name); err != nil {
				return nil, err
			}
			if err := contentFilename("install_mods", fmt.Sprintf("items[%d].file_name", i), item.FileName); err != nil {
				return nil, err
			}
			if err := contentHTTPSURL("install_mods", fmt.Sprintf("items[%d].download_url", i), item.DownloadURL); err != nil {
				return nil, err
			}
			items = append(items, &v1.InstallModEntry{
				Name: item.Name, FileName: item.FileName, DownloadUrl: item.DownloadURL,
			})
		}
		return invoke(ctx, cl.ModBrowse.InstallMods, v1.InstallModsRequest{ServerId: in.ServerID, Items: items})
	})

	// --- ModpackService ---------------------------------------------------
	addContentTool(s, "search_modpacks", cl, func(ctx context.Context, cl *ContentClients, in contentModpackSearchArgs) (any, error) {
		for field, value := range map[string]string{
			"query": in.Query, "game_version": in.GameVersion, "mod_loader": in.ModLoader, "indexer": in.Indexer,
		} {
			if err := requireText("search_modpacks", field, value, 64); err != nil {
				return nil, err
			}
		}
		if in.Page < 0 || in.Page > 1000 {
			return nil, contentError("search_modpacks", "page", "must be between 0 and 1000")
		}
		pageSize, err := limitOr("search_modpacks", in.PageSize)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Modpack.SearchModpacks, v1.SearchModpacksRequest{
			Query: in.Query, GameVersion: in.GameVersion, ModLoader: in.ModLoader,
			Indexer: in.Indexer, Page: in.Page, PageSize: pageSize,
		})
	})
	addContentTool(s, "get_modpack", cl, func(ctx context.Context, cl *ContentClients, in contentIDArgs) (any, error) {
		if err := requireID("get_modpack", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Modpack.GetModpack, v1.GetModpackRequest{Id: in.ID})
	})
	addContentTool(s, "get_modpack_by_slug", cl, func(ctx context.Context, cl *ContentClients, in contentSlugArgs) (any, error) {
		if err := requireID("get_modpack_by_slug", "slug", in.Slug); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Modpack.GetModpackBySlug, v1.GetModpackBySlugRequest{Slug: in.Slug})
	})
	addContentTool(s, "list_favorites", cl, func(ctx context.Context, cl *ContentClients, _ emptyArgs) (any, error) {
		return invoke(ctx, cl.Modpack.ListFavorites, v1.ListFavoritesRequest{})
	})
	addContentTool(s, "get_indexer_status", cl, func(ctx context.Context, cl *ContentClients, _ emptyArgs) (any, error) {
		return invoke(ctx, cl.Modpack.GetIndexerStatus, v1.GetIndexerStatusRequest{})
	})
	addContentTool(s, "get_modpack_config", cl, func(ctx context.Context, cl *ContentClients, in contentIDArgs) (any, error) {
		if err := requireID("get_modpack_config", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Modpack.GetModpackConfig, v1.GetModpackConfigRequest{Id: in.ID})
	})
	addContentTool(s, "get_modpack_files", cl, func(ctx context.Context, cl *ContentClients, in contentIDArgs) (any, error) {
		if err := requireID("get_modpack_files", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Modpack.GetModpackFiles, v1.GetModpackFilesRequest{Id: in.ID})
	})
	addContentTool(s, "get_modpack_versions", cl, func(ctx context.Context, cl *ContentClients, in contentModpackVersionArgs) (any, error) {
		if err := requireID("get_modpack_versions", "id", in.ID); err != nil {
			return nil, err
		}
		if err := requireText("get_modpack_versions", "game_version", in.GameVersion, 32); err != nil {
			return nil, err
		}
		if err := requireText("get_modpack_versions", "mod_loader", in.ModLoader, 32); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Modpack.GetModpackVersions, v1.GetModpackVersionsRequest{
			Id: in.ID, GameVersion: in.GameVersion, ModLoader: in.ModLoader,
		})
	})
	addContentTool(s, "toggle_favorite", cl, func(ctx context.Context, cl *ContentClients, in contentIDArgs) (any, error) {
		if err := requireID("toggle_favorite", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Modpack.ToggleFavorite, v1.ToggleFavoriteRequest{Id: in.ID})
	})
	addContentTool(s, "sync_modpacks", cl, func(ctx context.Context, cl *ContentClients, in contentSyncModpacksArgs) (any, error) {
		for field, value := range map[string]string{
			"query": in.Query, "game_version": in.GameVersion, "mod_loader": in.ModLoader, "indexer": in.Indexer,
		} {
			if err := requireText("sync_modpacks", field, value, 64); err != nil {
				return nil, err
			}
		}
		return invoke(ctx, cl.Modpack.SyncModpacks, v1.SyncModpacksRequest{
			Query: in.Query, GameVersion: in.GameVersion, ModLoader: in.ModLoader, Indexer: in.Indexer,
		})
	})
	addContentTool(s, "import_uploaded_modpack", cl, func(ctx context.Context, cl *ContentClients, in contentImportModpackArgs) (any, error) {
		if err := requireID("import_uploaded_modpack", "upload_session_id", in.UploadSessionID); err != nil {
			return nil, err
		}
		if err := requireText("import_uploaded_modpack", "name", in.Name, 256); err != nil {
			return nil, err
		}
		if err := requireText("import_uploaded_modpack", "description", in.Description, 256); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Modpack.ImportUploadedModpack, v1.ImportUploadedModpackRequest{
			UploadSessionId: in.UploadSessionID, Name: in.Name, Description: in.Description,
		})
	})
	addContentTool(s, "delete_modpack", cl, func(ctx context.Context, cl *ContentClients, in contentIDConfirmArgs) (any, error) {
		if err := requireID("delete_modpack", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Modpack.DeleteModpack, v1.DeleteModpackRequest{Id: in.ID})
	})
	addContentTool(s, "sync_modpack_files", cl, func(ctx context.Context, cl *ContentClients, in contentIDConfirmArgs) (any, error) {
		if err := requireID("sync_modpack_files", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Modpack.SyncModpackFiles, v1.SyncModpackFilesRequest{Id: in.ID})
	})

	// --- DatapackService --------------------------------------------------
	addContentTool(s, "search_datapacks", cl, func(ctx context.Context, cl *ContentClients, in contentQueryArgs) (any, error) {
		for field, value := range map[string]string{
			"query": in.Query, "game_version": in.GameVersion, "indexer": in.Indexer,
		} {
			if err := requireText("search_datapacks", field, value, 64); err != nil {
				return nil, err
			}
		}
		if err := contentOffset("search_datapacks", in.Offset); err != nil {
			return nil, err
		}
		limit, err := limitOr("search_datapacks", in.Limit)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Datapack.SearchDatapacks, v1.SearchDatapacksRequest{
			Query: in.Query, GameVersion: in.GameVersion, Indexer: in.Indexer,
			Offset: in.Offset, Limit: limit,
		})
	})
	addContentTool(s, "list_datapacks", cl, func(ctx context.Context, cl *ContentClients, in contentServerArgs) (any, error) {
		if err := requireID("list_datapacks", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Datapack.ListDatapacks, v1.ListDatapacksRequest{ServerId: in.ServerID})
	})
	addContentTool(s, "install_datapack", cl, func(ctx context.Context, cl *ContentClients, in contentInstallDatapackArgs) (any, error) {
		if err := requireID("install_datapack", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("install_datapack", "datapack_id", in.DatapackID); err != nil {
			return nil, err
		}
		for field, value := range map[string]string{
			"game_version": in.GameVersion, "file_name": in.FileName, "indexer": in.Indexer,
		} {
			if err := requireText("install_datapack", field, value, 128); err != nil {
				return nil, err
			}
		}
		if err := contentFilename("install_datapack", "file_name", in.FileName); err != nil && in.FileName != "" {
			return nil, err
		}
		return invoke(ctx, cl.Datapack.InstallDatapack, v1.InstallDatapackRequest{
			ServerId: in.ServerID, DatapackId: in.DatapackID, GameVersion: in.GameVersion,
			FileName: in.FileName, Indexer: in.Indexer, Stage: in.Stage,
		})
	})
	addContentTool(s, "upload_datapack", cl, func(ctx context.Context, cl *ContentClients, in contentUploadDatapackArgs) (any, error) {
		if err := requireID("upload_datapack", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("upload_datapack", "upload_session_id", in.UploadSessionID); err != nil {
			return nil, err
		}
		if err := contentFilename("upload_datapack", "file_name", in.FileName); err != nil && in.FileName != "" {
			return nil, err
		}
		return invoke(ctx, cl.Datapack.UploadDatapack, v1.UploadDatapackRequest{
			ServerId: in.ServerID, UploadSessionId: in.UploadSessionID,
			FileName: in.FileName, Stage: in.Stage,
		})
	})
	addContentTool(s, "delete_datapack", cl, func(ctx context.Context, cl *ContentClients, in contentDeleteDatapackArgs) (any, error) {
		if err := requireID("delete_datapack", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := contentFilename("delete_datapack", "file_name", in.FileName); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Datapack.DeleteDatapack, v1.DeleteDatapackRequest{
			ServerId: in.ServerID, FileName: in.FileName,
		})
	})

	// --- ModuleService ----------------------------------------------------
	addContentTool(s, "list_module_templates", cl, func(ctx context.Context, cl *ContentClients, in contentModuleTemplateListArgs) (any, error) {
		n, err := contentEnum("list_module_templates", "type", in.Type, v1.ModuleTemplateType_value)
		if err != nil {
			return nil, err
		}
		if err := requireText("list_module_templates", "category", in.Category, 64); err != nil {
			return nil, err
		}
		var templateType *v1.ModuleTemplateType
		if in.Type != "" {
			t := v1.ModuleTemplateType(n)
			templateType = &t
		}
		return invoke(ctx, cl.Module.ListModuleTemplates, v1.ListModuleTemplatesRequest{
			Type: templateType, Category: contentOptionalString(in.Category),
		})
	})
	addContentTool(s, "get_module_template", cl, func(ctx context.Context, cl *ContentClients, in contentIDArgs) (any, error) {
		if err := requireID("get_module_template", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.GetModuleTemplate, v1.GetModuleTemplateRequest{Id: in.ID})
	})
	addContentTool(s, "list_modules", cl, func(ctx context.Context, cl *ContentClients, in contentModuleFilterArgs) (any, error) {
		if err := optionalID("list_modules", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := optionalID("list_modules", "template_id", in.TemplateID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.ListModules, v1.ListModulesRequest{
			ServerId:   contentOptionalString(in.ServerID),
			TemplateId: contentOptionalString(in.TemplateID),
		})
	})
	addContentTool(s, "get_module", cl, func(ctx context.Context, cl *ContentClients, in contentIDArgs) (any, error) {
		if err := requireID("get_module", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.GetModule, v1.GetModuleRequest{Id: in.ID})
	})
	addContentTool(s, "get_module_logs", cl, func(ctx context.Context, cl *ContentClients, in contentModuleLogsArgs) (any, error) {
		if err := requireID("get_module_logs", "id", in.ID); err != nil {
			return nil, err
		}
		tail, err := limitOr("get_module_logs", in.Tail)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.GetModuleLogs, v1.GetModuleLogsRequest{Id: in.ID, Tail: tail})
	})
	addContentTool(s, "get_next_available_module_port", cl, func(ctx context.Context, cl *ContentClients, in contentOptionalServerArgs) (any, error) {
		if err := optionalID("get_next_available_module_port", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.GetNextAvailableModulePort, v1.GetNextAvailableModulePortRequest{
			ServerId: contentOptionalString(in.ServerID),
		})
	})
	addContentTool(s, "get_available_aliases", cl, func(ctx context.Context, cl *ContentClients, in contentAliasArgs) (any, error) {
		if err := optionalID("get_available_aliases", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := optionalID("get_available_aliases", "module_id", in.ModuleID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.GetAvailableAliases, v1.GetAvailableAliasesRequest{
			ServerId: contentOptionalString(in.ServerID),
			ModuleId: contentOptionalString(in.ModuleID),
		})
	})
	addContentTool(s, "get_resolved_aliases", cl, func(ctx context.Context, cl *ContentClients, in contentAliasArgs) (any, error) {
		if err := optionalID("get_resolved_aliases", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := optionalID("get_resolved_aliases", "module_id", in.ModuleID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.GetResolvedAliases, v1.GetResolvedAliasesRequest{
			ServerId: contentOptionalString(in.ServerID),
			ModuleId: contentOptionalString(in.ModuleID),
		})
	})
	addContentTool(s, "create_module_template", cl, func(ctx context.Context, cl *ContentClients, in contentCreateModuleTemplateArgs) (any, error) {
		for field, value := range map[string]string{
			"name": in.Name, "docker_image": in.DockerImage, "category": in.Category,
			"icon": in.Icon, "health_check_path": in.HealthCheckPath,
		} {
			if err := requireText("create_module_template", field, value, 128); err != nil {
				return nil, err
			}
		}
		if err := requireText("create_module_template", "description", in.Description, 256); err != nil {
			return nil, err
		}
		if in.Name == "" || in.DockerImage == "" {
			return nil, contentError("create_module_template", "name/docker_image", "name and docker_image are required")
		}
		if err := contentPort("create_module_template", "health_check_port", in.HealthCheckPort); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.CreateModuleTemplate, v1.CreateModuleTemplateRequest{
			Name: in.Name, Description: in.Description, DockerImage: in.DockerImage,
			Category: in.Category, Icon: in.Icon, HealthCheckPath: in.HealthCheckPath,
			HealthCheckPort: in.HealthCheckPort, RequiresServer: in.RequiresServer, SupportsProxy: in.SupportsProxy,
		})
	})
	addContentTool(s, "update_module_template", cl, func(ctx context.Context, cl *ContentClients, in contentUpdateModuleTemplateArgs) (any, error) {
		if err := requireID("update_module_template", "id", in.ID); err != nil {
			return nil, err
		}
		for field, value := range map[string]string{
			"name": in.Name, "docker_image": in.DockerImage, "category": in.Category,
			"icon": in.Icon, "health_check_path": in.HealthCheckPath,
		} {
			if err := requireText("update_module_template", field, value, 128); err != nil {
				return nil, err
			}
		}
		if err := requireText("update_module_template", "description", in.Description, 256); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.UpdateModuleTemplate, v1.UpdateModuleTemplateRequest{
			Id:   in.ID,
			Name: contentOptionalString(in.Name), Description: contentOptionalString(in.Description),
			DockerImage: contentOptionalString(in.DockerImage), Category: contentOptionalString(in.Category),
			Icon: contentOptionalString(in.Icon), HealthCheckPath: contentOptionalString(in.HealthCheckPath),
		})
	})
	addContentTool(s, "delete_module_template", cl, func(ctx context.Context, cl *ContentClients, in contentIDConfirmArgs) (any, error) {
		if err := requireID("delete_module_template", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.DeleteModuleTemplate, v1.DeleteModuleTemplateRequest{Id: in.ID})
	})
	addContentTool(s, "create_module", cl, func(ctx context.Context, cl *ContentClients, in contentCreateModuleArgs) (any, error) {
		if err := requireID("create_module", "name", in.Name); err != nil {
			return nil, err
		}
		if err := requireID("create_module", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("create_module", "template_id", in.TemplateID); err != nil {
			return nil, err
		}
		if err := contentMemory("create_module", in.Memory); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.CreateModule, v1.CreateModuleRequest{
			Name: in.Name, ServerId: in.ServerID, TemplateId: in.TemplateID,
			Memory: in.Memory, AutoStart: in.AutoStart, StartImmediately: in.StartImmediately,
		})
	})
	addContentTool(s, "update_module", cl, func(ctx context.Context, cl *ContentClients, in contentUpdateModuleArgs) (any, error) {
		if err := requireID("update_module", "id", in.ID); err != nil {
			return nil, err
		}
		if err := requireText("update_module", "name", in.Name, 128); err != nil {
			return nil, err
		}
		if in.Memory != nil {
			if err := contentMemory("update_module", *in.Memory); err != nil {
				return nil, err
			}
		}
		return invoke(ctx, cl.Module.UpdateModule, v1.UpdateModuleRequest{
			Id: in.ID, Stage: in.Stage,
			Name:   contentOptionalString(in.Name),
			Memory: in.Memory, AutoStart: in.AutoStart, Detached: in.Detached,
			FollowServerLifecycle: in.FollowServerLifecycle,
		})
	})
	addContentTool(s, "delete_module", cl, func(ctx context.Context, cl *ContentClients, in contentIDConfirmArgs) (any, error) {
		if err := requireID("delete_module", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.DeleteModule, v1.DeleteModuleRequest{Id: in.ID})
	})
	addContentTool(s, "start_module", cl, func(ctx context.Context, cl *ContentClients, in contentIDConfirmArgs) (any, error) {
		if err := requireID("start_module", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.StartModule, v1.StartModuleRequest{Id: in.ID})
	})
	addContentTool(s, "stop_module", cl, func(ctx context.Context, cl *ContentClients, in contentIDConfirmArgs) (any, error) {
		if err := requireID("stop_module", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.StopModule, v1.StopModuleRequest{Id: in.ID})
	})
	addContentTool(s, "restart_module", cl, func(ctx context.Context, cl *ContentClients, in contentIDConfirmArgs) (any, error) {
		if err := requireID("restart_module", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.RestartModule, v1.RestartModuleRequest{Id: in.ID})
	})
	addContentTool(s, "recreate_module", cl, func(ctx context.Context, cl *ContentClients, in contentIDConfirmArgs) (any, error) {
		if err := requireID("recreate_module", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Module.RecreateModule, v1.RecreateModuleRequest{Id: in.ID})
	})

	// --- FileService bounded read / export --------------------------------
	addContentTool(s, "get_extraction_status", cl, func(ctx context.Context, cl *ContentClients, in contentOperationArgs) (any, error) {
		if err := requireID("get_extraction_status", "operation_id", in.OperationID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.File.GetExtractionStatus, v1.GetExtractionStatusRequest{OperationId: in.OperationID})
	})
	addContentTool(s, "get_remote_archive_progress", cl, func(ctx context.Context, cl *ContentClients, in contentServerTaskArgs) (any, error) {
		if err := requireID("get_remote_archive_progress", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("get_remote_archive_progress", "task_id", in.TaskID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.File.GetRemoteArchiveProgress, v1.GetRemoteArchiveProgressRequest{
			ServerId: in.ServerID, TaskId: in.TaskID,
		})
	})
	addContentTool(s, "init_file_download", cl, func(ctx context.Context, cl *ContentClients, in contentServerPathArgs) (any, error) {
		if err := requireID("init_file_download", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := validatePath("init_file_download", in.Path, true); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.File.InitFileDownload, v1.InitFileDownloadRequest{
			ServerId: in.ServerID, Path: in.Path,
		})
	})
	addContentTool(s, "download_archive", cl, func(ctx context.Context, cl *ContentClients, in contentServerPathsArgs) (any, error) {
		if err := requireID("download_archive", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := contentBoundList("download_archive", "paths", len(in.Paths), contentMaxPaths); err != nil {
			return nil, err
		}
		if len(in.Paths) == 0 {
			return nil, contentError("download_archive", "paths", "at least one path is required")
		}
		for i, p := range in.Paths {
			if err := validatePath("download_archive", p, true); err != nil {
				return nil, contentError("download_archive", fmt.Sprintf("paths[%d]", i), err.Error())
			}
		}
		return invoke(ctx, cl.File.DownloadArchive, v1.DownloadArchiveRequest{
			ServerId: in.ServerID, Paths: in.Paths,
		})
	})
	addContentTool(s, "export_world", cl, func(ctx context.Context, cl *ContentClients, in contentServerArgs) (any, error) {
		if err := requireID("export_world", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.File.ExportWorld, v1.ExportWorldRequest{ServerId: in.ServerID})
	})

	// --- Full coverage: remaining generated RPCs --------------------------
	addContentTool(s, "get_modpack_by_url", cl, func(ctx context.Context, cl *ContentClients, in contentURLArgs) (any, error) {
		if err := contentHTTPSURL("get_modpack_by_url", "url", in.URL); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Modpack.GetModpackByURL, v1.GetModpackByURLRequest{Url: in.URL})
	})
	addContentTool(s, "import_remote_modpack", cl, func(ctx context.Context, cl *ContentClients, in contentImportRemoteModpackArgs) (any, error) {
		if err := contentHTTPSURL("import_remote_modpack", "url", in.URL); err != nil {
			return nil, err
		}
		for field, value := range map[string]string{
			"name": in.Name, "description": in.Description, "mc_version": in.MCVersion, "mod_loader": in.ModLoader,
		} {
			max := 128
			switch field {
			case "description":
				max = 256
			case "mc_version", "mod_loader":
				max = 32
			}
			if err := requireText("import_remote_modpack", field, value, max); err != nil {
				return nil, err
			}
		}
		// auth_token and allow_private_network are intentionally never set:
		// MCP arguments must not carry provider credentials or private-network
		// overrides (modpack.proto:176-177).
		return invoke(ctx, cl.Modpack.ImportRemoteModpack, v1.ImportRemoteModpackRequest{
			Url: in.URL, Name: in.Name, Description: in.Description,
			McVersion: in.MCVersion, ModLoader: in.ModLoader,
		})
	})

	addContentTool(s, "update_file", cl, func(ctx context.Context, cl *ContentClients, in contentFileWriteArgs) (any, error) {
		if err := requireID("update_file", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := validatePath("update_file", in.Path, true); err != nil {
			return nil, err
		}
		content, err := contentDecodeBytes("update_file", in.Content, maxFileBytes)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.File.UpdateFile, v1.UpdateFileRequest{
			ServerId: in.ServerID, Path: in.Path, Content: content, Stage: in.Stage,
		})
	})
	addContentTool(s, "delete_file", cl, func(ctx context.Context, cl *ContentClients, in contentFileDeleteArgs) (any, error) {
		if err := requireID("delete_file", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := validatePath("delete_file", in.Path, true); err != nil {
			return nil, err
		}
		// Only the single-path form is exposed; bulk paths are not accepted.
		return invoke(ctx, cl.File.DeleteFile, v1.DeleteFileRequest{
			ServerId: in.ServerID, Path: in.Path, Stage: in.Stage,
		})
	})
	addContentTool(s, "rename_file", cl, func(ctx context.Context, cl *ContentClients, in contentFileRenameArgs) (any, error) {
		if err := requireID("rename_file", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := validatePath("rename_file", in.Path, true); err != nil {
			return nil, err
		}
		if err := contentFilename("rename_file", "new_name", in.NewName); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.File.RenameFile, v1.RenameFileRequest{
			ServerId: in.ServerID, Path: in.Path, NewName: in.NewName,
		})
	})
	addContentTool(s, "extract_archive", cl, func(ctx context.Context, cl *ContentClients, in contentFilePathArgs) (any, error) {
		if err := requireID("extract_archive", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := validatePath("extract_archive", in.Path, true); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.File.ExtractArchive, v1.ExtractArchiveRequest{
			ServerId: in.ServerID, Path: in.Path,
		})
	})
	addContentTool(s, "create_folder", cl, func(ctx context.Context, cl *ContentClients, in contentFilePathArgs) (any, error) {
		if err := requireID("create_folder", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := validatePath("create_folder", in.Path, true); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.File.CreateFolder, v1.CreateFolderRequest{
			ServerId: in.ServerID, Path: in.Path,
		})
	})
	addContentTool(s, "move_file", cl, func(ctx context.Context, cl *ContentClients, in contentFileMoveArgs) (any, error) {
		if err := requireID("move_file", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		for field, value := range map[string]string{"source_path": in.SourcePath, "destination_path": in.DestinationPath} {
			if err := validatePath("move_file", value, true); err != nil {
				return nil, contentError("move_file", field, err.Error())
			}
		}
		return invoke(ctx, cl.File.MoveFile, v1.MoveFileRequest{
			ServerId: in.ServerID, SourcePath: in.SourcePath, DestinationPath: in.DestinationPath,
		})
	})
	addContentTool(s, "copy_file", cl, func(ctx context.Context, cl *ContentClients, in contentFileMoveArgs) (any, error) {
		if err := requireID("copy_file", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		for field, value := range map[string]string{"source_path": in.SourcePath, "destination_path": in.DestinationPath} {
			if err := validatePath("copy_file", value, true); err != nil {
				return nil, contentError("copy_file", field, err.Error())
			}
		}
		return invoke(ctx, cl.File.CopyFile, v1.CopyFileRequest{
			ServerId: in.ServerID, SourcePath: in.SourcePath, DestinationPath: in.DestinationPath,
		})
	})
	addContentTool(s, "create_archive", cl, func(ctx context.Context, cl *ContentClients, in contentCreateArchiveArgs) (any, error) {
		if err := requireID("create_archive", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := contentBoundList("create_archive", "paths", len(in.Paths), contentMaxPaths); err != nil {
			return nil, err
		}
		for i, p := range in.Paths {
			if err := validatePath("create_archive", p, true); err != nil {
				return nil, contentError("create_archive", fmt.Sprintf("paths[%d]", i), err.Error())
			}
		}
		if err := validatePath("create_archive", in.DestinationPath, false); err != nil {
			return nil, err
		}
		if err := contentFilename("create_archive", "archive_name", in.ArchiveName); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.File.CreateArchive, v1.CreateArchiveRequest{
			ServerId: in.ServerID, Paths: in.Paths, DestinationPath: in.DestinationPath,
			ArchiveName: in.ArchiveName,
		})
	})
	addContentTool(s, "download_remote_archive", cl, func(ctx context.Context, cl *ContentClients, in contentRemoteArchiveArgs) (any, error) {
		if err := requireID("download_remote_archive", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := contentHTTPSURL("download_remote_archive", "url", in.URL); err != nil {
			return nil, err
		}
		if err := validatePath("download_remote_archive", in.DestinationPath, true); err != nil {
			return nil, err
		}
		if err := contentSHA256("download_remote_archive", in.SHA256Checksum); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.File.DownloadRemoteArchive, v1.DownloadRemoteArchiveRequest{
			ServerId: in.ServerID, Url: in.URL, DestinationPath: in.DestinationPath,
			Sha256Checksum: in.SHA256Checksum, AutoExtract: in.AutoExtract,
		})
	})
	addContentTool(s, "save_uploaded_file", cl, func(ctx context.Context, cl *ContentClients, in contentSaveUploadedFileArgs) (any, error) {
		if err := requireID("save_uploaded_file", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("save_uploaded_file", "upload_session_id", in.UploadSessionID); err != nil {
			return nil, err
		}
		if err := validatePath("save_uploaded_file", in.DestinationPath, false); err != nil {
			return nil, err
		}
		if err := contentFilename("save_uploaded_file", "filename", in.Filename); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.File.SaveUploadedFile, v1.SaveUploadedFileRequest{
			ServerId: in.ServerID, UploadSessionId: in.UploadSessionID,
			DestinationPath: in.DestinationPath, Filename: in.Filename,
		})
	})

	// --- Scoped streaming upload workflow ---------------------------------
	addContentTool(s, "init_upload", cl, func(ctx context.Context, cl *ContentClients, in contentInitUploadArgs) (any, error) {
		if err := contentFilename("init_upload", "filename", in.Filename); err != nil {
			return nil, err
		}
		if in.TotalSize < 1 || in.TotalSize > contentMaxUploadBytes {
			return nil, contentError("init_upload", "total_size", fmt.Sprintf("must be between 1 and %d bytes (4 GiB)", contentMaxUploadBytes))
		}
		if in.ChunkSize < 1 || in.ChunkSize > contentChunkBytes {
			return nil, contentError("init_upload", "chunk_size", fmt.Sprintf("must be between 1 and %d bytes (256 KiB)", contentChunkBytes))
		}
		return invoke(ctx, cl.Upload.InitUpload, v1.InitUploadRequest{
			Filename: in.Filename, TotalSize: in.TotalSize, ChunkSize: in.ChunkSize,
		})
	})
	addContentTool(s, "get_upload_status", cl, func(ctx context.Context, cl *ContentClients, in contentSessionArgs) (any, error) {
		if err := requireID("get_upload_status", "session_id", in.SessionID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Upload.GetUploadStatus, v1.GetUploadStatusRequest{SessionId: in.SessionID})
	})
	addContentTool(s, "upload_chunk", cl, func(ctx context.Context, cl *ContentClients, in contentUploadChunkArgs) (any, error) {
		if err := requireID("upload_chunk", "session_id", in.SessionID); err != nil {
			return nil, err
		}
		if in.ChunkIndex < 0 || in.ChunkIndex > contentMaxChunkIndex {
			return nil, contentError("upload_chunk", "chunk_index", fmt.Sprintf("must be between 0 and %d", contentMaxChunkIndex))
		}
		data, err := contentDecodeBytes("upload_chunk", in.Data, contentChunkBytes)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Upload.UploadChunk, v1.UploadChunkRequest{
			SessionId: in.SessionID, ChunkIndex: in.ChunkIndex, Data: data,
		})
	})
	addContentTool(s, "cancel_upload", cl, func(ctx context.Context, cl *ContentClients, in contentCancelUploadArgs) (any, error) {
		if err := requireID("cancel_upload", "session_id", in.SessionID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Upload.CancelUpload, v1.CancelUploadRequest{SessionId: in.SessionID})
	})
}

// contentDecodeBytes decodes a base64 argument and enforces a hard decoded
// cap, so an MCP caller cannot smuggle an unbounded byte stream through a
// string field.
func contentDecodeBytes(tool, value string, max int) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, contentError(tool, "data", "must be valid base64")
	}
	if len(decoded) > max {
		return nil, contentError(tool, "data", fmt.Sprintf("must decode to at most %d bytes", max))
	}
	return decoded, nil
}

func contentSHA256(tool, value string) error {
	if len(value) != 64 {
		return contentError(tool, "sha256_checksum", "must be exactly 64 hex characters")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return contentError(tool, "sha256_checksum", "must be exactly 64 hex characters")
	}
	return nil
}

func contentMemory(tool string, v int32) error {
	if v < 0 || v > contentMaxMemoryMB {
		return contentError(tool, "memory", fmt.Sprintf("must be between 0 and %d MB", contentMaxMemoryMB))
	}
	return nil
}
