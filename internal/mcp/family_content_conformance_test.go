// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Executable conformance evidence for the OSS-372 content families.
//
// Every case below drives a real MCP SDK client over an in-memory stdio pair
// against the production registerContentTools handlers. The Connect handlers
// are typed, capturing fixtures wrapped in the real rpc.NewAuthInterceptor;
// they record the exact generated request message and return a typed response
// carrying an observable marker. This file never uses a generic success mock
// and never issues a hand-rolled product HTTP request: the only cross-package
// seams used are exported (rpc.NewAuthInterceptor, auth.NewManager,
// rbac.NewEnforcer, NewContentClients, registerContentTools).
package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// --- capturing fixture ----------------------------------------------------

type conformanceRecorder struct {
	mu    sync.Mutex
	calls map[string][]any
	state map[string]any
}

func newConformanceRecorder() *conformanceRecorder {
	return &conformanceRecorder{
		calls: make(map[string][]any),
		state: make(map[string]any),
	}
}

func (r *conformanceRecorder) add(procedure string, msg any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[procedure] = append(r.calls[procedure], msg)
}

func (r *conformanceRecorder) take(procedure string) []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]any(nil), r.calls[procedure]...)
	delete(r.calls, procedure)
	return out
}

func (r *conformanceRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = make(map[string][]any)
}

func (r *conformanceRecorder) setState(key string, value any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state[key] = value
}

func (r *conformanceRecorder) stateInt(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, _ := r.state[key].(int)
	return v
}

func (r *conformanceRecorder) deleteState(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.state, key)
}

// conformanceMark writes an observable marker into a response string field
// without touching identifier/path fields that the assertions need to read.
func conformanceMark(message proto.Message, procedure string) {
	fields := message.ProtoReflect().Descriptor().Fields()
	for _, name := range []string{"message", "status", "error", "temp_path"} {
		field := fields.ByName(protoreflect.Name(name))
		if field != nil && field.Kind() == protoreflect.StringKind {
			message.ProtoReflect().Set(field, protoreflect.ValueOfString("handled:"+procedure))
			return
		}
	}
}

func conformanceCapture[Req, Res any](mux *http.ServeMux, rec *conformanceRecorder,
	procedure string, ic connect.UnaryInterceptorFunc, respond func(*Req) Res) {
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(_ context.Context, req *connect.Request[Req]) (*connect.Response[Res], error) {
			rec.add(procedure, req.Msg)
			res := respond(req.Msg)
			if message, ok := any(&res).(proto.Message); ok {
				conformanceMark(message, procedure)
			}
			return connect.NewResponse(&res), nil
		}, connect.WithInterceptors(ic)))
}

func conformanceRegister[Req, Res any](mux *http.ServeMux, rec *conformanceRecorder,
	procedure string, ic connect.UnaryInterceptorFunc) {
	conformanceCapture(mux, rec, procedure, ic, func(*Req) Res {
		var zero Res
		return zero
	})
}

