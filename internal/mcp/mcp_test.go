// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// invocationRecorder captures a typed clone of the Connect request each
// fixture handler receives. Exact field assertions read the typed message;
// no substring-of-JSON check can count as mapping evidence.
type invocationRecord struct {
	Procedure string
	Msg       protoreflect.Message
}

type invocationRecorderState struct {
	mu      sync.Mutex
	enabled bool
	records []invocationRecord
}

var invocationRecorder = &invocationRecorderState{}

func (r *invocationRecorderState) enable() {
	r.mu.Lock()
	r.enabled = true
	r.records = nil
	r.mu.Unlock()
}

func (r *invocationRecorderState) disable() {
	r.mu.Lock()
	r.enabled = false
	r.mu.Unlock()
}

func (r *invocationRecorderState) record(procedure string, msg any) {
	if m, ok := msg.(proto.Message); ok {
		cloned := proto.Clone(m)
		r.mu.Lock()
		if r.enabled {
			r.records = append(r.records, invocationRecord{Procedure: procedure, Msg: cloned.ProtoReflect()})
		}
		r.mu.Unlock()
	}
}

// invocationRecordJSON is only for human-readable failure diagnostics; it is
// never used as the mapping assertion itself.
func invocationRecordJSON(rec invocationRecord) string {
	if rec.Msg == nil {
		return ""
	}
	b, err := protojson.Marshal(rec.Msg.Interface())
	if err != nil {
		return fmt.Sprintf("%v", err)
	}
	return string(b)
}

func (r *invocationRecorderState) snapshot() []invocationRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]invocationRecord, len(r.records))
	copy(out, r.records)
	return out
}

// fixture bundles a real SDK MCP client connected over the stdio wire format
// to the in-process MCP server, whose tools call typed Connect clients against
// disposable httptest handlers behind the production auth interceptor.
type fixture struct {
	sessions  map[string]*sdkmcp.ClientSession
	tokens    map[string]string
	server    *httptest.Server
	verifyErr error
}

func newTestStore(t *testing.T) *storage.Store {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_", "\\", "_").Replace(t.Name())
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Path:           fmt.Sprintf("file:%s?mode=memory&cache=shared", dbName),
			AutoMigrate:    true,
			MaxConnections: 5,
		},
	}
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedToken(t *testing.T, store *storage.Store, enforcer *rbac.Enforcer, userID, role string, perms []rbac.Permission) string {
	t.Helper()
	ctx := context.Background()
	if role != "admin" {
		if err := enforcer.SetPermissionsForRole(role, perms); err != nil {
			t.Fatalf("SetPermissionsForRole(%s): %v", role, err)
		}
	}
	if err := store.CreateUser(ctx, &storage.User{ID: userID, Username: userID, AuthProvider: "local", IsActive: true}); err != nil {
		t.Fatalf("CreateUser(%s): %v", userID, err)
	}
	if err := store.AssignRole(ctx, userID, role, "manual"); err != nil {
		t.Fatalf("AssignRole(%s): %v", userID, err)
	}
	raw := "dp_" + userID + "token"
	sum := sha256.Sum256([]byte(raw))
	if err := store.CreateAPIToken(ctx, &storage.APIToken{UserID: userID, Name: "mcp-test", TokenHash: hex.EncodeToString(sum[:])}); err != nil {
		t.Fatalf("CreateAPIToken(%s): %v", userID, err)
	}
	return raw
}

func globalPerms(resource string, actions ...string) []rbac.Permission {
	out := make([]rbac.Permission, 0, len(actions))
	for _, action := range actions {
		out = append(out, rbac.Permission{Resource: resource, Action: action, ObjectID: "*"})
	}
	return out
}

func registerOK[Req, Res any](mux *http.ServeMux, procedure string, ic connect.UnaryInterceptorFunc, olderMode string) {
	var handler func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error)
	postStable := false
	if e, ok := DeclaredProcedure(procedure); ok {
		postStable = e.Build != PanelBuildStableV140
	}
	if olderMode == "domain-notfound" && procedure == "/carbonpanel.v1.ServerService/GetServer" {
		handler = func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("server srv-missing not found"))
		}
	} else if olderMode != "" && postStable {
		// Older panels report an unregistered procedure either as Unimplemented
		// or as a transport-level 404/NotFound with an explicit marker.
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
		handler = func(_ context.Context, req *connect.Request[Req]) (*connect.Response[Res], error) {
			invocationRecorder.record(procedure, req.Msg)
			var zero Res
			return connect.NewResponse(&zero), nil
		}
	}
	mux.Handle(procedure, connect.NewUnaryHandler(procedure, handler, connect.WithInterceptors(ic)))
}

// registerAuthHandlers mounts GetAuthStatus (public) and GetCurrentUser
// (authenticated-only) with the real interceptor so Clients.Verify exercises
// the production auth path before any tool is exposed.
func registerAuthHandlers(mux *http.ServeMux, ic connect.UnaryInterceptorFunc, authEnabled bool) {
	const statusProc = "/carbonpanel.v1.AuthService/GetAuthStatus"
	mux.Handle(statusProc, connect.NewUnaryHandler(statusProc,
		func(_ context.Context, req *connect.Request[v1.GetAuthStatusRequest]) (*connect.Response[v1.GetAuthStatusResponse], error) {
			invocationRecorder.record(statusProc, req.Msg)
			return connect.NewResponse(&v1.GetAuthStatusResponse{LocalAuthEnabled: authEnabled}), nil
		}, connect.WithInterceptors(ic)))

	const userProc = "/carbonpanel.v1.AuthService/GetCurrentUser"
	mux.Handle(userProc, connect.NewUnaryHandler(userProc,
		func(ctx context.Context, req *connect.Request[v1.GetCurrentUserRequest]) (*connect.Response[v1.GetCurrentUserResponse], error) {
			invocationRecorder.record(userProc, req.Msg)
			u := auth.GetUserFromContext(ctx)
			if u == nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no authenticated user"))
			}
			return connect.NewResponse(&v1.GetCurrentUserResponse{User: &v1.User{
				Id: u.ID, Username: u.Username, IsActive: true, Roles: u.Roles,
			}}), nil
		}, connect.WithInterceptors(ic)))
}

