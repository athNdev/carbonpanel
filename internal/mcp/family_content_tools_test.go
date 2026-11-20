// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var updateContentDescriptors = flag.Bool("update-content-descriptors", false, "regenerate family_content_descriptors.json")

// --- disposable fixture ---------------------------------------------------

type contentFixture struct {
	sessions map[string]*sdkmcp.ClientSession
	tokens   map[string]string
	server   *httptest.Server
}

func registerContentOK[Req, Res any](mux *http.ServeMux, procedure string, ic connect.UnaryInterceptorFunc, olderMode string) {
	var handler func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error)
	postStable := false
	if e, ok := DeclaredProcedure(procedure); ok {
		postStable = e.Build == PanelBuildPostStable
	}
	if olderMode != "" && postStable {
		if olderMode == "transport-notfound" {
			handler = func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error) {
				return nil, connect.NewError(connect.CodeNotFound, errors.New("unknown method for service: procedure is not registered"))
			}
		} else {
			handler = func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error) {
				return nil, connect.NewError(connect.CodeUnimplemented, errors.New("rpc is not implemented by this panel build"))
			}
		}
	} else {
		handler = func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error) {
			var zero Res
			return connect.NewResponse(&zero), nil
		}
	}
	mux.Handle(procedure, connect.NewUnaryHandler(procedure, handler, connect.WithInterceptors(ic)))
}