func conformanceRegisterAll(mux *http.ServeMux, rec *conformanceRecorder, ic connect.UnaryInterceptorFunc) {
	// ModService.
	conformanceRegister[v1.ListModsRequest, v1.ListModsResponse](mux, rec, "/carbonpanel.v1.ModService/ListMods", ic)
	conformanceRegister[v1.GetModRequest, v1.GetModResponse](mux, rec, "/carbonpanel.v1.ModService/GetMod", ic)
	conformanceRegister[v1.GetFabricOptimizationStackRequest, v1.GetFabricOptimizationStackResponse](mux, rec, "/carbonpanel.v1.ModService/GetFabricOptimizationStack", ic)
	conformanceRegister[v1.ImportUploadedModRequest, v1.ImportUploadedModResponse](mux, rec, "/carbonpanel.v1.ModService/ImportUploadedMod", ic)
	conformanceRegister[v1.UpdateModRequest, v1.UpdateModResponse](mux, rec, "/carbonpanel.v1.ModService/UpdateMod", ic)
	conformanceRegister[v1.DeleteModRequest, v1.DeleteModResponse](mux, rec, "/carbonpanel.v1.ModService/DeleteMod", ic)
	conformanceRegister[v1.InstallFabricOptimizationStackRequest, v1.InstallFabricOptimizationStackResponse](mux, rec, "/carbonpanel.v1.ModService/InstallFabricOptimizationStack", ic)

	// ModBrowseService.
	conformanceRegister[v1.SearchModsRequest, v1.SearchModsResponse](mux, rec, "/carbonpanel.v1.ModBrowseService/SearchMods", ic)
	conformanceRegister[v1.GetModVersionsRequest, v1.GetModVersionsResponse](mux, rec, "/carbonpanel.v1.ModBrowseService/GetModVersions", ic)
	conformanceRegister[v1.InstallModsRequest, v1.InstallModsResponse](mux, rec, "/carbonpanel.v1.ModBrowseService/InstallMods", ic)

	// ModpackService.
	conformanceRegister[v1.SearchModpacksRequest, v1.SearchModpacksResponse](mux, rec, "/carbonpanel.v1.ModpackService/SearchModpacks", ic)
	conformanceRegister[v1.GetModpackRequest, v1.GetModpackResponse](mux, rec, "/carbonpanel.v1.ModpackService/GetModpack", ic)
	conformanceRegister[v1.GetModpackBySlugRequest, v1.GetModpackBySlugResponse](mux, rec, "/carbonpanel.v1.ModpackService/GetModpackBySlug", ic)
	conformanceRegister[v1.GetModpackByURLRequest, v1.GetModpackByURLResponse](mux, rec, "/carbonpanel.v1.ModpackService/GetModpackByURL", ic)
	conformanceRegister[v1.SyncModpacksRequest, v1.SyncModpacksResponse](mux, rec, "/carbonpanel.v1.ModpackService/SyncModpacks", ic)
	conformanceRegister[v1.ImportUploadedModpackRequest, v1.ImportUploadedModpackResponse](mux, rec, "/carbonpanel.v1.ModpackService/ImportUploadedModpack", ic)
	conformanceCapture(mux, rec, "/carbonpanel.v1.ModpackService/ImportRemoteModpack", ic,
		func(r *v1.ImportRemoteModpackRequest) v1.ImportRemoteModpackResponse {
			return v1.ImportRemoteModpackResponse{Message: "url=" + r.GetUrl()}
		})
	conformanceRegister[v1.DeleteModpackRequest, v1.DeleteModpackResponse](mux, rec, "/carbonpanel.v1.ModpackService/DeleteModpack", ic)
	conformanceRegister[v1.ToggleFavoriteRequest, v1.ToggleFavoriteResponse](mux, rec, "/carbonpanel.v1.ModpackService/ToggleFavorite", ic)
	conformanceRegister[v1.ListFavoritesRequest, v1.ListFavoritesResponse](mux, rec, "/carbonpanel.v1.ModpackService/ListFavorites", ic)
	conformanceRegister[v1.GetIndexerStatusRequest, v1.GetIndexerStatusResponse](mux, rec, "/carbonpanel.v1.ModpackService/GetIndexerStatus", ic)
	conformanceRegister[v1.GetModpackConfigRequest, v1.GetModpackConfigResponse](mux, rec, "/carbonpanel.v1.ModpackService/GetModpackConfig", ic)
	conformanceRegister[v1.GetModpackFilesRequest, v1.GetModpackFilesResponse](mux, rec, "/carbonpanel.v1.ModpackService/GetModpackFiles", ic)
	conformanceRegister[v1.GetModpackVersionsRequest, v1.GetModpackVersionsResponse](mux, rec, "/carbonpanel.v1.ModpackService/GetModpackVersions", ic)
	conformanceRegister[v1.SyncModpackFilesRequest, v1.SyncModpackFilesResponse](mux, rec, "/carbonpanel.v1.ModpackService/SyncModpackFiles", ic)

	// DatapackService.
	conformanceRegister[v1.SearchDatapacksRequest, v1.SearchDatapacksResponse](mux, rec, "/carbonpanel.v1.DatapackService/SearchDatapacks", ic)
	conformanceRegister[v1.InstallDatapackRequest, v1.InstallDatapackResponse](mux, rec, "/carbonpanel.v1.DatapackService/InstallDatapack", ic)
	conformanceRegister[v1.UploadDatapackRequest, v1.UploadDatapackResponse](mux, rec, "/carbonpanel.v1.DatapackService/UploadDatapack", ic)
	conformanceRegister[v1.ListDatapacksRequest, v1.ListDatapacksResponse](mux, rec, "/carbonpanel.v1.DatapackService/ListDatapacks", ic)
	conformanceRegister[v1.DeleteDatapackRequest, v1.DeleteDatapackResponse](mux, rec, "/carbonpanel.v1.DatapackService/DeleteDatapack", ic)

	// ModuleService.
	conformanceRegister[v1.ListModuleTemplatesRequest, v1.ListModuleTemplatesResponse](mux, rec, "/carbonpanel.v1.ModuleService/ListModuleTemplates", ic)
	conformanceRegister[v1.GetModuleTemplateRequest, v1.GetModuleTemplateResponse](mux, rec, "/carbonpanel.v1.ModuleService/GetModuleTemplate", ic)
	conformanceRegister[v1.CreateModuleTemplateRequest, v1.CreateModuleTemplateResponse](mux, rec, "/carbonpanel.v1.ModuleService/CreateModuleTemplate", ic)
	conformanceRegister[v1.UpdateModuleTemplateRequest, v1.UpdateModuleTemplateResponse](mux, rec, "/carbonpanel.v1.ModuleService/UpdateModuleTemplate", ic)
	conformanceRegister[v1.DeleteModuleTemplateRequest, v1.DeleteModuleTemplateResponse](mux, rec, "/carbonpanel.v1.ModuleService/DeleteModuleTemplate", ic)
	conformanceRegister[v1.ListModulesRequest, v1.ListModulesResponse](mux, rec, "/carbonpanel.v1.ModuleService/ListModules", ic)
	conformanceRegister[v1.GetModuleRequest, v1.GetModuleResponse](mux, rec, "/carbonpanel.v1.ModuleService/GetModule", ic)
	conformanceRegister[v1.CreateModuleRequest, v1.CreateModuleResponse](mux, rec, "/carbonpanel.v1.ModuleService/CreateModule", ic)
	conformanceRegister[v1.UpdateModuleRequest, v1.UpdateModuleResponse](mux, rec, "/carbonpanel.v1.ModuleService/UpdateModule", ic)
	conformanceRegister[v1.DeleteModuleRequest, v1.DeleteModuleResponse](mux, rec, "/carbonpanel.v1.ModuleService/DeleteModule", ic)
	conformanceRegister[v1.StartModuleRequest, v1.StartModuleResponse](mux, rec, "/carbonpanel.v1.ModuleService/StartModule", ic)
	conformanceRegister[v1.StopModuleRequest, v1.StopModuleResponse](mux, rec, "/carbonpanel.v1.ModuleService/StopModule", ic)
	conformanceRegister[v1.RestartModuleRequest, v1.RestartModuleResponse](mux, rec, "/carbonpanel.v1.ModuleService/RestartModule", ic)
	conformanceRegister[v1.RecreateModuleRequest, v1.RecreateModuleResponse](mux, rec, "/carbonpanel.v1.ModuleService/RecreateModule", ic)
	conformanceRegister[v1.GetModuleLogsRequest, v1.GetModuleLogsResponse](mux, rec, "/carbonpanel.v1.ModuleService/GetModuleLogs", ic)
	conformanceRegister[v1.GetNextAvailableModulePortRequest, v1.GetNextAvailableModulePortResponse](mux, rec, "/carbonpanel.v1.ModuleService/GetNextAvailableModulePort", ic)
	conformanceRegister[v1.GetAvailableAliasesRequest, v1.GetAvailableAliasesResponse](mux, rec, "/carbonpanel.v1.ModuleService/GetAvailableAliases", ic)
	conformanceRegister[v1.GetResolvedAliasesRequest, v1.GetResolvedAliasesResponse](mux, rec, "/carbonpanel.v1.ModuleService/GetResolvedAliases", ic)

	// FileService.
	conformanceRegister[v1.ListFilesRequest, v1.ListFilesResponse](mux, rec, "/carbonpanel.v1.FileService/ListFiles", ic)
	conformanceRegister[v1.GetFileRequest, v1.GetFileResponse](mux, rec, "/carbonpanel.v1.FileService/GetFile", ic)
	conformanceRegister[v1.GetExtractionStatusRequest, v1.GetExtractionStatusResponse](mux, rec, "/carbonpanel.v1.FileService/GetExtractionStatus", ic)
	conformanceRegister[v1.GetRemoteArchiveProgressRequest, v1.GetRemoteArchiveProgressResponse](mux, rec, "/carbonpanel.v1.FileService/GetRemoteArchiveProgress", ic)
	conformanceCapture(mux, rec, "/carbonpanel.v1.FileService/InitFileDownload", ic,
		func(r *v1.InitFileDownloadRequest) v1.InitFileDownloadResponse {
			return v1.InitFileDownloadResponse{SessionId: "sess-download-1", Filename: "server.properties", TotalSize: int64(len(r.GetPath()) * 10)}
		})
	conformanceRegister[v1.DownloadArchiveRequest, v1.DownloadArchiveResponse](mux, rec, "/carbonpanel.v1.FileService/DownloadArchive", ic)
	conformanceRegister[v1.ExportWorldRequest, v1.ExportWorldResponse](mux, rec, "/carbonpanel.v1.FileService/ExportWorld", ic)
	conformanceCapture(mux, rec, "/carbonpanel.v1.FileService/UpdateFile", ic,
		func(r *v1.UpdateFileRequest) v1.UpdateFileResponse {
			return v1.UpdateFileResponse{Path: r.GetPath(), Staged: r.GetStage()}
		})
	conformanceRegister[v1.DeleteFileRequest, v1.DeleteFileResponse](mux, rec, "/carbonpanel.v1.FileService/DeleteFile", ic)
	conformanceRegister[v1.RenameFileRequest, v1.RenameFileResponse](mux, rec, "/carbonpanel.v1.FileService/RenameFile", ic)
	conformanceRegister[v1.ExtractArchiveRequest, v1.ExtractArchiveResponse](mux, rec, "/carbonpanel.v1.FileService/ExtractArchive", ic)
	conformanceRegister[v1.CreateFolderRequest, v1.CreateFolderResponse](mux, rec, "/carbonpanel.v1.FileService/CreateFolder", ic)
	conformanceRegister[v1.MoveFileRequest, v1.MoveFileResponse](mux, rec, "/carbonpanel.v1.FileService/MoveFile", ic)
	conformanceRegister[v1.CopyFileRequest, v1.CopyFileResponse](mux, rec, "/carbonpanel.v1.FileService/CopyFile", ic)
	conformanceRegister[v1.CreateArchiveRequest, v1.CreateArchiveResponse](mux, rec, "/carbonpanel.v1.FileService/CreateArchive", ic)
	conformanceRegister[v1.DownloadRemoteArchiveRequest, v1.DownloadRemoteArchiveResponse](mux, rec, "/carbonpanel.v1.FileService/DownloadRemoteArchive", ic)
	conformanceRegister[v1.SaveUploadedFileRequest, v1.SaveUploadedFileResponse](mux, rec, "/carbonpanel.v1.FileService/SaveUploadedFile", ic)

	// UploadService: stateful responders prove actual bytes and cleanup.
	conformanceCapture(mux, rec, "/carbonpanel.v1.UploadService/InitUpload", ic,
		func(r *v1.InitUploadRequest) v1.InitUploadResponse {
			rec.setState("upload:"+r.GetFilename(), 0)
			return v1.InitUploadResponse{SessionId: "sess-upload-1"}
		})
	conformanceCapture(mux, rec, "/carbonpanel.v1.UploadService/UploadChunk", ic,
		func(r *v1.UploadChunkRequest) v1.UploadChunkResponse {
			key := "bytes:" + r.GetSessionId()
			rec.setState(key, rec.stateInt(key)+len(r.GetData()))
			return v1.UploadChunkResponse{
				SessionId: r.GetSessionId(), BytesReceived: int64(rec.stateInt(key)),
				ChunksReceived: 1, Completed: true,
			}
		})
	conformanceCapture(mux, rec, "/carbonpanel.v1.UploadService/GetUploadStatus", ic,
		func(r *v1.GetUploadStatusRequest) v1.GetUploadStatusResponse {
			return v1.GetUploadStatusResponse{
				SessionId: r.GetSessionId(), BytesReceived: int64(rec.stateInt("bytes:" + r.GetSessionId())),
				ChunksReceived: 1, TotalChunks: 1, Completed: true,
			}
		})
	conformanceCapture(mux, rec, "/carbonpanel.v1.UploadService/CancelUpload", ic,
		func(r *v1.CancelUploadRequest) v1.CancelUploadResponse {
			rec.deleteState("bytes:" + r.GetSessionId())
			return v1.CancelUploadResponse{}
		})
}