// registerToolHandlers mounts one concrete generated procedure per exposed
// tool with the real production auth interceptor.
func registerToolHandlers(mux *http.ServeMux, ic connect.UnaryInterceptorFunc, olderMode string) {
	registerOK[v1.ListStagedChangesRequest, v1.ListStagedChangesResponse](mux, stagedConfigListSpec.Procedure, ic, olderMode)
	registerOK[v1.ListServersRequest, v1.ListServersResponse](mux, "/carbonpanel.v1.ServerService/ListServers", ic, olderMode)
	registerOK[v1.GetServerRequest, v1.GetServerResponse](mux, "/carbonpanel.v1.ServerService/GetServer", ic, olderMode)
	registerOK[v1.StartServerRequest, v1.StartServerResponse](mux, "/carbonpanel.v1.ServerService/StartServer", ic, olderMode)
	registerOK[v1.StopServerRequest, v1.StopServerResponse](mux, "/carbonpanel.v1.ServerService/StopServer", ic, olderMode)
	registerOK[v1.PauseServerRequest, v1.PauseServerResponse](mux, "/carbonpanel.v1.ServerService/PauseServer", ic, olderMode)
	registerOK[v1.DeepSleepServerRequest, v1.DeepSleepServerResponse](mux, "/carbonpanel.v1.ServerService/DeepSleepServer", ic, olderMode)
	registerOK[v1.RestartServerRequest, v1.RestartServerResponse](mux, "/carbonpanel.v1.ServerService/RestartServer", ic, olderMode)

	registerOK[v1.LookupPlayerRequest, v1.LookupPlayerResponse](mux, "/carbonpanel.v1.PlayerService/LookupPlayer", ic, olderMode)
	registerOK[v1.ListPlayersRequest, v1.ListPlayersResponse](mux, "/carbonpanel.v1.PlayerService/ListPlayers", ic, olderMode)
	registerOK[v1.GetPlayerRequest, v1.GetPlayerResponse](mux, "/carbonpanel.v1.PlayerService/GetPlayer", ic, olderMode)
	registerOK[v1.ListPlayerCapabilitiesRequest, v1.ListPlayerCapabilitiesResponse](mux, "/carbonpanel.v1.PlayerService/ListPlayerCapabilities", ic, olderMode)
	registerOK[v1.GetPlayerSkinRequest, v1.GetPlayerSkinResponse](mux, "/carbonpanel.v1.PlayerService/GetPlayerSkin", ic, olderMode)

	registerOK[v1.ListWorldEntriesRequest, v1.ListWorldEntriesResponse](mux, "/carbonpanel.v1.WorldService/ListWorldEntries", ic, olderMode)
	registerOK[v1.GetWorldEntryRequest, v1.GetWorldEntryResponse](mux, "/carbonpanel.v1.WorldService/GetWorldEntry", ic, olderMode)
	registerOK[v1.ListRevisionsRequest, v1.ListRevisionsResponse](mux, "/carbonpanel.v1.WorldService/ListRevisions", ic, olderMode)
	registerOK[v1.GetRevisionRequest, v1.GetRevisionResponse](mux, "/carbonpanel.v1.WorldService/GetRevision", ic, olderMode)
	registerOK[v1.ListBindingsRequest, v1.ListBindingsResponse](mux, "/carbonpanel.v1.WorldService/ListBindings", ic, olderMode)
	registerOK[v1.GetBindingRequest, v1.GetBindingResponse](mux, "/carbonpanel.v1.WorldService/GetBinding", ic, olderMode)
	registerOK[v1.ListImportJobsRequest, v1.ListImportJobsResponse](mux, "/carbonpanel.v1.WorldService/ListImportJobs", ic, olderMode)
	registerOK[v1.GetImportJobRequest, v1.GetImportJobResponse](mux, "/carbonpanel.v1.WorldService/GetImportJob", ic, olderMode)
	registerOK[v1.CheckCompatibilityRequest, v1.CheckCompatibilityResponse](mux, "/carbonpanel.v1.WorldService/CheckCompatibility", ic, olderMode)
	registerOK[v1.GetWorldConfigSchemaRequest, v1.GetWorldConfigSchemaResponse](mux, "/carbonpanel.v1.WorldService/GetWorldConfigSchema", ic, olderMode)
	registerOK[v1.PreviewWorldConfigRequest, v1.PreviewWorldConfigResponse](mux, "/carbonpanel.v1.WorldService/PreviewWorldConfig", ic, olderMode)
	registerOK[v1.SearchWorldArtifactsRequest, v1.SearchWorldArtifactsResponse](mux, "/carbonpanel.v1.WorldService/SearchWorldArtifacts", ic, olderMode)
	registerOK[v1.GetProviderCapabilitiesRequest, v1.GetProviderCapabilitiesResponse](mux, "/carbonpanel.v1.WorldService/GetProviderCapabilities", ic, olderMode)

	registerOK[v1.ListWorldViewTargetsRequest, v1.ListWorldViewTargetsResponse](mux, "/carbonpanel.v1.WorldViewService/ListWorldViewTargets", ic, olderMode)
	registerOK[v1.GetWorldViewStatusRequest, v1.GetWorldViewStatusResponse](mux, "/carbonpanel.v1.WorldViewService/GetWorldViewStatus", ic, olderMode)
	registerOK[v1.ListCapabilitiesRequest, v1.ListCapabilitiesResponse](mux, "/carbonpanel.v1.WorldViewService/ListCapabilities", ic, olderMode)
	registerOK[v1.ListMapOverlaysRequest, v1.ListMapOverlaysResponse](mux, "/carbonpanel.v1.WorldViewService/ListMapOverlays", ic, olderMode)

	registerOK[v1.ListBackupsRequest, v1.ListBackupsResponse](mux, "/carbonpanel.v1.BackupService/ListBackups", ic, olderMode)
	registerOK[v1.RestoreBackupRequest, v1.RestoreBackupResponse](mux, "/carbonpanel.v1.BackupService/RestoreBackup", ic, olderMode)

	registerOK[v1.ListFilesRequest, v1.ListFilesResponse](mux, "/carbonpanel.v1.FileService/ListFiles", ic, olderMode)
	registerOK[v1.GetFileRequest, v1.GetFileResponse](mux, "/carbonpanel.v1.FileService/GetFile", ic, olderMode)
}

