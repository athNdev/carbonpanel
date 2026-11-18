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
)

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
		postStable = e.Build == PanelBuildPostStable
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
		handler = func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error) {
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
		func(context.Context, *connect.Request[v1.GetAuthStatusRequest]) (*connect.Response[v1.GetAuthStatusResponse], error) {
			return connect.NewResponse(&v1.GetAuthStatusResponse{LocalAuthEnabled: authEnabled}), nil
		}, connect.WithInterceptors(ic)))

	const userProc = "/carbonpanel.v1.AuthService/GetCurrentUser"
	mux.Handle(userProc, connect.NewUnaryHandler(userProc,
		func(ctx context.Context, _ *connect.Request[v1.GetCurrentUserRequest]) (*connect.Response[v1.GetCurrentUserResponse], error) {
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
	registerOK[v1.ListServersRequest, v1.ListServersResponse](mux, "/carbonpanel.v1.ServerService/ListServers", ic, olderMode)
	registerOK[v1.GetServerRequest, v1.GetServerResponse](mux, "/carbonpanel.v1.ServerService/GetServer", ic, olderMode)
	registerOK[v1.StartServerRequest, v1.StartServerResponse](mux, "/carbonpanel.v1.ServerService/StartServer", ic, olderMode)
	registerOK[v1.StopServerRequest, v1.StopServerResponse](mux, "/carbonpanel.v1.ServerService/StopServer", ic, olderMode)
	registerOK[v1.RestartServerRequest, v1.RestartServerResponse](mux, "/carbonpanel.v1.ServerService/RestartServer", ic, olderMode)

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
	assertStructuredError(t, callTool(t, f.sessions["admin"], "restart_server",
		map[string]any{"id": "srv-1"}), "restart_server", ErrKindConfirmationRequired)
	assertSuccess(t, callTool(t, f.sessions["admin"], "restart_server",
		map[string]any{"id": "srv-1", "confirm": true, "request_id": "req-1"}), "restart_server")
	assertStructuredError(t, callTool(t, f.sessions["admin"], "list_players",
		map[string]any{"server_id": "srv-1", "limit": 5000}), "list_players", ErrKindInvalidArguments)
	assertStructuredError(t, callTool(t, f.sessions["admin"], "get_file",
		map[string]any{"server_id": "srv-1", "path": "../secrets"}), "get_file", ErrKindInvalidArguments)
}