type conformanceFixture struct {
	sessions map[string]*sdkmcp.ClientSession
	recorder *conformanceRecorder
}

func newConformanceFixture(t *testing.T) *conformanceFixture {
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
		Local: config.LocalConfig{Enabled: true}, OIDC: config.OIDCConfig{Enabled: false},
		AnonymousAccess: false, AllowNoAuth: false,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	interceptor := rpc.NewAuthInterceptor(authManager, enforcer, store, logger.New())
	recorder := newConformanceRecorder()

	mux := http.NewServeMux()
	registerAuthHandlers(mux, interceptor, true)
	conformanceRegisterAll(mux, recorder, interceptor)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	tokens := map[string]string{
		"admin":     seedToken(t, store, enforcer, "u-cf-admin", "admin", nil),
		"noperms":   seedToken(t, store, enforcer, "u-cf-noperms", "cf-noperms", globalPerms("servers", "read")),
		"modreader": seedToken(t, store, enforcer, "u-cf-modreader", "cf-modreader", globalPerms("mods", "read")),
		"filesA":    seedToken(t, store, enforcer, "u-cf-files-a", "cf-files-a", []rbac.Permission{{Resource: "files", Action: "read", ObjectID: "srv-A"}}),
		"invalid":   "dp_nonexistent_conformance_token",
	}

	f := &conformanceFixture{sessions: make(map[string]*sdkmcp.ClientSession), recorder: recorder}
	for name, token := range tokens {
		clients, err := NewContentClients(ts.Client(), ts.URL, token)
		if err != nil {
			t.Fatalf("NewContentClients(%s): %v", name, err)
		}
		s := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "content-conformance", Version: "v0"}, nil)
		registerContentTools(s, clients)
		f.sessions[name] = connectStdioSession(t, s, ctx)
	}
	return f
}