func registerAllContentHandlers(mux *http.ServeMux, ic connect.UnaryInterceptorFunc, olderMode string) {
	// ModService.
	registerContentOK[v1.ListModsRequest, v1.ListModsResponse](mux, "/carbonpanel.v1.ModService/ListMods", ic, olderMode)
	registerContentOK[v1.GetModRequest, v1.GetModResponse](mux, "/carbonpanel.v1.ModService/GetMod", ic, olderMode)
	registerContentOK[v1.GetFabricOptimizationStackRequest, v1.GetFabricOptimizationStackResponse](mux, "/carbonpanel.v1.ModService/GetFabricOptimizationStack", ic, olderMode)
	registerContentOK[v1.ImportUploadedModRequest, v1.ImportUploadedModResponse](mux, "/carbonpanel.v1.ModService/ImportUploadedMod", ic, olderMode)
	registerContentOK[v1.UpdateModRequest, v1.UpdateModResponse](mux, "/carbonpanel.v1.ModService/UpdateMod", ic, olderMode)
	registerContentOK[v1.DeleteModRequest, v1.DeleteModResponse](mux, "/carbonpanel.v1.ModService/DeleteMod", ic, olderMode)
	registerContentOK[v1.InstallFabricOptimizationStackRequest, v1.InstallFabricOptimizationStackResponse](mux, "/carbonpanel.v1.ModService/InstallFabricOptimizationStack", ic, olderMode)

	// ModBrowseService.
	registerContentOK[v1.SearchModsRequest, v1.SearchModsResponse](mux, "/carbonpanel.v1.ModBrowseService/SearchMods", ic, olderMode)
	registerContentOK[v1.GetModVersionsRequest, v1.GetModVersionsResponse](mux, "/carbonpanel.v1.ModBrowseService/GetModVersions", ic, olderMode)
	registerContentOK[v1.InstallModsRequest, v1.InstallModsResponse](mux, "/carbonpanel.v1.ModBrowseService/InstallMods", ic, olderMode)

	// ModpackService.
	registerContentOK[v1.SearchModpacksRequest, v1.SearchModpacksResponse](mux, "/carbonpanel.v1.ModpackService/SearchModpacks", ic, olderMode)
	registerContentOK[v1.GetModpackRequest, v1.GetModpackResponse](mux, "/carbonpanel.v1.ModpackService/GetModpack", ic, olderMode)
	registerContentOK[v1.GetModpackBySlugRequest, v1.GetModpackBySlugResponse](mux, "/carbonpanel.v1.ModpackService/GetModpackBySlug", ic, olderMode)
	registerContentOK[v1.GetModpackByURLRequest, v1.GetModpackByURLResponse](mux, "/carbonpanel.v1.ModpackService/GetModpackByURL", ic, olderMode)
	registerContentOK[v1.SyncModpacksRequest, v1.SyncModpacksResponse](mux, "/carbonpanel.v1.ModpackService/SyncModpacks", ic, olderMode)
	registerContentOK[v1.ImportUploadedModpackRequest, v1.ImportUploadedModpackResponse](mux, "/carbonpanel.v1.ModpackService/ImportUploadedModpack", ic, olderMode)
	registerContentOK[v1.ImportRemoteModpackRequest, v1.ImportRemoteModpackResponse](mux, "/carbonpanel.v1.ModpackService/ImportRemoteModpack", ic, olderMode)
	registerContentOK[v1.DeleteModpackRequest, v1.DeleteModpackResponse](mux, "/carbonpanel.v1.ModpackService/DeleteModpack", ic, olderMode)
	registerContentOK[v1.ToggleFavoriteRequest, v1.ToggleFavoriteResponse](mux, "/carbonpanel.v1.ModpackService/ToggleFavorite", ic, olderMode)
	registerContentOK[v1.ListFavoritesRequest, v1.ListFavoritesResponse](mux, "/carbonpanel.v1.ModpackService/ListFavorites", ic, olderMode)
	registerContentOK[v1.GetIndexerStatusRequest, v1.GetIndexerStatusResponse](mux, "/carbonpanel.v1.ModpackService/GetIndexerStatus", ic, olderMode)
	registerContentOK[v1.GetModpackConfigRequest, v1.GetModpackConfigResponse](mux, "/carbonpanel.v1.ModpackService/GetModpackConfig", ic, olderMode)
	registerContentOK[v1.GetModpackFilesRequest, v1.GetModpackFilesResponse](mux, "/carbonpanel.v1.ModpackService/GetModpackFiles", ic, olderMode)
	registerContentOK[v1.GetModpackVersionsRequest, v1.GetModpackVersionsResponse](mux, "/carbonpanel.v1.ModpackService/GetModpackVersions", ic, olderMode)
	registerContentOK[v1.SyncModpackFilesRequest, v1.SyncModpackFilesResponse](mux, "/carbonpanel.v1.ModpackService/SyncModpackFiles", ic, olderMode)

	// DatapackService.
	registerContentOK[v1.SearchDatapacksRequest, v1.SearchDatapacksResponse](mux, "/carbonpanel.v1.DatapackService/SearchDatapacks", ic, olderMode)
	registerContentOK[v1.InstallDatapackRequest, v1.InstallDatapackResponse](mux, "/carbonpanel.v1.DatapackService/InstallDatapack", ic, olderMode)
	registerContentOK[v1.UploadDatapackRequest, v1.UploadDatapackResponse](mux, "/carbonpanel.v1.DatapackService/UploadDatapack", ic, olderMode)
	registerContentOK[v1.ListDatapacksRequest, v1.ListDatapacksResponse](mux, "/carbonpanel.v1.DatapackService/ListDatapacks", ic, olderMode)
	registerContentOK[v1.DeleteDatapackRequest, v1.DeleteDatapackResponse](mux, "/carbonpanel.v1.DatapackService/DeleteDatapack", ic, olderMode)

	// ModuleService.
	registerContentOK[v1.ListModuleTemplatesRequest, v1.ListModuleTemplatesResponse](mux, "/carbonpanel.v1.ModuleService/ListModuleTemplates", ic, olderMode)
	registerContentOK[v1.GetModuleTemplateRequest, v1.GetModuleTemplateResponse](mux, "/carbonpanel.v1.ModuleService/GetModuleTemplate", ic, olderMode)
	registerContentOK[v1.CreateModuleTemplateRequest, v1.CreateModuleTemplateResponse](mux, "/carbonpanel.v1.ModuleService/CreateModuleTemplate", ic, olderMode)
	registerContentOK[v1.UpdateModuleTemplateRequest, v1.UpdateModuleTemplateResponse](mux, "/carbonpanel.v1.ModuleService/UpdateModuleTemplate", ic, olderMode)
	registerContentOK[v1.DeleteModuleTemplateRequest, v1.DeleteModuleTemplateResponse](mux, "/carbonpanel.v1.ModuleService/DeleteModuleTemplate", ic, olderMode)
	registerContentOK[v1.ListModulesRequest, v1.ListModulesResponse](mux, "/carbonpanel.v1.ModuleService/ListModules", ic, olderMode)
	registerContentOK[v1.GetModuleRequest, v1.GetModuleResponse](mux, "/carbonpanel.v1.ModuleService/GetModule", ic, olderMode)
	registerContentOK[v1.CreateModuleRequest, v1.CreateModuleResponse](mux, "/carbonpanel.v1.ModuleService/CreateModule", ic, olderMode)
	registerContentOK[v1.UpdateModuleRequest, v1.UpdateModuleResponse](mux, "/carbonpanel.v1.ModuleService/UpdateModule", ic, olderMode)
	registerContentOK[v1.DeleteModuleRequest, v1.DeleteModuleResponse](mux, "/carbonpanel.v1.ModuleService/DeleteModule", ic, olderMode)
	registerContentOK[v1.StartModuleRequest, v1.StartModuleResponse](mux, "/carbonpanel.v1.ModuleService/StartModule", ic, olderMode)
	registerContentOK[v1.StopModuleRequest, v1.StopModuleResponse](mux, "/carbonpanel.v1.ModuleService/StopModule", ic, olderMode)
	registerContentOK[v1.RestartModuleRequest, v1.RestartModuleResponse](mux, "/carbonpanel.v1.ModuleService/RestartModule", ic, olderMode)
	registerContentOK[v1.RecreateModuleRequest, v1.RecreateModuleResponse](mux, "/carbonpanel.v1.ModuleService/RecreateModule", ic, olderMode)
	registerContentOK[v1.GetModuleLogsRequest, v1.GetModuleLogsResponse](mux, "/carbonpanel.v1.ModuleService/GetModuleLogs", ic, olderMode)
	registerContentOK[v1.GetNextAvailableModulePortRequest, v1.GetNextAvailableModulePortResponse](mux, "/carbonpanel.v1.ModuleService/GetNextAvailableModulePort", ic, olderMode)
	registerContentOK[v1.GetAvailableAliasesRequest, v1.GetAvailableAliasesResponse](mux, "/carbonpanel.v1.ModuleService/GetAvailableAliases", ic, olderMode)
	registerContentOK[v1.GetResolvedAliasesRequest, v1.GetResolvedAliasesResponse](mux, "/carbonpanel.v1.ModuleService/GetResolvedAliases", ic, olderMode)

	// FileService (all 17; the two first-slice reads are also mounted so the
	// typed client can be exercised here, though their tools live in PR522).
	registerContentOK[v1.ListFilesRequest, v1.ListFilesResponse](mux, "/carbonpanel.v1.FileService/ListFiles", ic, olderMode)
	registerContentOK[v1.GetFileRequest, v1.GetFileResponse](mux, "/carbonpanel.v1.FileService/GetFile", ic, olderMode)
	registerContentOK[v1.GetExtractionStatusRequest, v1.GetExtractionStatusResponse](mux, "/carbonpanel.v1.FileService/GetExtractionStatus", ic, olderMode)
	registerContentOK[v1.GetRemoteArchiveProgressRequest, v1.GetRemoteArchiveProgressResponse](mux, "/carbonpanel.v1.FileService/GetRemoteArchiveProgress", ic, olderMode)
	registerContentOK[v1.InitFileDownloadRequest, v1.InitFileDownloadResponse](mux, "/carbonpanel.v1.FileService/InitFileDownload", ic, olderMode)
	registerContentOK[v1.DownloadArchiveRequest, v1.DownloadArchiveResponse](mux, "/carbonpanel.v1.FileService/DownloadArchive", ic, olderMode)
	registerContentOK[v1.ExportWorldRequest, v1.ExportWorldResponse](mux, "/carbonpanel.v1.FileService/ExportWorld", ic, olderMode)
	registerContentOK[v1.UpdateFileRequest, v1.UpdateFileResponse](mux, "/carbonpanel.v1.FileService/UpdateFile", ic, olderMode)
	registerContentOK[v1.DeleteFileRequest, v1.DeleteFileResponse](mux, "/carbonpanel.v1.FileService/DeleteFile", ic, olderMode)
	registerContentOK[v1.RenameFileRequest, v1.RenameFileResponse](mux, "/carbonpanel.v1.FileService/RenameFile", ic, olderMode)
	registerContentOK[v1.ExtractArchiveRequest, v1.ExtractArchiveResponse](mux, "/carbonpanel.v1.FileService/ExtractArchive", ic, olderMode)
	registerContentOK[v1.CreateFolderRequest, v1.CreateFolderResponse](mux, "/carbonpanel.v1.FileService/CreateFolder", ic, olderMode)
	registerContentOK[v1.MoveFileRequest, v1.MoveFileResponse](mux, "/carbonpanel.v1.FileService/MoveFile", ic, olderMode)
	registerContentOK[v1.CopyFileRequest, v1.CopyFileResponse](mux, "/carbonpanel.v1.FileService/CopyFile", ic, olderMode)
	registerContentOK[v1.CreateArchiveRequest, v1.CreateArchiveResponse](mux, "/carbonpanel.v1.FileService/CreateArchive", ic, olderMode)
	registerContentOK[v1.DownloadRemoteArchiveRequest, v1.DownloadRemoteArchiveResponse](mux, "/carbonpanel.v1.FileService/DownloadRemoteArchive", ic, olderMode)
	registerContentOK[v1.SaveUploadedFileRequest, v1.SaveUploadedFileResponse](mux, "/carbonpanel.v1.FileService/SaveUploadedFile", ic, olderMode)

	// UploadService.
	registerContentOK[v1.InitUploadRequest, v1.InitUploadResponse](mux, "/carbonpanel.v1.UploadService/InitUpload", ic, olderMode)
	registerContentOK[v1.GetUploadStatusRequest, v1.GetUploadStatusResponse](mux, "/carbonpanel.v1.UploadService/GetUploadStatus", ic, olderMode)
	registerContentOK[v1.UploadChunkRequest, v1.UploadChunkResponse](mux, "/carbonpanel.v1.UploadService/UploadChunk", ic, olderMode)
	registerContentOK[v1.CancelUploadRequest, v1.CancelUploadResponse](mux, "/carbonpanel.v1.UploadService/CancelUpload", ic, olderMode)
}