// registerCombinedToolHandlers mounts the remaining OSS-370 supported
// procedures behind the same production auth interceptor. These registrations
// are the fixture evidence consumed by family_audit_test.go's VERIFIED signal;
// each is a typed generated Connect handler, never a hand-rolled HTTP route.
func registerCombinedToolHandlers(mux *http.ServeMux, ic connect.UnaryInterceptorFunc, olderMode string) {
	registerOK[v1.ListActivityLogsRequest, v1.ListActivityLogsResponse](mux, "/carbonpanel.v1.ActivityService/ListActivityLogs", ic, olderMode)
	registerOK[v1.ChangePasswordRequest, v1.ChangePasswordResponse](mux, "/carbonpanel.v1.AuthService/ChangePassword", ic, olderMode)
	registerOK[v1.CreateInviteRequest, v1.CreateInviteResponse](mux, "/carbonpanel.v1.AuthService/CreateInvite", ic, olderMode)
	registerOK[v1.DeleteInviteRequest, v1.DeleteInviteResponse](mux, "/carbonpanel.v1.AuthService/DeleteInvite", ic, olderMode)
	registerOK[v1.GetAuthConfigRequest, v1.GetAuthConfigResponse](mux, "/carbonpanel.v1.AuthService/GetAuthConfig", ic, olderMode)
	registerOK[v1.GetInviteRequest, v1.GetInviteResponse](mux, "/carbonpanel.v1.AuthService/GetInvite", ic, olderMode)
	registerOK[v1.ListInvitesRequest, v1.ListInvitesResponse](mux, "/carbonpanel.v1.AuthService/ListInvites", ic, olderMode)
	registerOK[v1.LogoutRequest, v1.LogoutResponse](mux, "/carbonpanel.v1.AuthService/Logout", ic, olderMode)
	registerOK[v1.UpdateAuthSettingsRequest, v1.UpdateAuthSettingsResponse](mux, "/carbonpanel.v1.AuthService/UpdateAuthSettings", ic, olderMode)
	registerOK[v1.CreateBackupRequest, v1.CreateBackupResponse](mux, "/carbonpanel.v1.BackupService/CreateBackup", ic, olderMode)
	registerOK[v1.DeleteBackupRequest, v1.DeleteBackupResponse](mux, "/carbonpanel.v1.BackupService/DeleteBackup", ic, olderMode)
	registerOK[v1.SetBackupLockedRequest, v1.SetBackupLockedResponse](mux, "/carbonpanel.v1.BackupService/SetBackupLocked", ic, olderMode)
	registerOK[v1.GetBlueprintRequest, v1.GetBlueprintResponse](mux, "/carbonpanel.v1.BlueprintService/GetBlueprint", ic, olderMode)
	registerOK[v1.ListBlueprintsRequest, v1.ListBlueprintsResponse](mux, "/carbonpanel.v1.BlueprintService/ListBlueprints", ic, olderMode)
	registerOK[v1.DiscardUpdateBatchRequest, v1.DiscardUpdateBatchResponse](mux, "/carbonpanel.v1.ConfigService/DiscardUpdateBatch", ic, olderMode)
	registerOK[v1.GetGlobalSettingsRequest, v1.GetGlobalSettingsResponse](mux, "/carbonpanel.v1.ConfigService/GetGlobalSettings", ic, olderMode)
	registerOK[v1.GetServerConfigRequest, v1.GetServerConfigResponse](mux, "/carbonpanel.v1.ConfigService/GetServerConfig", ic, olderMode)
	registerOK[v1.GetUpdateBatchRequest, v1.GetUpdateBatchResponse](mux, "/carbonpanel.v1.ConfigService/GetUpdateBatch", ic, olderMode)
	registerOK[v1.SyncGlobalSettingsToServersRequest, v1.SyncGlobalSettingsToServersResponse](mux, "/carbonpanel.v1.ConfigService/SyncGlobalSettingsToServers", ic, olderMode)
	registerOK[v1.UpdateGlobalSettingsRequest, v1.UpdateGlobalSettingsResponse](mux, "/carbonpanel.v1.ConfigService/UpdateGlobalSettings", ic, olderMode)
	registerOK[v1.UpdateServerConfigRequest, v1.UpdateServerConfigResponse](mux, "/carbonpanel.v1.ConfigService/UpdateServerConfig", ic, olderMode)
	registerOK[v1.ValidateProviderKeyRequest, v1.ValidateProviderKeyResponse](mux, "/carbonpanel.v1.ConfigService/ValidateProviderKey", ic, olderMode)
	registerOK[v1.DeleteDatapackRequest, v1.DeleteDatapackResponse](mux, "/carbonpanel.v1.DatapackService/DeleteDatapack", ic, olderMode)
	registerOK[v1.InstallDatapackRequest, v1.InstallDatapackResponse](mux, "/carbonpanel.v1.DatapackService/InstallDatapack", ic, olderMode)
	registerOK[v1.ListDatapacksRequest, v1.ListDatapacksResponse](mux, "/carbonpanel.v1.DatapackService/ListDatapacks", ic, olderMode)
	registerOK[v1.SearchDatapacksRequest, v1.SearchDatapacksResponse](mux, "/carbonpanel.v1.DatapackService/SearchDatapacks", ic, olderMode)
	registerOK[v1.UploadDatapackRequest, v1.UploadDatapackResponse](mux, "/carbonpanel.v1.DatapackService/UploadDatapack", ic, olderMode)
	registerOK[v1.CopyFileRequest, v1.CopyFileResponse](mux, "/carbonpanel.v1.FileService/CopyFile", ic, olderMode)
	registerOK[v1.CreateArchiveRequest, v1.CreateArchiveResponse](mux, "/carbonpanel.v1.FileService/CreateArchive", ic, olderMode)
	registerOK[v1.CreateFolderRequest, v1.CreateFolderResponse](mux, "/carbonpanel.v1.FileService/CreateFolder", ic, olderMode)
	registerOK[v1.DeleteFileRequest, v1.DeleteFileResponse](mux, "/carbonpanel.v1.FileService/DeleteFile", ic, olderMode)
	registerOK[v1.DownloadArchiveRequest, v1.DownloadArchiveResponse](mux, "/carbonpanel.v1.FileService/DownloadArchive", ic, olderMode)
	registerOK[v1.DownloadRemoteArchiveRequest, v1.DownloadRemoteArchiveResponse](mux, "/carbonpanel.v1.FileService/DownloadRemoteArchive", ic, olderMode)
	registerOK[v1.ExportWorldRequest, v1.ExportWorldResponse](mux, "/carbonpanel.v1.FileService/ExportWorld", ic, olderMode)
	registerOK[v1.ExtractArchiveRequest, v1.ExtractArchiveResponse](mux, "/carbonpanel.v1.FileService/ExtractArchive", ic, olderMode)
	registerOK[v1.GetExtractionStatusRequest, v1.GetExtractionStatusResponse](mux, "/carbonpanel.v1.FileService/GetExtractionStatus", ic, olderMode)
	registerOK[v1.GetRemoteArchiveProgressRequest, v1.GetRemoteArchiveProgressResponse](mux, "/carbonpanel.v1.FileService/GetRemoteArchiveProgress", ic, olderMode)
	registerOK[v1.InitFileDownloadRequest, v1.InitFileDownloadResponse](mux, "/carbonpanel.v1.FileService/InitFileDownload", ic, olderMode)
	registerOK[v1.MoveFileRequest, v1.MoveFileResponse](mux, "/carbonpanel.v1.FileService/MoveFile", ic, olderMode)
	registerOK[v1.RenameFileRequest, v1.RenameFileResponse](mux, "/carbonpanel.v1.FileService/RenameFile", ic, olderMode)
	registerOK[v1.SaveUploadedFileRequest, v1.SaveUploadedFileResponse](mux, "/carbonpanel.v1.FileService/SaveUploadedFile", ic, olderMode)
	registerOK[v1.UpdateFileRequest, v1.UpdateFileResponse](mux, "/carbonpanel.v1.FileService/UpdateFile", ic, olderMode)
	registerOK[v1.GetDockerImagesRequest, v1.GetDockerImagesResponse](mux, "/carbonpanel.v1.MinecraftService/GetDockerImages", ic, olderMode)
	registerOK[v1.GetLoaderVersionsRequest, v1.GetLoaderVersionsResponse](mux, "/carbonpanel.v1.MinecraftService/GetLoaderVersions", ic, olderMode)
	registerOK[v1.GetMinecraftVersionsRequest, v1.GetMinecraftVersionsResponse](mux, "/carbonpanel.v1.MinecraftService/GetMinecraftVersions", ic, olderMode)
	registerOK[v1.GetModLoadersRequest, v1.GetModLoadersResponse](mux, "/carbonpanel.v1.MinecraftService/GetModLoaders", ic, olderMode)
	registerOK[v1.GetModVersionsRequest, v1.GetModVersionsResponse](mux, "/carbonpanel.v1.ModBrowseService/GetModVersions", ic, olderMode)
	registerOK[v1.InstallModsRequest, v1.InstallModsResponse](mux, "/carbonpanel.v1.ModBrowseService/InstallMods", ic, olderMode)
	registerOK[v1.SearchModsRequest, v1.SearchModsResponse](mux, "/carbonpanel.v1.ModBrowseService/SearchMods", ic, olderMode)
	registerOK[v1.DeleteModRequest, v1.DeleteModResponse](mux, "/carbonpanel.v1.ModService/DeleteMod", ic, olderMode)
	registerOK[v1.GetFabricOptimizationStackRequest, v1.GetFabricOptimizationStackResponse](mux, "/carbonpanel.v1.ModService/GetFabricOptimizationStack", ic, olderMode)
	registerOK[v1.GetModRequest, v1.GetModResponse](mux, "/carbonpanel.v1.ModService/GetMod", ic, olderMode)
	registerOK[v1.ImportUploadedModRequest, v1.ImportUploadedModResponse](mux, "/carbonpanel.v1.ModService/ImportUploadedMod", ic, olderMode)
	registerOK[v1.InstallFabricOptimizationStackRequest, v1.InstallFabricOptimizationStackResponse](mux, "/carbonpanel.v1.ModService/InstallFabricOptimizationStack", ic, olderMode)
	registerOK[v1.ListModsRequest, v1.ListModsResponse](mux, "/carbonpanel.v1.ModService/ListMods", ic, olderMode)
	registerOK[v1.UpdateModRequest, v1.UpdateModResponse](mux, "/carbonpanel.v1.ModService/UpdateMod", ic, olderMode)
	registerOK[v1.DeleteModpackRequest, v1.DeleteModpackResponse](mux, "/carbonpanel.v1.ModpackService/DeleteModpack", ic, olderMode)
	registerOK[v1.GetIndexerStatusRequest, v1.GetIndexerStatusResponse](mux, "/carbonpanel.v1.ModpackService/GetIndexerStatus", ic, olderMode)
	registerOK[v1.GetModpackRequest, v1.GetModpackResponse](mux, "/carbonpanel.v1.ModpackService/GetModpack", ic, olderMode)
	registerOK[v1.GetModpackBySlugRequest, v1.GetModpackBySlugResponse](mux, "/carbonpanel.v1.ModpackService/GetModpackBySlug", ic, olderMode)
	registerOK[v1.GetModpackByURLRequest, v1.GetModpackByURLResponse](mux, "/carbonpanel.v1.ModpackService/GetModpackByURL", ic, olderMode)
	registerOK[v1.GetModpackConfigRequest, v1.GetModpackConfigResponse](mux, "/carbonpanel.v1.ModpackService/GetModpackConfig", ic, olderMode)
	registerOK[v1.GetModpackFilesRequest, v1.GetModpackFilesResponse](mux, "/carbonpanel.v1.ModpackService/GetModpackFiles", ic, olderMode)
	registerOK[v1.GetModpackVersionsRequest, v1.GetModpackVersionsResponse](mux, "/carbonpanel.v1.ModpackService/GetModpackVersions", ic, olderMode)
	registerOK[v1.ImportRemoteModpackRequest, v1.ImportRemoteModpackResponse](mux, "/carbonpanel.v1.ModpackService/ImportRemoteModpack", ic, olderMode)
	registerOK[v1.ImportUploadedModpackRequest, v1.ImportUploadedModpackResponse](mux, "/carbonpanel.v1.ModpackService/ImportUploadedModpack", ic, olderMode)
	registerOK[v1.ListFavoritesRequest, v1.ListFavoritesResponse](mux, "/carbonpanel.v1.ModpackService/ListFavorites", ic, olderMode)
	registerOK[v1.SearchModpacksRequest, v1.SearchModpacksResponse](mux, "/carbonpanel.v1.ModpackService/SearchModpacks", ic, olderMode)
	registerOK[v1.SyncModpackFilesRequest, v1.SyncModpackFilesResponse](mux, "/carbonpanel.v1.ModpackService/SyncModpackFiles", ic, olderMode)
	registerOK[v1.SyncModpacksRequest, v1.SyncModpacksResponse](mux, "/carbonpanel.v1.ModpackService/SyncModpacks", ic, olderMode)
	registerOK[v1.ToggleFavoriteRequest, v1.ToggleFavoriteResponse](mux, "/carbonpanel.v1.ModpackService/ToggleFavorite", ic, olderMode)
	registerOK[v1.CreateModuleRequest, v1.CreateModuleResponse](mux, "/carbonpanel.v1.ModuleService/CreateModule", ic, olderMode)
	registerOK[v1.CreateModuleTemplateRequest, v1.CreateModuleTemplateResponse](mux, "/carbonpanel.v1.ModuleService/CreateModuleTemplate", ic, olderMode)
	registerOK[v1.DeleteModuleRequest, v1.DeleteModuleResponse](mux, "/carbonpanel.v1.ModuleService/DeleteModule", ic, olderMode)
	registerOK[v1.DeleteModuleTemplateRequest, v1.DeleteModuleTemplateResponse](mux, "/carbonpanel.v1.ModuleService/DeleteModuleTemplate", ic, olderMode)
	registerOK[v1.GetAvailableAliasesRequest, v1.GetAvailableAliasesResponse](mux, "/carbonpanel.v1.ModuleService/GetAvailableAliases", ic, olderMode)
	registerOK[v1.GetModuleRequest, v1.GetModuleResponse](mux, "/carbonpanel.v1.ModuleService/GetModule", ic, olderMode)
	registerOK[v1.GetModuleLogsRequest, v1.GetModuleLogsResponse](mux, "/carbonpanel.v1.ModuleService/GetModuleLogs", ic, olderMode)
	registerOK[v1.GetModuleTemplateRequest, v1.GetModuleTemplateResponse](mux, "/carbonpanel.v1.ModuleService/GetModuleTemplate", ic, olderMode)
	registerOK[v1.GetNextAvailableModulePortRequest, v1.GetNextAvailableModulePortResponse](mux, "/carbonpanel.v1.ModuleService/GetNextAvailableModulePort", ic, olderMode)
	registerOK[v1.GetResolvedAliasesRequest, v1.GetResolvedAliasesResponse](mux, "/carbonpanel.v1.ModuleService/GetResolvedAliases", ic, olderMode)
	registerOK[v1.ListModuleTemplatesRequest, v1.ListModuleTemplatesResponse](mux, "/carbonpanel.v1.ModuleService/ListModuleTemplates", ic, olderMode)
	registerOK[v1.ListModulesRequest, v1.ListModulesResponse](mux, "/carbonpanel.v1.ModuleService/ListModules", ic, olderMode)
	registerOK[v1.RecreateModuleRequest, v1.RecreateModuleResponse](mux, "/carbonpanel.v1.ModuleService/RecreateModule", ic, olderMode)
	registerOK[v1.RestartModuleRequest, v1.RestartModuleResponse](mux, "/carbonpanel.v1.ModuleService/RestartModule", ic, olderMode)
	registerOK[v1.StartModuleRequest, v1.StartModuleResponse](mux, "/carbonpanel.v1.ModuleService/StartModule", ic, olderMode)
	registerOK[v1.StopModuleRequest, v1.StopModuleResponse](mux, "/carbonpanel.v1.ModuleService/StopModule", ic, olderMode)
	registerOK[v1.UpdateModuleRequest, v1.UpdateModuleResponse](mux, "/carbonpanel.v1.ModuleService/UpdateModule", ic, olderMode)
	registerOK[v1.UpdateModuleTemplateRequest, v1.UpdateModuleTemplateResponse](mux, "/carbonpanel.v1.ModuleService/UpdateModuleTemplate", ic, olderMode)
	registerOK[v1.GetNodeRequest, v1.GetNodeResponse](mux, "/carbonpanel.v1.NodeService/GetNode", ic, olderMode)
	registerOK[v1.PingNodeRequest, v1.PingNodeResponse](mux, "/carbonpanel.v1.NodeService/PingNode", ic, olderMode)
	registerOK[v1.GetOperationRequest, v1.GetOperationResponse](mux, "/carbonpanel.v1.OperationService/GetOperation", ic, olderMode)
	registerOK[v1.ListOperationsRequest, v1.ListOperationsResponse](mux, "/carbonpanel.v1.OperationService/ListOperations", ic, olderMode)
	registerOK[v1.ResolveOperationRequest, v1.ResolveOperationResponse](mux, "/carbonpanel.v1.OperationService/ResolveOperation", ic, olderMode)
	registerOK[v1.BanPlayerRequest, v1.BanPlayerResponse](mux, "/carbonpanel.v1.PlayerService/BanPlayer", ic, olderMode)
	registerOK[v1.BulkPlayerActionRequest, v1.BulkPlayerActionResponse](mux, "/carbonpanel.v1.PlayerService/BulkPlayerAction", ic, olderMode)
	registerOK[v1.GetPlayerDeepDataRequest, v1.GetPlayerDeepDataResponse](mux, "/carbonpanel.v1.PlayerService/GetPlayerDeepData", ic, olderMode)
	registerOK[v1.KickPlayerRequest, v1.KickPlayerResponse](mux, "/carbonpanel.v1.PlayerService/KickPlayer", ic, olderMode)
	registerOK[v1.ListPlayerAccessRequest, v1.ListPlayerAccessResponse](mux, "/carbonpanel.v1.PlayerService/ListPlayerAccess", ic, olderMode)
	registerOK[v1.ListPlayerHistoryRequest, v1.ListPlayerHistoryResponse](mux, "/carbonpanel.v1.PlayerService/ListPlayerHistory", ic, olderMode)
	registerOK[v1.ListPlayerSessionsRequest, v1.ListPlayerSessionsResponse](mux, "/carbonpanel.v1.PlayerService/ListPlayerSessions", ic, olderMode)
	registerOK[v1.PardonPlayerRequest, v1.PardonPlayerResponse](mux, "/carbonpanel.v1.PlayerService/PardonPlayer", ic, olderMode)
	registerOK[v1.RefreshPlayerSkinRequest, v1.RefreshPlayerSkinResponse](mux, "/carbonpanel.v1.PlayerService/RefreshPlayerSkin", ic, olderMode)
	registerOK[v1.SetOperatorRequest, v1.SetOperatorResponse](mux, "/carbonpanel.v1.PlayerService/SetOperator", ic, olderMode)
	registerOK[v1.SetWhitelistEntryRequest, v1.SetWhitelistEntryResponse](mux, "/carbonpanel.v1.PlayerService/SetWhitelistEntry", ic, olderMode)
	registerOK[v1.UndoPlayerActionRequest, v1.UndoPlayerActionResponse](mux, "/carbonpanel.v1.PlayerService/UndoPlayerAction", ic, olderMode)
	registerOK[v1.CreateProxyListenerRequest, v1.CreateProxyListenerResponse](mux, "/carbonpanel.v1.ProxyService/CreateProxyListener", ic, olderMode)
	registerOK[v1.DeleteProxyListenerRequest, v1.DeleteProxyListenerResponse](mux, "/carbonpanel.v1.ProxyService/DeleteProxyListener", ic, olderMode)
	registerOK[v1.GetProxyListenersRequest, v1.GetProxyListenersResponse](mux, "/carbonpanel.v1.ProxyService/GetProxyListeners", ic, olderMode)
	registerOK[v1.GetProxyRoutesRequest, v1.GetProxyRoutesResponse](mux, "/carbonpanel.v1.ProxyService/GetProxyRoutes", ic, olderMode)
	registerOK[v1.GetProxyStatusRequest, v1.GetProxyStatusResponse](mux, "/carbonpanel.v1.ProxyService/GetProxyStatus", ic, olderMode)
	registerOK[v1.GetServerRoutingRequest, v1.GetServerRoutingResponse](mux, "/carbonpanel.v1.ProxyService/GetServerRouting", ic, olderMode)
	registerOK[v1.UpdateProxyConfigRequest, v1.UpdateProxyConfigResponse](mux, "/carbonpanel.v1.ProxyService/UpdateProxyConfig", ic, olderMode)
	registerOK[v1.UpdateProxyListenerRequest, v1.UpdateProxyListenerResponse](mux, "/carbonpanel.v1.ProxyService/UpdateProxyListener", ic, olderMode)
	registerOK[v1.UpdateServerRoutingRequest, v1.UpdateServerRoutingResponse](mux, "/carbonpanel.v1.ProxyService/UpdateServerRouting", ic, olderMode)
	registerOK[v1.AssignRoleRequest, v1.AssignRoleResponse](mux, "/carbonpanel.v1.RoleService/AssignRole", ic, olderMode)
	registerOK[v1.CreateRoleRequest, v1.CreateRoleResponse](mux, "/carbonpanel.v1.RoleService/CreateRole", ic, olderMode)
	registerOK[v1.DeleteRoleRequest, v1.DeleteRoleResponse](mux, "/carbonpanel.v1.RoleService/DeleteRole", ic, olderMode)
	registerOK[v1.GetPermissionMatrixRequest, v1.GetPermissionMatrixResponse](mux, "/carbonpanel.v1.RoleService/GetPermissionMatrix", ic, olderMode)
	registerOK[v1.GetRoleRequest, v1.GetRoleResponse](mux, "/carbonpanel.v1.RoleService/GetRole", ic, olderMode)
	registerOK[v1.GetUserRolesRequest, v1.GetUserRolesResponse](mux, "/carbonpanel.v1.RoleService/GetUserRoles", ic, olderMode)
	registerOK[v1.ListRolesRequest, v1.ListRolesResponse](mux, "/carbonpanel.v1.RoleService/ListRoles", ic, olderMode)
	registerOK[v1.UnassignRoleRequest, v1.UnassignRoleResponse](mux, "/carbonpanel.v1.RoleService/UnassignRole", ic, olderMode)
	registerOK[v1.UpdatePermissionsRequest, v1.UpdatePermissionsResponse](mux, "/carbonpanel.v1.RoleService/UpdatePermissions", ic, olderMode)
	registerOK[v1.UpdateRoleRequest, v1.UpdateRoleResponse](mux, "/carbonpanel.v1.RoleService/UpdateRole", ic, olderMode)
	registerOK[v1.ClearServerLogsRequest, v1.ClearServerLogsResponse](mux, "/carbonpanel.v1.ServerService/ClearServerLogs", ic, olderMode)
	registerOK[v1.ExportServerArchiveRequest, v1.ExportServerArchiveResponse](mux, "/carbonpanel.v1.ServerService/ExportServerArchive", ic, olderMode)
	registerOK[v1.PreviewServerArchiveRequest, v1.PreviewServerArchiveResponse](mux, "/carbonpanel.v1.ServerService/PreviewServerArchive", ic, olderMode)
	registerOK[v1.CreateServerRequest, v1.CreateServerResponse](mux, "/carbonpanel.v1.ServerService/CreateServer", ic, olderMode)
	registerOK[v1.DeleteServerRequest, v1.DeleteServerResponse](mux, "/carbonpanel.v1.ServerService/DeleteServer", ic, olderMode)
	registerOK[v1.GetNextAvailablePortRequest, v1.GetNextAvailablePortResponse](mux, "/carbonpanel.v1.ServerService/GetNextAvailablePort", ic, olderMode)
	registerOK[v1.GetServerLogsRequest, v1.GetServerLogsResponse](mux, "/carbonpanel.v1.ServerService/GetServerLogs", ic, olderMode)
	registerOK[v1.ListServerPlayersRequest, v1.ListServerPlayersResponse](mux, "/carbonpanel.v1.ServerService/ListServerPlayers", ic, olderMode)
	registerOK[v1.MigrateServerRequest, v1.MigrateServerResponse](mux, "/carbonpanel.v1.ServerService/MigrateServer", ic, olderMode)
	registerOK[v1.RecreateServerRequest, v1.RecreateServerResponse](mux, "/carbonpanel.v1.ServerService/RecreateServer", ic, olderMode)
	registerOK[v1.UpdateServerRequest, v1.UpdateServerResponse](mux, "/carbonpanel.v1.ServerService/UpdateServer", ic, olderMode)
	registerOK[v1.ListSubusersRequest, v1.ListSubusersResponse](mux, "/carbonpanel.v1.SubuserService/ListSubusers", ic, olderMode)
	registerOK[v1.RemoveSubuserRequest, v1.RemoveSubuserResponse](mux, "/carbonpanel.v1.SubuserService/RemoveSubuser", ic, olderMode)
	registerOK[v1.SetSubuserRequest, v1.SetSubuserResponse](mux, "/carbonpanel.v1.SubuserService/SetSubuser", ic, olderMode)
	registerOK[v1.GetApplicationLogsRequest, v1.GetApplicationLogsResponse](mux, "/carbonpanel.v1.SupportService/GetApplicationLogs", ic, olderMode)
	registerOK[v1.CancelExecutionRequest, v1.CancelExecutionResponse](mux, "/carbonpanel.v1.TaskService/CancelExecution", ic, olderMode)
	registerOK[v1.CreateTaskRequest, v1.CreateTaskResponse](mux, "/carbonpanel.v1.TaskService/CreateTask", ic, olderMode)
	registerOK[v1.DeleteTaskRequest, v1.DeleteTaskResponse](mux, "/carbonpanel.v1.TaskService/DeleteTask", ic, olderMode)
	registerOK[v1.GetSchedulerStatusRequest, v1.GetSchedulerStatusResponse](mux, "/carbonpanel.v1.TaskService/GetSchedulerStatus", ic, olderMode)
	registerOK[v1.GetTaskRequest, v1.GetTaskResponse](mux, "/carbonpanel.v1.TaskService/GetTask", ic, olderMode)
	registerOK[v1.GetTaskExecutionRequest, v1.GetTaskExecutionResponse](mux, "/carbonpanel.v1.TaskService/GetTaskExecution", ic, olderMode)
	registerOK[v1.ListServerExecutionsRequest, v1.ListServerExecutionsResponse](mux, "/carbonpanel.v1.TaskService/ListServerExecutions", ic, olderMode)
	registerOK[v1.ListTaskExecutionsRequest, v1.ListTaskExecutionsResponse](mux, "/carbonpanel.v1.TaskService/ListTaskExecutions", ic, olderMode)
	registerOK[v1.ListTasksRequest, v1.ListTasksResponse](mux, "/carbonpanel.v1.TaskService/ListTasks", ic, olderMode)
	registerOK[v1.ToggleTaskRequest, v1.ToggleTaskResponse](mux, "/carbonpanel.v1.TaskService/ToggleTask", ic, olderMode)
	registerOK[v1.TriggerTaskRequest, v1.TriggerTaskResponse](mux, "/carbonpanel.v1.TaskService/TriggerTask", ic, olderMode)
	registerOK[v1.UpdateTaskRequest, v1.UpdateTaskResponse](mux, "/carbonpanel.v1.TaskService/UpdateTask", ic, olderMode)
	registerOK[v1.CancelUploadRequest, v1.CancelUploadResponse](mux, "/carbonpanel.v1.UploadService/CancelUpload", ic, olderMode)
	registerOK[v1.GetUploadStatusRequest, v1.GetUploadStatusResponse](mux, "/carbonpanel.v1.UploadService/GetUploadStatus", ic, olderMode)
	registerOK[v1.InitUploadRequest, v1.InitUploadResponse](mux, "/carbonpanel.v1.UploadService/InitUpload", ic, olderMode)
	registerOK[v1.UploadChunkRequest, v1.UploadChunkResponse](mux, "/carbonpanel.v1.UploadService/UploadChunk", ic, olderMode)
	registerOK[v1.CreateUserRequest, v1.CreateUserResponse](mux, "/carbonpanel.v1.UserService/CreateUser", ic, olderMode)
	registerOK[v1.DeleteUserRequest, v1.DeleteUserResponse](mux, "/carbonpanel.v1.UserService/DeleteUser", ic, olderMode)
	registerOK[v1.GetUserRequest, v1.GetUserResponse](mux, "/carbonpanel.v1.UserService/GetUser", ic, olderMode)
	registerOK[v1.ListUsersRequest, v1.ListUsersResponse](mux, "/carbonpanel.v1.UserService/ListUsers", ic, olderMode)
	registerOK[v1.UpdateUserRequest, v1.UpdateUserResponse](mux, "/carbonpanel.v1.UserService/UpdateUser", ic, olderMode)
	registerOK[v1.BindWorldRequest, v1.BindWorldResponse](mux, "/carbonpanel.v1.WorldService/BindWorld", ic, olderMode)
	registerOK[v1.CreateWorldFromConfigRequest, v1.CreateWorldFromConfigResponse](mux, "/carbonpanel.v1.WorldService/CreateWorldFromConfig", ic, olderMode)
	registerOK[v1.ImportWorldRequest, v1.ImportWorldResponse](mux, "/carbonpanel.v1.WorldService/ImportWorld", ic, olderMode)
	registerOK[v1.ResolveWorldOperationRequest, v1.ResolveWorldOperationResponse](mux, "/carbonpanel.v1.WorldService/ResolveWorldOperation", ic, olderMode)
	registerOK[v1.UnbindWorldRequest, v1.UnbindWorldResponse](mux, "/carbonpanel.v1.WorldService/UnbindWorld", ic, olderMode)
	registerOK[v1.GetWorldViewSessionRequest, v1.GetWorldViewSessionResponse](mux, "/carbonpanel.v1.WorldViewService/GetWorldViewSession", ic, olderMode)
	registerOK[v1.SetMapOverlayRequest, v1.SetMapOverlayResponse](mux, "/carbonpanel.v1.WorldViewService/SetMapOverlay", ic, olderMode)
}