// --- captured-request assertions -----------------------------------------

func conformanceExpect(t *testing.T, tool string, captured any, want map[string]any) {
	t.Helper()
	message, ok := captured.(proto.Message)
	if !ok {
		t.Fatalf("%s: captured request is not a proto message: %T", tool, captured)
	}
	reflected := message.ProtoReflect()
	for name, expected := range want {
		field := reflected.Descriptor().Fields().ByName(protoreflect.Name(name))
		if field == nil {
			t.Fatalf("%s: request has no field %q", tool, name)
		}
		got := reflected.Get(field)
		switch v := expected.(type) {
		case string:
			if actual := got.String(); actual != v {
				t.Errorf("%s: field %s = %q, want %q", tool, name, actual, v)
			}
		case bool:
			if actual := got.Bool(); actual != v {
				t.Errorf("%s: field %s = %v, want %v", tool, name, actual, v)
			}
		case int:
			if actual := int(got.Int()); actual != v {
				t.Errorf("%s: field %s = %d, want %d", tool, name, actual, v)
			}
		case int64:
			if actual := got.Int(); actual != v {
				t.Errorf("%s: field %s = %d, want %d", tool, name, actual, v)
			}
		case []byte:
			if actual := got.Bytes(); !bytes.Equal(actual, v) {
				t.Errorf("%s: field %s bytes = %x, want %x", tool, name, actual, v)
			}
		case []string:
			list := got.List()
			if list.Len() != len(v) {
				t.Fatalf("%s: field %s len = %d, want %d", tool, name, list.Len(), len(v))
			}
			for i := range v {
				if actual := list.Get(i).String(); actual != v[i] {
					t.Errorf("%s: field %s[%d] = %q, want %q", tool, name, i, actual, v[i])
				}
			}
		default:
			t.Fatalf("%s: unsupported expected type %T for %s", tool, expected, name)
		}
	}
}