func newContentFixture(t *testing.T, olderMode string, authEnabled bool) *contentFixture {
	t.Helper()
	ctx := context.Background()
	store := newTestStore(t)
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatalf("SeedDefaultPolicies: %v", err)
	}
	authManager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
		Local:           config.LocalConfig{Enabled: true},
		OIDC:            config.OIDCConfig{Enabled: false},
		AnonymousAccess: false,
		AllowNoAuth:     false,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	interceptor := rpc.NewAuthInterceptor(authManager, enforcer, store, logger.New())

	mux := http.NewServeMux()
	registerAuthHandlers(mux, interceptor, authEnabled)
	registerAllContentHandlers(mux, interceptor, olderMode)
	registerPackwizReadFixture(t, mux, interceptor, olderMode)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	f := &contentFixture{
		sessions: make(map[string]*sdkmcp.ClientSession),
		tokens:   make(map[string]string),
		server:   ts,
	}
	f.tokens["admin"] = seedToken(t, store, enforcer, "u-content-admin", "admin", nil)
	readerPerms := append(globalPerms("mods", "read", "create"),
		append(globalPerms("modpacks", "read", "create", "update", "delete"),
			append(globalPerms("module_templates", "read", "create", "update", "delete"),
				append(globalPerms("modules", "read", "create", "update", "delete", "start", "stop", "restart"),
					append(globalPerms("files", "read", "create", "update", "delete"),
						globalPerms("uploads", "read", "create", "delete")...)...)...)...)...)
	f.tokens["reader"] = seedToken(t, store, enforcer, "u-content-reader", "content-reader", readerPerms)
	f.tokens["noperms"] = seedToken(t, store, enforcer, "u-content-noperms", "content-noperms", globalPerms("servers", "read"))
	f.tokens["invalid"] = "dp_nonexistent_content_token"

	for name, token := range f.tokens {
		clients, err := NewContentClients(ts.Client(), ts.URL, token)
		if err != nil {
			t.Fatalf("NewContentClients(%s): %v", name, err)
		}
		s := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "content-test-server", Version: "v0"}, nil)
		registerContentTools(s, clients)
		f.sessions[name] = connectStdioSession(t, s, ctx)
	}
	return f
}