func connectStdioSession(t *testing.T, server *sdkmcp.Server, ctx context.Context) *sdkmcp.ClientSession {
	t.Helper()
	// io.Pipe pairs reproduce the stdio wire format (newline-delimited JSON)
	// without touching the test process's os.Stdin/os.Stdout.
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	serverTransport := &sdkmcp.IOTransport{Reader: serverReader, Writer: serverWriter}
	clientTransport := &sdkmcp.IOTransport{Reader: clientReader, Writer: clientWriter}
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "mcp-test-client", Version: "v0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	})
	return clientSession
}

func newFixture(t *testing.T, olderMode string, authEnabled bool) *fixture {
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
	registerToolHandlers(mux, interceptor, olderMode)
	registerCombinedToolHandlers(mux, interceptor, olderMode)
	registerPackwizReadFixture(t, mux, interceptor, olderMode)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	f := &fixture{
		sessions: make(map[string]*sdkmcp.ClientSession),
		tokens:   make(map[string]string),
		server:   ts,
	}
	f.tokens["admin"] = seedToken(t, store, enforcer, "u-admin", "admin", nil)
	serverPerms := append(globalPerms("servers", "read", "start", "stop", "restart"),
		append(globalPerms("files", "read"), globalPerms("backups", "read")...)...)
	f.tokens["serverReader"] = seedToken(t, store, enforcer, "u-server-reader", "server-reader", serverPerms)
	f.tokens["worldReader"] = seedToken(t, store, enforcer, "u-world-reader", "world-reader",
		append(globalPerms("players", "read"),
			append(globalPerms("worlds", "read"), globalPerms("world_view", "read")...)...))
	// noperms is a real, active dp_ token whose role grants nothing. It is
	// used by executable coverage evidence to prove resource-scope denial.
	f.tokens["noperms"] = seedToken(t, store, enforcer, "u-noperms", "noperms", nil)
	f.tokens["invalid"] = "dp_nonexistent_token"

	for name, token := range f.tokens {
		clients, err := NewClients(ts.Client(), ts.URL, token)
		if err != nil {
			t.Fatalf("NewClients(%s): %v", name, err)
		}
		if name != "invalid" {
			err = clients.Verify(ctx)
			if authEnabled && err != nil {
				t.Fatalf("Verify(%s): %v", name, err)
			}
			if !authEnabled {
				f.verifyErr = err
			}
		}
		f.sessions[name] = connectStdioSession(t, NewServer(clients), ctx)
	}
	return f
}