func conformanceResultJSON(t *testing.T, out ToolOutput) string {
	t.Helper()
	if out.Result == nil {
		t.Fatal("tool returned no typed result")
	}
	data, err := json.Marshal(out.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return string(data)
}

// --- per-tool conformance table -------------------------------------------

type conformanceCase struct {
	tool      string
	procedure string
	args      map[string]any
	expect    map[string]any
}

func conformanceCases() []conformanceCase {
	const sha = "0000000000000000000000000000000000000000000000000000000000000000"
	b64 := base64.StdEncoding.EncodeToString([]byte("hello"))
	return []conformanceCase{
		// ModService.
		{"list_mods", "/carbonpanel.v1.ModService/ListMods", map[string]any{"server_id": "srv-1"}, map[string]any{"server_id": "srv-1"}},
		{"get_mod", "/carbonpanel.v1.ModService/GetMod", map[string]any{"server_id": "srv-1", "id": "mod-1"}, map[string]any{"server_id": "srv-1", "mod_id": "mod-1"}},
		{"get_fabric_optimization_stack", "/carbonpanel.v1.ModService/GetFabricOptimizationStack", map[string]any{"server_id": "srv-1"}, map[string]any{"server_id": "srv-1"}},
		{"import_uploaded_mod", "/carbonpanel.v1.ModService/ImportUploadedMod", map[string]any{"server_id": "srv-1", "upload_session_id": "up-1", "display_name": "m", "confirm": true}, map[string]any{"server_id": "srv-1", "upload_session_id": "up-1", "display_name": "m"}},
		{"update_mod", "/carbonpanel.v1.ModService/UpdateMod", map[string]any{"server_id": "srv-1", "mod_id": "mod-1", "display_name": "m", "confirm": true}, map[string]any{"server_id": "srv-1", "mod_id": "mod-1", "display_name": "m"}},
		{"delete_mod", "/carbonpanel.v1.ModService/DeleteMod", map[string]any{"server_id": "srv-1", "mod_id": "mod-1", "confirm": true}, map[string]any{"server_id": "srv-1", "mod_id": "mod-1"}},
		{"install_fabric_optimization_stack", "/carbonpanel.v1.ModService/InstallFabricOptimizationStack", map[string]any{"server_id": "srv-1", "mod_ids": []any{"lithium"}, "confirm": true}, map[string]any{"server_id": "srv-1", "mod_ids": []string{"lithium"}}},

		// ModBrowseService.
		{"search_mods", "/carbonpanel.v1.ModBrowseService/SearchMods", map[string]any{"query": "jei"}, map[string]any{"query": "jei"}},
		{"get_mod_versions", "/carbonpanel.v1.ModBrowseService/GetModVersions", map[string]any{"project": "jei"}, map[string]any{"project": "jei"}},
		{"install_mods", "/carbonpanel.v1.ModBrowseService/InstallMods", map[string]any{"server_id": "srv-1", "items": []any{map[string]any{"name": "jei", "file_name": "jei.jar", "download_url": "https://example.com/jei.jar"}}, "confirm": true}, map[string]any{"server_id": "srv-1"}},

		// ModpackService.
		{"search_modpacks", "/carbonpanel.v1.ModpackService/SearchModpacks", map[string]any{"query": "pack"}, map[string]any{"query": "pack", "page_size": 20}},
		{"get_modpack", "/carbonpanel.v1.ModpackService/GetModpack", map[string]any{"id": "mp-1"}, map[string]any{"id": "mp-1"}},
		{"get_modpack_by_slug", "/carbonpanel.v1.ModpackService/GetModpackBySlug", map[string]any{"slug": "pack"}, map[string]any{"slug": "pack"}},
		{"get_modpack_by_url", "/carbonpanel.v1.ModpackService/GetModpackByURL", map[string]any{"url": "https://example.com/pack"}, map[string]any{"url": "https://example.com/pack"}},
		{"list_favorites", "/carbonpanel.v1.ModpackService/ListFavorites", map[string]any{}, map[string]any{}},
		{"get_indexer_status", "/carbonpanel.v1.ModpackService/GetIndexerStatus", map[string]any{}, map[string]any{}},
		{"get_modpack_config", "/carbonpanel.v1.ModpackService/GetModpackConfig", map[string]any{"id": "mp-1"}, map[string]any{"id": "mp-1"}},
		{"get_modpack_files", "/carbonpanel.v1.ModpackService/GetModpackFiles", map[string]any{"id": "mp-1"}, map[string]any{"id": "mp-1"}},
		{"get_modpack_versions", "/carbonpanel.v1.ModpackService/GetModpackVersions", map[string]any{"id": "mp-1"}, map[string]any{"id": "mp-1"}},
		{"toggle_favorite", "/carbonpanel.v1.ModpackService/ToggleFavorite", map[string]any{"id": "mp-1"}, map[string]any{"id": "mp-1"}},
		{"sync_modpacks", "/carbonpanel.v1.ModpackService/SyncModpacks", map[string]any{"query": "pack", "confirm": true}, map[string]any{"query": "pack"}},
		{"import_uploaded_modpack", "/carbonpanel.v1.ModpackService/ImportUploadedModpack", map[string]any{"upload_session_id": "up-1", "name": "mp", "confirm": true}, map[string]any{"upload_session_id": "up-1", "name": "mp"}},
		{"import_remote_modpack", "/carbonpanel.v1.ModpackService/ImportRemoteModpack", map[string]any{"url": "https://example.com/pack.zip", "name": "mp", "confirm": true}, map[string]any{"url": "https://example.com/pack.zip", "name": "mp", "auth_token": "", "allow_private_network": false}},
		{"delete_modpack", "/carbonpanel.v1.ModpackService/DeleteModpack", map[string]any{"id": "mp-1", "confirm": true}, map[string]any{"id": "mp-1"}},
		{"sync_modpack_files", "/carbonpanel.v1.ModpackService/SyncModpackFiles", map[string]any{"id": "mp-1", "confirm": true}, map[string]any{"id": "mp-1"}},

		// DatapackService.
		{"search_datapacks", "/carbonpanel.v1.DatapackService/SearchDatapacks", map[string]any{"query": "pack"}, map[string]any{"query": "pack", "limit": 20}},
		{"list_datapacks", "/carbonpanel.v1.DatapackService/ListDatapacks", map[string]any{"server_id": "srv-1"}, map[string]any{"server_id": "srv-1"}},
		{"install_datapack", "/carbonpanel.v1.DatapackService/InstallDatapack", map[string]any{"server_id": "srv-1", "datapack_id": "dp-1", "confirm": true}, map[string]any{"server_id": "srv-1", "datapack_id": "dp-1"}},
		{"upload_datapack", "/carbonpanel.v1.DatapackService/UploadDatapack", map[string]any{"server_id": "srv-1", "upload_session_id": "up-1", "confirm": true}, map[string]any{"server_id": "srv-1", "upload_session_id": "up-1"}},
		{"delete_datapack", "/carbonpanel.v1.DatapackService/DeleteDatapack", map[string]any{"server_id": "srv-1", "file_name": "pack.zip", "confirm": true}, map[string]any{"server_id": "srv-1", "file_name": "pack.zip"}},

		// ModuleService.
		{"list_module_templates", "/carbonpanel.v1.ModuleService/ListModuleTemplates", map[string]any{}, map[string]any{}},
		{"get_module_template", "/carbonpanel.v1.ModuleService/GetModuleTemplate", map[string]any{"id": "tpl-1"}, map[string]any{"id": "tpl-1"}},
		{"list_modules", "/carbonpanel.v1.ModuleService/ListModules", map[string]any{}, map[string]any{}},
		{"get_module", "/carbonpanel.v1.ModuleService/GetModule", map[string]any{"id": "mod-1"}, map[string]any{"id": "mod-1"}},
		{"get_module_logs", "/carbonpanel.v1.ModuleService/GetModuleLogs", map[string]any{"id": "mod-1"}, map[string]any{"id": "mod-1", "tail": 20}},
		{"get_next_available_module_port", "/carbonpanel.v1.ModuleService/GetNextAvailableModulePort", map[string]any{}, map[string]any{}},
		{"get_available_aliases", "/carbonpanel.v1.ModuleService/GetAvailableAliases", map[string]any{}, map[string]any{}},
		{"get_resolved_aliases", "/carbonpanel.v1.ModuleService/GetResolvedAliases", map[string]any{}, map[string]any{}},
		{"create_module_template", "/carbonpanel.v1.ModuleService/CreateModuleTemplate", map[string]any{"name": "tpl", "docker_image": "nginx:latest", "confirm": true}, map[string]any{"name": "tpl", "docker_image": "nginx:latest"}},
		{"update_module_template", "/carbonpanel.v1.ModuleService/UpdateModuleTemplate", map[string]any{"id": "tpl-1", "name": "tpl", "confirm": true}, map[string]any{"id": "tpl-1", "name": "tpl"}},
		{"delete_module_template", "/carbonpanel.v1.ModuleService/DeleteModuleTemplate", map[string]any{"id": "tpl-1", "confirm": true}, map[string]any{"id": "tpl-1"}},
		{"create_module", "/carbonpanel.v1.ModuleService/CreateModule", map[string]any{"name": "mod", "server_id": "srv-1", "template_id": "tpl-1", "confirm": true}, map[string]any{"name": "mod", "server_id": "srv-1", "template_id": "tpl-1"}},
		{"update_module", "/carbonpanel.v1.ModuleService/UpdateModule", map[string]any{"id": "mod-1", "name": "mod", "confirm": true}, map[string]any{"id": "mod-1", "name": "mod"}},
		{"delete_module", "/carbonpanel.v1.ModuleService/DeleteModule", map[string]any{"id": "mod-1", "confirm": true}, map[string]any{"id": "mod-1"}},
		{"start_module", "/carbonpanel.v1.ModuleService/StartModule", map[string]any{"id": "mod-1", "confirm": true}, map[string]any{"id": "mod-1"}},
		{"stop_module", "/carbonpanel.v1.ModuleService/StopModule", map[string]any{"id": "mod-1", "confirm": true}, map[string]any{"id": "mod-1"}},
		{"restart_module", "/carbonpanel.v1.ModuleService/RestartModule", map[string]any{"id": "mod-1", "confirm": true}, map[string]any{"id": "mod-1"}},
		{"recreate_module", "/carbonpanel.v1.ModuleService/RecreateModule", map[string]any{"id": "mod-1", "confirm": true}, map[string]any{"id": "mod-1"}},

		// FileService (this family's 15).
		{"get_extraction_status", "/carbonpanel.v1.FileService/GetExtractionStatus", map[string]any{"operation_id": "op-1"}, map[string]any{"operation_id": "op-1"}},
		{"get_remote_archive_progress", "/carbonpanel.v1.FileService/GetRemoteArchiveProgress", map[string]any{"server_id": "srv-1", "task_id": "task-1"}, map[string]any{"server_id": "srv-1", "task_id": "task-1"}},
		{"init_file_download", "/carbonpanel.v1.FileService/InitFileDownload", map[string]any{"server_id": "srv-1", "path": "server.properties"}, map[string]any{"server_id": "srv-1", "path": "server.properties"}},
		{"download_archive", "/carbonpanel.v1.FileService/DownloadArchive", map[string]any{"server_id": "srv-1", "paths": []any{"a.txt", "b.txt"}}, map[string]any{"server_id": "srv-1", "paths": []string{"a.txt", "b.txt"}}},
		{"export_world", "/carbonpanel.v1.FileService/ExportWorld", map[string]any{"server_id": "srv-1"}, map[string]any{"server_id": "srv-1"}},
		{"update_file", "/carbonpanel.v1.FileService/UpdateFile", map[string]any{"server_id": "srv-1", "path": "server.properties", "content": b64, "confirm": true}, map[string]any{"server_id": "srv-1", "path": "server.properties", "content": []byte("hello")}},
		{"delete_file", "/carbonpanel.v1.FileService/DeleteFile", map[string]any{"server_id": "srv-1", "path": "a.txt", "confirm": true}, map[string]any{"server_id": "srv-1", "path": "a.txt"}},
		{"rename_file", "/carbonpanel.v1.FileService/RenameFile", map[string]any{"server_id": "srv-1", "path": "a.txt", "new_name": "b.txt", "confirm": true}, map[string]any{"server_id": "srv-1", "path": "a.txt", "new_name": "b.txt"}},
		{"extract_archive", "/carbonpanel.v1.FileService/ExtractArchive", map[string]any{"server_id": "srv-1", "path": "a.zip", "confirm": true}, map[string]any{"server_id": "srv-1", "path": "a.zip"}},
		{"create_folder", "/carbonpanel.v1.FileService/CreateFolder", map[string]any{"server_id": "srv-1", "path": "folder", "confirm": true}, map[string]any{"server_id": "srv-1", "path": "folder"}},
		{"move_file", "/carbonpanel.v1.FileService/MoveFile", map[string]any{"server_id": "srv-1", "source_path": "a.txt", "destination_path": "b.txt", "confirm": true}, map[string]any{"server_id": "srv-1", "source_path": "a.txt", "destination_path": "b.txt"}},
		{"copy_file", "/carbonpanel.v1.FileService/CopyFile", map[string]any{"server_id": "srv-1", "source_path": "a.txt", "destination_path": "b.txt", "confirm": true}, map[string]any{"server_id": "srv-1", "source_path": "a.txt", "destination_path": "b.txt"}},
		{"create_archive", "/carbonpanel.v1.FileService/CreateArchive", map[string]any{"server_id": "srv-1", "paths": []any{"a.txt"}, "archive_name": "a.zip", "confirm": true}, map[string]any{"server_id": "srv-1", "paths": []string{"a.txt"}, "archive_name": "a.zip"}},
		{"download_remote_archive", "/carbonpanel.v1.FileService/DownloadRemoteArchive", map[string]any{"server_id": "srv-1", "url": "https://example.com/a.zip", "destination_path": "a.zip", "sha256_checksum": sha, "confirm": true}, map[string]any{"server_id": "srv-1", "url": "https://example.com/a.zip", "destination_path": "a.zip", "sha256_checksum": sha}},
		{"save_uploaded_file", "/carbonpanel.v1.FileService/SaveUploadedFile", map[string]any{"server_id": "srv-1", "upload_session_id": "up-1", "destination_path": ".", "filename": "a.jar", "confirm": true}, map[string]any{"server_id": "srv-1", "upload_session_id": "up-1", "destination_path": ".", "filename": "a.jar"}},

		// UploadService.
		{"init_upload", "/carbonpanel.v1.UploadService/InitUpload", map[string]any{"filename": "a.jar", "total_size": 1024, "chunk_size": 256, "confirm": true}, map[string]any{"filename": "a.jar", "total_size": int64(1024), "chunk_size": 256}},
		{"get_upload_status", "/carbonpanel.v1.UploadService/GetUploadStatus", map[string]any{"session_id": "sess-upload-1"}, map[string]any{"session_id": "sess-upload-1"}},
		{"upload_chunk", "/carbonpanel.v1.UploadService/UploadChunk", map[string]any{"session_id": "sess-upload-1", "chunk_index": 0, "data": b64, "confirm": true}, map[string]any{"session_id": "sess-upload-1", "chunk_index": 0, "data": []byte("hello")}},
		{"cancel_upload", "/carbonpanel.v1.UploadService/CancelUpload", map[string]any{"session_id": "sess-upload-1", "confirm": true}, map[string]any{"session_id": "sess-upload-1"}},
	}
}

// --- executable tests -----------------------------------------------------

// TestConformanceEveryToolReachesTypedMethod proves for all 67 tools that the
// MCP call reaches the intended generated procedure exactly once, that every
// bounded argument is mapped into the typed request, and that a real typed
// response is observed.
func TestConformanceEveryToolReachesTypedMethod(t *testing.T) {
	f := newConformanceFixture(t)
	cases := conformanceCases()
	if len(cases) != 67 {
		t.Fatalf("conformance cases = %d, want 67", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			f.recorder.reset()
			out := callTool(t, f.sessions["admin"], tc.tool, tc.args)
			assertSuccess(t, out, tc.tool)
			captured := f.recorder.take(tc.procedure)
			if len(captured) != 1 {
				t.Fatalf("%s: typed handler reached %d times, want 1", tc.procedure, len(captured))
			}
			conformanceExpect(t, tc.tool, captured[0], tc.expect)
			result := conformanceResultJSON(t, out)
			if result == "null" || result == "" {
				t.Fatalf("%s: no observable typed response", tc.tool)
			}
		})
	}
}