// --- minimal valid arguments for every registered content tool ------------

func contentValidArgs() map[string]map[string]any {
	const sha = "0000000000000000000000000000000000000000000000000000000000000000"
	b64 := "aGVsbG8="
	return map[string]map[string]any{
		packwizListTool: {},
		packwizGetTool:  {"pack_id": packwizFixtureID},
		// ModService.
		"list_mods":                         {"server_id": "srv-1"},
		"get_mod":                           {"server_id": "srv-1", "id": "mod-1"},
		"get_fabric_optimization_stack":     {"server_id": "srv-1"},
		"import_uploaded_mod":               {"server_id": "srv-1", "upload_session_id": "up-1", "confirm": true},
		"update_mod":                        {"server_id": "srv-1", "mod_id": "mod-1", "display_name": "m", "confirm": true},
		"delete_mod":                        {"server_id": "srv-1", "mod_id": "mod-1", "confirm": true},
		"install_fabric_optimization_stack": {"server_id": "srv-1", "mod_ids": []any{"lithium"}, "confirm": true},
		// ModBrowseService.
		"search_mods":      {"query": "jei"},
		"get_mod_versions": {"project": "jei"},
		"install_mods":     {"server_id": "srv-1", "items": []any{map[string]any{"name": "jei", "file_name": "jei.jar", "download_url": "https://example.com/jei.jar"}}, "confirm": true},
		// ModpackService.
		"search_modpacks":         {"query": "pack"},
		"get_modpack":             {"id": "mp-1"},
		"get_modpack_by_slug":     {"slug": "pack"},
		"get_modpack_by_url":      {"url": "https://example.com/pack"},
		"list_favorites":          {},
		"get_indexer_status":      {},
		"get_modpack_config":      {"id": "mp-1"},
		"get_modpack_files":       {"id": "mp-1"},
		"get_modpack_versions":    {"id": "mp-1"},
		"toggle_favorite":         {"id": "mp-1"},
		"sync_modpacks":           {"confirm": true},
		"import_uploaded_modpack": {"upload_session_id": "up-1", "confirm": true},
		"import_remote_modpack":   {"url": "https://example.com/pack.zip", "confirm": true},
		"delete_modpack":          {"id": "mp-1", "confirm": true},
		"sync_modpack_files":      {"id": "mp-1", "confirm": true},
		// DatapackService.
		"search_datapacks": {"query": "pack"},
		"list_datapacks":   {"server_id": "srv-1"},
		"install_datapack": {"server_id": "srv-1", "datapack_id": "dp-1", "confirm": true},
		"upload_datapack":  {"server_id": "srv-1", "upload_session_id": "up-1", "confirm": true},
		"delete_datapack":  {"server_id": "srv-1", "file_name": "pack.zip", "confirm": true},
		// ModuleService.
		"list_module_templates":          {},
		"get_module_template":            {"id": "tpl-1"},
		"list_modules":                   {},
		"get_module":                     {"id": "mod-1"},
		"get_module_logs":                {"id": "mod-1"},
		"get_next_available_module_port": {},
		"get_available_aliases":          {},
		"get_resolved_aliases":           {},
		"create_module_template":         {"name": "tpl", "docker_image": "nginx:latest", "confirm": true},
		"update_module_template":         {"id": "tpl-1", "name": "tpl", "confirm": true},
		"delete_module_template":         {"id": "tpl-1", "confirm": true},
		"create_module":                  {"name": "mod", "server_id": "srv-1", "template_id": "tpl-1", "confirm": true},
		"update_module":                  {"id": "mod-1", "name": "mod", "confirm": true},
		"delete_module":                  {"id": "mod-1", "confirm": true},
		"start_module":                   {"id": "mod-1", "confirm": true},
		"stop_module":                    {"id": "mod-1", "confirm": true},
		"restart_module":                 {"id": "mod-1", "confirm": true},
		"recreate_module":                {"id": "mod-1", "confirm": true},
		// FileService (this family's 15).
		"get_extraction_status":       {"operation_id": "op-1"},
		"get_remote_archive_progress": {"server_id": "srv-1", "task_id": "task-1"},
		"init_file_download":          {"server_id": "srv-1", "path": "server.properties"},
		"download_archive":            {"server_id": "srv-1", "paths": []any{"server.properties"}},
		"export_world":                {"server_id": "srv-1"},
		"update_file":                 {"server_id": "srv-1", "path": "server.properties", "content": b64, "confirm": true},
		"delete_file":                 {"server_id": "srv-1", "path": "server.properties", "confirm": true},
		"rename_file":                 {"server_id": "srv-1", "path": "a.txt", "new_name": "b.txt", "confirm": true},
		"extract_archive":             {"server_id": "srv-1", "path": "a.zip", "confirm": true},
		"create_folder":               {"server_id": "srv-1", "path": "folder", "confirm": true},
		"move_file":                   {"server_id": "srv-1", "source_path": "a.txt", "destination_path": "b.txt", "confirm": true},
		"copy_file":                   {"server_id": "srv-1", "source_path": "a.txt", "destination_path": "b.txt", "confirm": true},
		"create_archive":              {"server_id": "srv-1", "paths": []any{"a.txt"}, "archive_name": "a.zip", "confirm": true},
		"download_remote_archive":     {"server_id": "srv-1", "url": "https://example.com/a.zip", "destination_path": "a.zip", "sha256_checksum": sha, "confirm": true},
		"save_uploaded_file":          {"server_id": "srv-1", "upload_session_id": "up-1", "destination_path": ".", "filename": "a.jar", "confirm": true},
		// UploadService.
		"init_upload":       {"filename": "a.jar", "total_size": 1024, "chunk_size": 256, "confirm": true},
		"get_upload_status": {"session_id": "up-1"},
		"upload_chunk":      {"session_id": "up-1", "chunk_index": 0, "data": b64, "confirm": true},
		"cancel_upload":     {"session_id": "up-1", "confirm": true},
	}
}