func callTool(t *testing.T, session *sdkmcp.ClientSession, name string, args map[string]any) ToolOutput {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) protocol error: %v", name, err)
	}
	if res.StructuredContent == nil {
		t.Fatalf("CallTool(%s) returned no structured content: %+v", name, res)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("CallTool(%s) marshal structured content: %v", name, err)
	}
	var out ToolOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("CallTool(%s) structured content: %v", name, err)
	}
	if res.IsError != (out.Status == "error") {
		t.Fatalf("CallTool(%s) IsError=%v but status=%q", name, res.IsError, out.Status)
	}
	return out
}

func assertSuccess(t *testing.T, out ToolOutput, tool string) {
	t.Helper()
	if out.Status != "ok" || out.Error != nil {
		t.Fatalf("%s: expected success, got %+v", tool, out)
	}
}

func assertStructuredError(t *testing.T, out ToolOutput, tool, kind string) *ToolError {
	t.Helper()
	if out.Status != "error" || out.Error == nil {
		t.Fatalf("%s: expected structured error, got %+v", tool, out)
	}
	if out.Error.Kind != kind {
		t.Fatalf("%s: error kind %q, want %q (%s)", tool, out.Error.Kind, kind, out.Error.Message)
	}
	return out.Error
}

type familyCase struct {
	family string
	tool   string
	args   map[string]any
	allow  string
	deny   string
}

