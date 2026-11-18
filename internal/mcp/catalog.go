// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp implements the Carbon Panel MCP server: a stdio MCP server whose
// tools call generated Connect clients with an API-token bearer credential.
//
// It is deliberately read-first. The tool catalog is stable across panel
// builds: tools are never hidden when a controller is older. Instead each tool
// declares the minimum panel build that contains its RPC, and an Unimplemented
// or NotFound response is surfaced as a structured capability error.
package mcp

// FirstSliceToolCount is the number of tools implemented in this first slice.
// Inventory coverage is not implemented API coverage: the remaining supported
// families are tracked separately (OSS-370).
const FirstSliceToolCount = 30

// Class describes the first-slice exposure decision for a declared procedure.
type Class string

const (
	// ClassSafe is a bounded, ACL-scoped read exposed as a tool.
	ClassSafe Class = "safe"
	// ClassReview is an exposed tool that requires an explicit confirmation
	// argument (or a deliberate opt-in) before it performs a mutation.
	ClassReview Class = "review"
	// ClassExclude is deliberately not exposed as a tool in this slice. The
	// declaring entry still participates in the coverage drift gate.
	ClassExclude Class = "exclude"
)

// PanelBuild is the minimum controller build that contains a tool's RPC.
// These are documentation and error-shape qualifiers only: the MCP server
// never auto-detects, auto-switches or defaults to a panel channel.
type PanelBuild string

const (
	// PanelBuildStableV140 is the latest stable panel release (v1.0.40).
	PanelBuildStableV140 PanelBuild = "v1.0.40"
	// PanelBuildPostStable is the pinned post-v1.0.40 build that first
	// contains PlayerService, WorldService, WorldViewService and
	// ModBrowseService.
	PanelBuildPostStable PanelBuild = "post-v1.0.40+fa23a8d73c181f9907633024d9023aead928324e"
)

// CatalogEntry is one declared tool tuple. Scope is a canonical string of the
// form "public", "authenticated", "resource/action" or
// "resource/action[object_id_field]". The coverage drift gate compares it to
// the live internal/rbac mapping.
type CatalogEntry struct {
	Procedure string
	Service   string
	Method    string
	Client    string
	Scope     string
	Class     Class
	Build     PanelBuild
	Tool      string
	Confirm   bool
}

// ToolSpec describes a tool that is actually registered with the MCP server.
type ToolSpec struct {
	Name        string
	Procedure   string
	Class       Class
	Confirm     bool
	Description string
}

// MountedServices mirrors the 25 mux.Handle registrations in
// internal/rpc/server.go registerServices. It is the authoritative shared
// list used by the coverage drift gate; gRPC reflection (19 services) and the
// RBAC parity test (24 services) are both stale and are never used here.
var MountedServices = []string{
	"AuthService", "ConfigService", "FileService", "MinecraftService", "ModService",
	"ModBrowseService", "ModpackService", "DatapackService", "ProxyService", "ServerService",
	"NodeService", "SupportService", "TaskService", "ActivityService", "OperationService",
	"SubuserService", "BlueprintService", "BackupService", "UserService", "RoleService",
	"ModuleService", "UploadService", "PlayerService", "WorldViewService", "WorldService",
}