func contentMutationTools() []string {
	var out []string
	for _, d := range ContentToolDescriptors {
		if d.Transport == "rpc" && d.RegisteredBy == "content" && d.Confirm {
			out = append(out, d.Tool)
		}
	}
	return out
}

// --- tests ----------------------------------------------------------------

// TestContentDescriptorCoverage checks LISTED / REACHABLE accounting and that
// every descriptor's factual tuple matches the generated catalog.
func TestContentDescriptorCoverage(t *testing.T) {
	counts := CoverageCounts()
	if counts.ListedDescriptors != 78 {
		t.Fatalf("listed descriptors = %d, want 78 (71 RPC + 4 REST + 3 controller-stream transport primitives)", counts.ListedDescriptors)
	}
	if counts.ListedRPCs != 71 || counts.ListedREST != 7 {
		t.Fatalf("listed RPC/REST = %d/%d, want 71/7 (the 3 extra non-RPC descriptors are controller-stream primitives)", counts.ListedRPCs, counts.ListedREST)
	}
	if counts.NewContentTools != 69 || counts.FirstSliceTools != 2 {
		t.Fatalf("new/first-slice tools = %d/%d, want 69/2", counts.NewContentTools, counts.FirstSliceTools)
	}
	if counts.ReachableRPCs != 71 {
		t.Fatalf("reachable RPCs = %d, want 71", counts.ReachableRPCs)
	}
	if ContentToolCount() != 69 {
		t.Fatalf("ContentToolCount = %d, want 69", ContentToolCount())
	}

	base := make(map[string]CatalogEntry, len(rawCatalogEntries))
	for _, e := range rawCatalogEntries {
		base[e.Procedure] = e
	}
	seen := make(map[string]bool)
	for _, d := range ContentToolDescriptors {
		if seen[d.Procedure] {
			t.Fatalf("duplicate descriptor %s", d.Procedure)
		}
		seen[d.Procedure] = true
		if d.Class == ClassExclude {
			t.Errorf("%s is still excluded", d.Procedure)
		}
		if d.Transport == "rpc" {
			e, ok := base[d.Procedure]
			if !ok {
				t.Fatalf("%s is not a mounted catalog procedure", d.Procedure)
			}
			if e.Service != d.Service || e.Method != d.Method || e.Client != d.Client || e.Scope != d.Scope || e.Build != d.Build {
				t.Errorf("%s: descriptor drifted from generated catalog: %+v vs %+v", d.Procedure, d, e)
			}
			if d.Tool == "" || d.BoundedArgs == "" {
				t.Errorf("%s: missing tool name or bounded args", d.Procedure)
			}
		} else {
			if d.SourceRef == "" || d.EquivalentFlow == "" {
				t.Errorf("%s: REST descriptor needs exact source ref and equivalent flow", d.Procedure)
			}
		}
		if d.Access == "mutation" && d.Access != "read" && d.Transport == "rpc" && d.RegisteredBy == "content" && d.Tool != "toggle_favorite" && !d.Confirm {
			t.Errorf("%s: mutation tool lacks a confirm gate", d.Tool)
		}
	}

	// All 69 family RPCs are listed; no extra procedures leaked in.
	for _, e := range rawCatalogEntries {
		family := strings.Contains(e.Service, "ModService") || strings.Contains(e.Service, "ModBrowseService") ||
			strings.Contains(e.Service, "ModpackService") || strings.Contains(e.Service, "DatapackService") ||
			strings.Contains(e.Service, "ModuleService") || strings.Contains(e.Service, "FileService") ||
			strings.Contains(e.Service, "UploadService")
		if family && !seen[e.Procedure] {
			t.Errorf("family RPC missing from descriptors: %s", e.Procedure)
		}
	}
}