func familyCases() []familyCase {
	return []familyCase{
		{"server", "get_server", map[string]any{"id": "srv-1"}, "serverReader", "worldReader"},
		{"player", "list_players", map[string]any{"server_id": "srv-1", "limit": 10}, "worldReader", "serverReader"},
		{"world", "list_world_entries", map[string]any{"limit": 10}, "worldReader", "serverReader"},
		{"worldview", "list_world_view_targets", map[string]any{}, "worldReader", "serverReader"},
		{"backup", "list_backups", map[string]any{"server_id": "srv-1"}, "serverReader", "worldReader"},
		{"file", "list_files", map[string]any{"server_id": "srv-1"}, "serverReader", "worldReader"},
	}
}

func TestToolFamiliesSuccessAndStructuredAuthErrors(t *testing.T) {
	f := newFixture(t, "", true)
	for _, tc := range familyCases() {
		t.Run(tc.family+"/success", func(t *testing.T) {
			assertSuccess(t, callTool(t, f.sessions[tc.allow], tc.tool, tc.args), tc.tool)
		})
		t.Run(tc.family+"/permission_denied", func(t *testing.T) {
			e := assertStructuredError(t, callTool(t, f.sessions[tc.deny], tc.tool, tc.args), tc.tool, ErrKindPermissionDenied)
			if e.Tool != tc.tool || e.Procedure == "" || e.Code != "permission_denied" {
				t.Fatalf("%s: incomplete structured error: %+v", tc.tool, e)
			}
		})
		t.Run(tc.family+"/unauthenticated", func(t *testing.T) {
			e := assertStructuredError(t, callTool(t, f.sessions["invalid"], tc.tool, tc.args), tc.tool, ErrKindUnauthenticated)
			if e.Code != "unauthenticated" {
				t.Fatalf("%s: code %q", tc.tool, e.Code)
			}
		})
	}
}