// Exposed tools. The catalog is intentionally identical on every panel build;
// capability qualification lives in CatalogEntry.Build and is reported only
// when the controller returns Unimplemented/NotFound.
var exposedTools = []ToolSpec{
	// Server lifecycle.
	{Name: "list_servers", Procedure: "/carbonpanel.v1.ServerService/ListServers", Class: ClassSafe, Description: "List Minecraft servers visible to this token. Read-only."},
	{Name: "get_server", Procedure: "/carbonpanel.v1.ServerService/GetServer", Class: ClassSafe, Description: "Get status and details for one server by exact id. Read-only."},
	{Name: "start_server", Procedure: "/carbonpanel.v1.ServerService/StartServer", Class: ClassReview, Confirm: true, Description: "Start one server. Requires confirm=true and an exact server id."},
	{Name: "stop_server", Procedure: "/carbonpanel.v1.ServerService/StopServer", Class: ClassReview, Confirm: true, Description: "Stop one server. Requires confirm=true and an exact server id."},
	{Name: "restart_server", Procedure: "/carbonpanel.v1.ServerService/RestartServer", Class: ClassReview, Confirm: true, Description: "Restart one server. Requires confirm=true and an exact server id."},

	// Player lookup. Requires a post-v1.0.40 panel build.
	{Name: "list_players", Procedure: "/carbonpanel.v1.PlayerService/ListPlayers", Class: ClassSafe, Description: "List players on one server. Requires a concrete server_id and a bounded limit."},
	{Name: "get_player", Procedure: "/carbonpanel.v1.PlayerService/GetPlayer", Class: ClassSafe, Description: "Get one player's detail on one server."},
	{Name: "list_player_capabilities", Procedure: "/carbonpanel.v1.PlayerService/ListPlayerCapabilities", Class: ClassSafe, Description: "List player capability status on one server."},
	{Name: "get_player_skin", Procedure: "/carbonpanel.v1.PlayerService/GetPlayerSkin", Class: ClassSafe, Description: "Load a skin by opaque skin_ref cache key; skin_ref is never a URL."},

	// World library and bindings. Requires a post-v1.0.40 panel build.
	{Name: "list_world_entries", Procedure: "/carbonpanel.v1.WorldService/ListWorldEntries", Class: ClassSafe, Description: "Browse world library metadata with a bounded limit."},
	{Name: "get_world_entry", Procedure: "/carbonpanel.v1.WorldService/GetWorldEntry", Class: ClassSafe, Description: "Get one world library entry by id."},
	{Name: "list_world_revisions", Procedure: "/carbonpanel.v1.WorldService/ListRevisions", Class: ClassSafe, Description: "List immutable revisions for one world entry."},
	{Name: "get_world_revision", Procedure: "/carbonpanel.v1.WorldService/GetRevision", Class: ClassSafe, Description: "Get one immutable world revision."},
	{Name: "list_world_bindings", Procedure: "/carbonpanel.v1.WorldService/ListBindings", Class: ClassSafe, Description: "List active world bindings for one server."},
	{Name: "get_world_binding", Procedure: "/carbonpanel.v1.WorldService/GetBinding", Class: ClassSafe, Description: "Get the active world binding for a server level."},
	{Name: "list_import_jobs", Procedure: "/carbonpanel.v1.WorldService/ListImportJobs", Class: ClassSafe, Description: "List recent world import jobs with a bounded limit."},
	{Name: "get_import_job", Procedure: "/carbonpanel.v1.WorldService/GetImportJob", Class: ClassSafe, Description: "Get one world import job."},
	{Name: "check_world_compatibility", Procedure: "/carbonpanel.v1.WorldService/CheckCompatibility", Class: ClassSafe, Description: "Read-only compatibility and dry-run report for one revision."},
	{Name: "get_world_config_schema", Procedure: "/carbonpanel.v1.WorldService/GetWorldConfigSchema", Class: ClassSafe, Description: "Get the version-driven world configuration schema."},
	{Name: "preview_world_config", Procedure: "/carbonpanel.v1.WorldService/PreviewWorldConfig", Class: ClassSafe, Description: "Validate draft world configuration without applying it."},
	{Name: "search_world_artifacts", Procedure: "/carbonpanel.v1.WorldService/SearchWorldArtifacts", Class: ClassSafe, Description: "Search provider artifacts with bounded query and limit."},
	{Name: "get_provider_capabilities", Procedure: "/carbonpanel.v1.WorldService/GetProviderCapabilities", Class: ClassSafe, Description: "Report world provider capability and discovery gates."},

	// World view (observation only). observe_players stays excluded.
	{Name: "list_world_view_targets", Procedure: "/carbonpanel.v1.WorldViewService/ListWorldViewTargets", Class: ClassSafe, Description: "List world-view targets visible to this token. No renderer URLs."},
	{Name: "get_world_view_status", Procedure: "/carbonpanel.v1.WorldViewService/GetWorldViewStatus", Class: ClassSafe, Description: "Get pollable status for one server-local world."},
	{Name: "list_world_view_capabilities", Procedure: "/carbonpanel.v1.WorldViewService/ListCapabilities", Class: ClassSafe, Description: "List verified renderer capabilities for one server."},
	{Name: "list_map_overlays", Procedure: "/carbonpanel.v1.WorldViewService/ListMapOverlays", Class: ClassSafe, Description: "List visible map overlays for one world and dimension."},

	// Backup list and restore.
	{Name: "list_backups", Procedure: "/carbonpanel.v1.BackupService/ListBackups", Class: ClassSafe, Description: "List backups for one server with a bounded limit."},
	{Name: "restore_backup", Procedure: "/carbonpanel.v1.BackupService/RestoreBackup", Class: ClassReview, Confirm: true, Description: "Restore one backup. Destructive: requires confirm=true and an exact backup id."},

	// File browse/read. No write tools in this slice.
	{Name: "list_files", Procedure: "/carbonpanel.v1.FileService/ListFiles", Class: ClassSafe, Description: "Browse a server-relative directory. Read-only."},
	{Name: "get_file", Procedure: "/carbonpanel.v1.FileService/GetFile", Class: ClassSafe, Description: "Read a bounded server-relative file. Read-only; content is truncated for safety."},
}

// SmokeTools is the verified minimal smoke path that also works against
// stable v1.0.40. Every entry is read-only and maps to a pre-v1.0.40 RPC.
var SmokeTools = []string{"list_servers", "get_server", "list_files", "get_file", "list_backups"}

// Catalog maps procedure -> declared entry, populated from the generated
// descriptor table plus the explicit exposure declarations above.
var Catalog map[string]CatalogEntry

// Tools is the stable, ordered list of registered tool declarations.
var Tools = exposedTools

func init() {
	Catalog = make(map[string]CatalogEntry, len(rawCatalogEntries))
	exposed := make(map[string]ToolSpec, len(exposedTools))
	for _, spec := range exposedTools {
		exposed[spec.Procedure] = spec
	}
	for _, e := range rawCatalogEntries {
		if spec, ok := exposed[e.Procedure]; ok {
			e.Tool = spec.Name
			e.Class = spec.Class
			e.Confirm = spec.Confirm
		}
		Catalog[e.Procedure] = e
	}
}

// DeclaredProcedure returns the declared entry for a Connect procedure.
func DeclaredProcedure(procedure string) (CatalogEntry, bool) {
	e, ok := Catalog[procedure]
	return e, ok
}

// Declared returns the declared entry for a registered tool name.
func Declared(tool string) (CatalogEntry, bool) {
	for _, spec := range exposedTools {
		if spec.Name == tool {
			e, ok := Catalog[spec.Procedure]
			return e, ok
		}
	}
	return CatalogEntry{}, false
}

// IsSmokeTool reports whether a tool belongs to the stable-build smoke path.
func IsSmokeTool(tool string) bool {
	for _, name := range SmokeTools {
		if name == tool {
			return true
		}
	}
	return false
}