// TestContentToolFullCoverageSuccess calls every registered content tool once
// with minimal bounded arguments and asserts a structured success response.
func TestContentToolFullCoverageSuccess(t *testing.T) {
	f := newContentFixture(t, "", true)
	args := contentValidArgs()
	registered := 0
	for _, d := range ContentToolDescriptors {
		if d.Transport != "rpc" || d.RegisteredBy != "content" {
			continue
		}
		registered++
		toolArgs, ok := args[d.Tool]
		if !ok {
			t.Fatalf("no fixture args for tool %s", d.Tool)
		}
		t.Run(d.Tool, func(t *testing.T) {
			out := callTool(t, f.sessions["admin"], d.Tool, toolArgs)
			assertSuccess(t, out, d.Tool)
		})
	}
	if registered != 69 {
		t.Fatalf("verified %d tools, want 69", registered)
	}
}

type contentFamilyCase struct {
	family string
	tool   string
}

func contentFamilyCases() []contentFamilyCase {
	return []contentFamilyCase{
		{"mod", "list_mods"},
		{"modbrowse", "search_mods"},
		{"modpack", "search_modpacks"},
		{"datapack", "list_datapacks"},
		{"module", "list_module_templates"},
		{"file", "get_extraction_status"},
		{"upload", "get_upload_status"},
	}
}