func TestStableBuildSmokeAndCapabilityErrors(t *testing.T) {
	for _, olderMode := range []string{"unimplemented", "transport-notfound"} {
		t.Run(olderMode, func(t *testing.T) {
			f := newFixture(t, olderMode, true)
			// The stable smoke path succeeds against the older panel.
			for _, tool := range SmokeTools {
				args := map[string]any{}
				switch tool {
				case "get_server":
					args["id"] = "srv-1"
				case "list_files":
					args["server_id"] = "srv-1"
				case "get_file":
					args["server_id"] = "srv-1"
					args["path"] = "server.properties"
				case "list_backups":
					args["server_id"] = "srv-1"
				}
				assertSuccess(t, callTool(t, f.sessions["admin"], tool, args), tool)
			}
			// A post-v1.0.40 tool returns an honest structured capability
			// error, while staying present in the catalog.
			e := assertStructuredError(t, callTool(t, f.sessions["admin"], "list_players",
				map[string]any{"server_id": "srv-1", "limit": 10}), "list_players", ErrKindCapability)
			wantCode := "unimplemented"
			if olderMode == "transport-notfound" {
				wantCode = "not_found"
			}
			if e.Code != wantCode {
				t.Fatalf("capability error code %q, want %q", e.Code, wantCode)
			}
			if e.MinimumPanelBuild != string(PanelBuildPostStable) {
				t.Fatalf("minimum build %q", e.MinimumPanelBuild)
			}
			if !strings.Contains(e.Message, "/carbonpanel.v1.PlayerService/ListPlayers") || !strings.Contains(e.Message, "panel build") {
				t.Fatalf("capability message lacks procedure/build: %q", e.Message)
			}
			if _, ok := Declared("list_players"); !ok {
				t.Fatal("tool was hidden from the catalog on an older panel")
			}
			staged := assertStructuredError(t, callTool(t, f.sessions["admin"], stagedConfigListSpec.Name,
				map[string]any{"server_id": "srv-1"}), stagedConfigListSpec.Name, ErrKindCapability)
			if staged.Code != wantCode || staged.MinimumPanelBuild != string(PanelBuildProductRPCs) {
				t.Fatalf("staged changes capability error: %+v", staged)
			}
			// The catalog is identical: all registered tools are still listed.
			tools, err := f.sessions["admin"].ListTools(context.Background(), nil)
			if err != nil {
				t.Fatalf("ListTools: %v", err)
			}
			if len(tools.Tools) != len(exposedTools) {
				t.Fatalf("tool list changed: got %d, want %d", len(tools.Tools), len(exposedTools))
			}
		})
	}
}