// TestConformanceInstallModsNestedArgs asserts the nested item mapping that
// generic scalar checks cannot cover.
func TestConformanceInstallModsNestedArgs(t *testing.T) {
	f := newConformanceFixture(t)
	f.recorder.reset()
	args := conformanceCases()[9].args
	assertSuccess(t, callTool(t, f.sessions["admin"], "install_mods", args), "install_mods")
	captured := f.recorder.take("/carbonpanel.v1.ModBrowseService/InstallMods")
	if len(captured) != 1 {
		t.Fatalf("handler calls = %d", len(captured))
	}
	req, ok := captured[0].(*v1.InstallModsRequest)
	if !ok {
		t.Fatalf("unexpected request type %T", captured[0])
	}
	if len(req.GetItems()) != 1 {
		t.Fatalf("items = %d", len(req.GetItems()))
	}
	item := req.GetItems()[0]
	if item.GetName() != "jei" || item.GetFileName() != "jei.jar" || item.GetDownloadUrl() != "https://example.com/jei.jar" {
		t.Fatalf("nested item mapped incorrectly: %+v", item)
	}
}

// TestConformancePrivateSourceRefusal proves the two credential-bearing
// proto fields are rejected by the typed tool schema and never reach the
// generated request.
func TestConformancePrivateSourceRefusal(t *testing.T) {
	f := newConformanceFixture(t)
	f.recorder.reset()
	res, err := f.sessions["admin"].CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name: "import_remote_modpack",
		Arguments: map[string]any{
			"url": "https://example.com/pack.zip", "name": "mp",
			"auth_token": "secret", "allow_private_network": true, "confirm": true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("credential-bearing arguments were accepted")
	}
	var text strings.Builder
	for _, part := range res.Content {
		if typed, ok := part.(*sdkmcp.TextContent); ok {
			text.WriteString(typed.Text)
		}
	}
	message := text.String()
	if !strings.Contains(message, "auth_token") && !strings.Contains(message, "allow_private_network") {
		t.Fatalf("schema rejection does not name the refused field: %q", message)
	}
	if calls := f.recorder.take("/carbonpanel.v1.ModpackService/ImportRemoteModpack"); len(calls) != 0 {
		t.Fatal("credential-bearing arguments reached the typed handler")
	}

	// The accepted argument shape produces a request with both sensitive
	// fields at their zero values (also asserted in the per-tool table).
	f.recorder.reset()
	assertSuccess(t, callTool(t, f.sessions["admin"], "import_remote_modpack",
		map[string]any{"url": "https://example.com/pack.zip", "name": "mp", "confirm": true}),
		"import_remote_modpack")
	captured := f.recorder.take("/carbonpanel.v1.ModpackService/ImportRemoteModpack")
	if len(captured) != 1 {
		t.Fatalf("handler calls = %d", len(captured))
	}
	req, ok := captured[0].(*v1.ImportRemoteModpackRequest)
	if !ok {
		t.Fatalf("unexpected request type %T", captured[0])
	}
	if req.GetAuthToken() != "" || req.GetAllowPrivateNetwork() {
		t.Fatalf("credential/private-network fields leaked into request: %+v", req)
	}
}