// TestContentFamiliesAuthErrors asserts success, structured PermissionDenied
// and structured Unauthenticated for each family.
func TestContentFamiliesAuthErrors(t *testing.T) {
	f := newContentFixture(t, "", true)
	args := contentValidArgs()
	for _, tc := range contentFamilyCases() {
		t.Run(tc.family+"/success", func(t *testing.T) {
			assertSuccess(t, callTool(t, f.sessions["reader"], tc.tool, args[tc.tool]), tc.tool)
		})
		t.Run(tc.family+"/permission_denied", func(t *testing.T) {
			e := assertStructuredError(t, callTool(t, f.sessions["noperms"], tc.tool, args[tc.tool]), tc.tool, ErrKindPermissionDenied)
			if e.Code != "permission_denied" || e.Tool != tc.tool || e.Procedure == "" {
				t.Fatalf("%s: incomplete structured error: %+v", tc.tool, e)
			}
		})
		t.Run(tc.family+"/unauthenticated", func(t *testing.T) {
			e := assertStructuredError(t, callTool(t, f.sessions["invalid"], tc.tool, args[tc.tool]), tc.tool, ErrKindUnauthenticated)
			if e.Code != "unauthenticated" {
				t.Fatalf("%s: code %q", tc.tool, e.Code)
			}
		})
	}
}

// TestContentConfirmGates asserts every mutation tool refuses to run without
// confirm=true.
func TestContentConfirmGates(t *testing.T) {
	f := newContentFixture(t, "", true)
	args := contentValidArgs()
	for _, tool := range contentMutationTools() {
		t.Run(tool, func(t *testing.T) {
			without := make(map[string]any, len(args[tool]))
			for k, v := range args[tool] {
				without[k] = v
			}
			delete(without, "confirm")
			e := assertStructuredError(t, callTool(t, f.sessions["admin"], tool, without), tool, ErrKindConfirmationRequired)
			if e.Tool != tool || e.Procedure == "" {
				t.Fatalf("%s: incomplete confirmation error: %+v", tool, e)
			}
		})
	}
}