func TestDomainNotFoundIsNotACapabilityError(t *testing.T) {
	f := newFixture(t, "domain-notfound", true)
	e := assertStructuredError(t, callTool(t, f.sessions["admin"], "get_server",
		map[string]any{"id": "srv-missing"}), "get_server", ErrKindNotFound)
	if strings.Contains(e.Message, "panel build") || strings.Contains(e.Message, "upgrade") ||
		e.MinimumPanelBuild != "" {
		t.Fatalf("domain not-found was reported as an upgrade requirement: %+v", e)
	}
	if !strings.Contains(e.Message, "does not exist") || e.Code != "not_found" {
		t.Fatalf("domain not-found message is not honest: %+v", e)
	}
}

func TestNoAuthPanelIsRefusedEvenWithWellFormedToken(t *testing.T) {
	f := newFixture(t, "", false)
	if f.verifyErr == nil {
		t.Fatal("no-auth panel was accepted with a well-formed dp_ token")
	}
	if !strings.Contains(f.verifyErr.Error(), "no authentication provider") {
		t.Fatalf("unexpected refusal reason: %v", f.verifyErr)
	}
}

func TestBoundedBackendTimeout(t *testing.T) {
	if newHTTPClient().Timeout != DefaultRequestTimeout {
		t.Fatal("stdio Run client is not bounded by DefaultRequestTimeout")
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusInternalServerError)
		case <-r.Context().Done():
		}
	}))
	defer ts.Close()

	clients, err := NewClients(&http.Client{Timeout: 150 * time.Millisecond}, ts.URL, "dp_timeout_test")
	if err != nil {
		t.Fatal(err)
	}
	session := connectStdioSession(t, NewServer(clients), context.Background())
	start := time.Now()
	out := callTool(t, session, "list_servers", map[string]any{})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("hung backend held the tool call for %s", elapsed)
	}
	e := assertStructuredError(t, out, "list_servers", ErrKindUpstream)
	if e.Code == "" && !strings.Contains(e.Message, "ListServers") {
		t.Fatalf("timeout error lacks procedure context: %+v", e)
	}
}

func TestConfirmationGateAndBoundedArguments(t *testing.T) {
	f := newFixture(t, "", true)
	_ = assertStructuredError(t, callTool(t, f.sessions["admin"], "restart_server",
		map[string]any{"id": "srv-1"}), "restart_server", ErrKindConfirmationRequired)
	assertSuccess(t, callTool(t, f.sessions["admin"], "restart_server",
		map[string]any{"id": "srv-1", "confirm": true, "request_id": "req-1"}), "restart_server")
	_ = assertStructuredError(t, callTool(t, f.sessions["admin"], "list_players",
		map[string]any{"server_id": "srv-1", "limit": 5000}), "list_players", ErrKindInvalidArguments)
	_ = assertStructuredError(t, callTool(t, f.sessions["admin"], "get_file",
		map[string]any{"server_id": "srv-1", "path": "../secrets"}), "get_file", ErrKindInvalidArguments)
}