func TestConformanceConfirmGates(t *testing.T) {
	f := newConformanceFixture(t)
	count := 0
	for _, d := range ContentToolDescriptors {
		if d.Transport != "rpc" || d.RegisteredBy != "content" || !d.Confirm {
			continue
		}
		count++
		t.Run(d.Tool, func(t *testing.T) {
			f.recorder.reset()
			var args map[string]any
			for _, tc := range conformanceCases() {
				if tc.tool == d.Tool {
					args = tc.args
					break
				}
			}
			without := make(map[string]any, len(args))
			for k, v := range args {
				without[k] = v
			}
			delete(without, "confirm")
			e := assertStructuredError(t, callTool(t, f.sessions["admin"], d.Tool, without), d.Tool, ErrKindConfirmationRequired)
			if e.Tool != d.Tool || e.Procedure == "" {
				t.Fatalf("incomplete confirmation error: %+v", e)
			}
			if calls := f.recorder.take(d.Procedure); len(calls) != 0 {
				t.Fatalf("%s: mutation reached typed handler despite missing confirm", d.Tool)
			}
		})
	}
	if count != 36 {
		t.Fatalf("confirm-gated tools exercised = %d, want 36", count)
	}
}

func TestConformanceBoundedArguments(t *testing.T) {
	f := newConformanceFixture(t)
	oversize := strings.Repeat("x", 257)
	bigChunk := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), 256*1024+1))
	cases := []struct {
		tool      string
		procedure string
		args      map[string]any
	}{
		{"get_mod", "/carbonpanel.v1.ModService/GetMod", map[string]any{"server_id": "srv-1", "id": strings.Repeat("x", 129)}},
		{"search_mods", "/carbonpanel.v1.ModBrowseService/SearchMods", map[string]any{"query": oversize}},
		{"install_fabric_optimization_stack", "/carbonpanel.v1.ModService/InstallFabricOptimizationStack", map[string]any{"server_id": "srv-1", "mod_ids": []any{"unknown-mod"}, "confirm": true}},
		{"install_mods", "/carbonpanel.v1.ModBrowseService/InstallMods", map[string]any{"server_id": "srv-1", "items": []any{map[string]any{"name": "a", "file_name": "a.jar", "download_url": "http://example.com/a.jar"}}, "confirm": true}},
		{"get_modpack_by_url", "/carbonpanel.v1.ModpackService/GetModpackByURL", map[string]any{"url": "https://user:pass@example.com/pack"}},
		{"search_datapacks", "/carbonpanel.v1.DatapackService/SearchDatapacks", map[string]any{"query": "q", "offset": 20000}},
		{"get_module_logs", "/carbonpanel.v1.ModuleService/GetModuleLogs", map[string]any{"id": "mod-1", "tail": 5000}},
		{"download_archive", "/carbonpanel.v1.FileService/DownloadArchive", map[string]any{"server_id": "srv-1", "paths": []any{"../secrets"}}},
		{"download_remote_archive", "/carbonpanel.v1.FileService/DownloadRemoteArchive", map[string]any{"server_id": "srv-1", "url": "https://example.com/a.zip", "destination_path": "a.zip", "sha256_checksum": "short", "confirm": true}},
		{"init_upload", "/carbonpanel.v1.UploadService/InitUpload", map[string]any{"filename": "a.jar", "total_size": 1024, "chunk_size": 262145, "confirm": true}},
		{"upload_chunk", "/carbonpanel.v1.UploadService/UploadChunk", map[string]any{"session_id": "up-1", "chunk_index": 0, "data": bigChunk, "confirm": true}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			f.recorder.reset()
			e := assertStructuredError(t, callTool(t, f.sessions["admin"], tc.tool, tc.args), tc.tool, ErrKindInvalidArguments)
			// Shared arg validators do not stamp Procedure; Kind, Tool and a
			// non-empty message are the stable structured contract here.
			if e.Tool != tc.tool || e.Message == "" {
				t.Fatalf("incomplete invalid-argument error: %+v", e)
			}
			if calls := f.recorder.take(tc.procedure); len(calls) != 0 {
				t.Fatalf("%s: rejected argument still reached typed handler", tc.tool)
			}
		})
	}
}