func TestContentBoundedArguments(t *testing.T) {
	f := newContentFixture(t, "", true)
	bad := []struct {
		tool string
		args map[string]any
	}{
		{"get_mod", map[string]any{"server_id": "srv-1", "id": strings.Repeat("x", 129)}},
		{"search_mods", map[string]any{"query": strings.Repeat("x", 257)}},
		{"install_fabric_optimization_stack", map[string]any{"server_id": "srv-1", "mod_ids": []any{"not-a-known-mod"}, "confirm": true}},
		{"install_mods", map[string]any{"server_id": "srv-1", "items": []any{map[string]any{"name": "a", "file_name": "a.jar", "download_url": "http://example.com/a.jar"}}, "confirm": true}},
		{"get_modpack_by_url", map[string]any{"url": "https://user:pass@example.com/pack"}},
		{"search_datapacks", map[string]any{"query": "q", "offset": 20000}},
		{"get_module_logs", map[string]any{"id": "mod-1", "tail": 5000}},
		{"download_archive", map[string]any{"server_id": "srv-1", "paths": []any{"../secrets"}}},
		{"download_remote_archive", map[string]any{"server_id": "srv-1", "url": "https://example.com/a.zip", "destination_path": "a.zip", "sha256_checksum": "short", "confirm": true}},
		{"init_upload", map[string]any{"filename": "a.jar", "total_size": 1024, "chunk_size": 262145, "confirm": true}},
		{"upload_chunk", map[string]any{"session_id": "up-1", "chunk_index": 0, "data": "not-base64!!", "confirm": true}},
	}
	for _, tc := range bad {
		t.Run(tc.tool, func(t *testing.T) {
			e := assertStructuredError(t, callTool(t, f.sessions["admin"], tc.tool, tc.args), tc.tool, ErrKindInvalidArguments)
			if e.Tool != tc.tool {
				t.Fatalf("%s: wrong tool in error: %+v", tc.tool, e)
			}
		})
	}
}

// TestContentModBrowseCapabilityError verifies that a post-v1.0.40 content
// tool reports its own build, not the shared first-slice fallback.
func TestContentModBrowseCapabilityError(t *testing.T) {
	for _, olderMode := range []string{"unimplemented", "transport-notfound"} {
		t.Run(olderMode, func(t *testing.T) {
			f := newContentFixture(t, olderMode, true)
			e := assertStructuredError(t, callTool(t, f.sessions["admin"], "search_mods", map[string]any{"query": "jei"}), "search_mods", ErrKindCapability)
			if e.MinimumPanelBuild != string(PanelBuildPostStable) {
				t.Fatalf("minimum build %q, want %q", e.MinimumPanelBuild, PanelBuildPostStable)
			}
			if !strings.Contains(e.Message, "/carbonpanel.v1.ModBrowseService/SearchMods") || !strings.Contains(e.Message, "panel build") {
				t.Fatalf("capability message lacks procedure/build: %q", e.Message)
			}
		})
	}
}

// TestContentDescriptorArtifact keeps the machine-readable hand-off file in
// sync with the Go descriptor list. Regenerate with
// `go test ./internal/mcp -run TestContentDescriptorArtifact -update-content-descriptors`.
func TestContentDescriptorArtifact(t *testing.T) {
	const path = "family_content_descriptors.json"
	data, err := json.MarshalIndent(ContentToolDescriptors, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if *updateContentDescriptors {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (run with -update-content-descriptors): %v", path, err)
	}
	if !bytes.Equal(onDisk, data) {
		t.Fatalf("%s is stale; regenerate with -update-content-descriptors", path)
	}
}

func TestContentClientsRequireToken(t *testing.T) {
	if _, err := NewContentClients(nil, "http://example.com", ""); err == nil {
		t.Fatal("empty token was accepted")
	}
	clients, err := NewContentClients(nil, "http://example.com", "dp_test")
	if err != nil {
		t.Fatalf("NewContentClients: %v", err)
	}
	if clients.Mod == nil || clients.ModBrowse == nil || clients.Modpack == nil ||
		clients.Datapack == nil || clients.Module == nil || clients.File == nil || clients.Upload == nil {
		t.Fatal("a typed content client was not constructed")
	}
}