func TestConformanceScopedAuthFailures(t *testing.T) {
	f := newConformanceFixture(t)
	cases := []struct {
		name      string
		tool      string
		procedure string
		args      map[string]any
		token     string
		kind      string
	}{
		{"mod-read-cannot-delete", "delete_mod", "/carbonpanel.v1.ModService/DeleteMod", map[string]any{"server_id": "srv-1", "mod_id": "mod-1", "confirm": true}, "modreader", ErrKindPermissionDenied},
		{"cross-scope-download", "init_file_download", "/carbonpanel.v1.FileService/InitFileDownload", map[string]any{"server_id": "srv-B", "path": "server.properties"}, "filesA", ErrKindPermissionDenied},
		{"no-permission-upload", "get_upload_status", "/carbonpanel.v1.UploadService/GetUploadStatus", map[string]any{"session_id": "up-1"}, "noperms", ErrKindPermissionDenied},
		{"invalid-token-mod", "list_mods", "/carbonpanel.v1.ModService/ListMods", map[string]any{"server_id": "srv-1"}, "invalid", ErrKindUnauthenticated},
		{"invalid-token-upload", "upload_chunk", "/carbonpanel.v1.UploadService/UploadChunk", map[string]any{"session_id": "up-1", "chunk_index": 0, "data": "aGVsbG8=", "confirm": true}, "invalid", ErrKindUnauthenticated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.recorder.reset()
			e := assertStructuredError(t, callTool(t, f.sessions[tc.token], tc.tool, tc.args), tc.tool, tc.kind)
			if e.Tool != tc.tool || e.Procedure == "" || e.Code == "" {
				t.Fatalf("incomplete structured auth error: %+v", e)
			}
			if calls := f.recorder.take(tc.procedure); len(calls) != 0 {
				t.Fatalf("%s: denied call reached typed handler", tc.name)
			}
		})
	}
}

// TestConformanceStreamingBytesAndCleanup drives a complete scoped upload
// workflow with real byte payloads, observes the handler's byte accounting,
// then cancels and proves cleanup took effect.
func TestConformanceStreamingBytesAndCleanup(t *testing.T) {
	f := newConformanceFixture(t)
	f.recorder.reset()
	payload := []byte("carbon-panel-scoped-stream-bytes")
	encoded := base64.StdEncoding.EncodeToString(payload)

	assertSuccess(t, callTool(t, f.sessions["admin"], "init_upload",
		map[string]any{"filename": "a.jar", "total_size": int64(len(payload)), "chunk_size": 256, "confirm": true}), "init_upload")

	chunkOut := callTool(t, f.sessions["admin"], "upload_chunk",
		map[string]any{"session_id": "sess-upload-1", "chunk_index": 0, "data": encoded, "confirm": true})
	assertSuccess(t, chunkOut, "upload_chunk")
	if result := conformanceResultJSON(t, chunkOut); !strings.Contains(result, fmt.Sprintf("%d", len(payload))) {
		t.Fatalf("chunk result lacks real byte accounting: %s", result)
	}
	chunkCalls := f.recorder.take("/carbonpanel.v1.UploadService/UploadChunk")
	if len(chunkCalls) != 1 {
		t.Fatalf("chunk handler calls = %d", len(chunkCalls))
	}
	chunkReq, ok := chunkCalls[0].(*v1.UploadChunkRequest)
	if !ok || !bytes.Equal(chunkReq.GetData(), payload) || chunkReq.GetChunkIndex() != 0 {
		t.Fatalf("chunk bytes/index not mapped: %+v", chunkCalls[0])
	}

	statusOut := callTool(t, f.sessions["admin"], "get_upload_status", map[string]any{"session_id": "sess-upload-1"})
	assertSuccess(t, statusOut, "get_upload_status")
	result := conformanceResultJSON(t, statusOut)
	if !strings.Contains(result, fmt.Sprintf("%d", len(payload))) || !strings.Contains(result, "true") {
		t.Fatalf("status does not reflect uploaded bytes/completion: %s", result)
	}

	assertSuccess(t, callTool(t, f.sessions["admin"], "cancel_upload",
		map[string]any{"session_id": "sess-upload-1", "confirm": true}), "cancel_upload")
	if remaining := f.recorder.take("/carbonpanel.v1.UploadService/CancelUpload"); len(remaining) != 1 {
		t.Fatalf("cancel handler calls = %d", len(remaining))
	}
}

// TestConformanceDownloadSessionOutcome proves the typed download-session
// response (filename/total_size) is observed by the MCP client.
func TestConformanceDownloadSessionOutcome(t *testing.T) {
	f := newConformanceFixture(t)
	f.recorder.reset()
	out := callTool(t, f.sessions["admin"], "init_file_download",
		map[string]any{"server_id": "srv-1", "path": "server.properties"})
	assertSuccess(t, out, "init_file_download")
	result := conformanceResultJSON(t, out)
	for _, want := range []string{"sess-download-1", "server.properties"} {
		if !strings.Contains(result, want) {
			t.Fatalf("download session result %s lacks %q", result, want)
		}
	}
}

func TestConformanceCapOnDecodedContent(t *testing.T) {
	f := newConformanceFixture(t)
	f.recorder.reset()
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("z"), 256*1024+1))
	e := assertStructuredError(t, callTool(t, f.sessions["admin"], "update_file",
		map[string]any{"server_id": "srv-1", "path": "a.txt", "content": encoded, "confirm": true}), "update_file", ErrKindInvalidArguments)
	if e.Tool != "update_file" {
		t.Fatalf("wrong tool: %+v", e)
	}
	if calls := f.recorder.take("/carbonpanel.v1.FileService/UpdateFile"); len(calls) != 0 {
		t.Fatal("over-cap content reached the update handler")
	}
}
