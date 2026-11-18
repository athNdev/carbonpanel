// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

// Executable conformance evidence for the OSS-371 family tools. Unlike the
// declaration tests, every case here drives a real MCP SDK client over the
// stdio wire format into a typed Connect handler behind the production auth
// interceptor. Each handler records the exact request message it received and
// returns a typed response carrying a per-procedure marker, so a passing case
// proves both that the intended typed method was reached and that its mapped
// request fields were bounded as declared.

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
	"google.golang.org/protobuf/reflect/protoregistry"
)

type confBehavior int

const (
	confNormal confBehavior = iota
	confNotFound
	confUnimplemented
	confUnavailable
)

type confCapture struct {
	procedure   string
	requestJSON string
	user        string
}

type confRecorder struct {
	mu    sync.Mutex
	calls map[string][]confCapture
}

func newConfRecorder() *confRecorder {
	return &confRecorder{calls: make(map[string][]confCapture)}
}

func (r *confRecorder) record(procedure string, msg any, u *auth.AuthenticatedUser) {
	var payload []byte
	if pm, ok := msg.(proto.Message); ok {
		payload, _ = protojson.MarshalOptions{UseProtoNames: true}.Marshal(pm)
	}
	name := ""
	if u != nil {
		name = u.Username
	}
	r.mu.Lock()
	r.calls[procedure] = append(r.calls[procedure], confCapture{procedure, string(payload), name})
	r.mu.Unlock()
}

func (r *confRecorder) reset() {
	r.mu.Lock()
	r.calls = make(map[string][]confCapture)
	r.mu.Unlock()
}

func (r *confRecorder) snapshot() map[string][]confCapture {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string][]confCapture, len(r.calls))
	for k, v := range r.calls {
		out[k] = append([]confCapture(nil), v...)
	}
	return out
}

func (r *confRecorder) count(procedure string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls[procedure])
}

func (r *confRecorder) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, v := range r.calls {
		n += len(v)
	}
	return n
}

// confSetFirstStringField stamps a procedure-specific marker into the typed
// response. The client cannot produce this value; seeing it in tool output
// proves the real handler response was returned end to end.
func confSetFirstStringField(m proto.Message, value string) {
	ref := m.ProtoReflect()
	fields := ref.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if f.Kind() == protoreflect.StringKind && f.Cardinality() != protoreflect.Repeated {
			ref.Set(f, protoreflect.ValueOfString(value))
			return
		}
	}
}

func confResponseHasStringField(procedure string) bool {
	idx := strings.LastIndex(procedure, "/")
	if idx <= 1 {
		return false
	}
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(procedure[1:idx]))
	if err != nil {
		return false
	}
	sd, ok := d.(protoreflect.ServiceDescriptor)
	if !ok {
		return false
	}
	md := sd.Methods().ByName(protoreflect.Name(procedure[idx+1:]))
	if md == nil {
		return false
	}
	fields := md.Output().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if f.Kind() == protoreflect.StringKind && f.Cardinality() != protoreflect.Repeated {
			return true
		}
	}
	return false
}

func confMarker(procedure string) string { return "CONF-HANDLER:" + procedure }

func confRegister[Req, Res any](mux *http.ServeMux, procedure string, ic connect.UnaryInterceptorFunc,
	rec *confRecorder, behavior confBehavior) {
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(ctx context.Context, req *connect.Request[Req]) (*connect.Response[Res], error) {
			rec.record(procedure, req.Msg, auth.GetUserFromContext(ctx))
			switch behavior {
			case confNotFound:
				return nil, connect.NewError(connect.CodeNotFound, errors.New("record does not exist on the controller"))
			case confUnimplemented:
				return nil, connect.NewError(connect.CodeUnimplemented, errors.New("rpc is not implemented by this panel build"))
			case confUnavailable:
				return nil, connect.NewError(connect.CodeUnavailable, errors.New("backend temporarily unavailable"))
			}
			var msg Res
			if pm, ok := any(&msg).(proto.Message); ok {
				confSetFirstStringField(pm, confMarker(procedure))
			}
			return connect.NewResponse(&msg), nil
		}, connect.WithInterceptors(ic)))
}

func confBehaviorFor(overrides map[string]confBehavior, procedure string) confBehavior {
	if b, ok := overrides[procedure]; ok {
		return b
	}
	return confNormal
}

// confRegisterAll mounts every reachable RPC exactly once. There is no route
// for ScanNodes, NodeControlService, health or any REST surface.
func confRegisterAll(mux *http.ServeMux, ic connect.UnaryInterceptorFunc, rec *confRecorder, overrides map[string]confBehavior) {
	b := func(p string) confBehavior { return confBehaviorFor(overrides, p) }
	const srv = "/carbonpanel.v1.ServerService/"
	confRegister[v1.GetServerLogsRequest, v1.GetServerLogsResponse](mux, srv+"GetServerLogs", ic, rec, b(srv+"GetServerLogs"))
	confRegister[v1.ClearServerLogsRequest, v1.ClearServerLogsResponse](mux, srv+"ClearServerLogs", ic, rec, b(srv+"ClearServerLogs"))
	confRegister[v1.GetNextAvailablePortRequest, v1.GetNextAvailablePortResponse](mux, srv+"GetNextAvailablePort", ic, rec, b(srv+"GetNextAvailablePort"))
	confRegister[v1.CreateServerRequest, v1.CreateServerResponse](mux, srv+"CreateServer", ic, rec, b(srv+"CreateServer"))
	confRegister[v1.UpdateServerRequest, v1.UpdateServerResponse](mux, srv+"UpdateServer", ic, rec, b(srv+"UpdateServer"))
	confRegister[v1.DeleteServerRequest, v1.DeleteServerResponse](mux, srv+"DeleteServer", ic, rec, b(srv+"DeleteServer"))
	confRegister[v1.RecreateServerRequest, v1.RecreateServerResponse](mux, srv+"RecreateServer", ic, rec, b(srv+"RecreateServer"))
	confRegister[v1.SendCommandRequest, v1.SendCommandResponse](mux, srv+"SendCommand", ic, rec, b(srv+"SendCommand"))
	confRegister[v1.UploadToMCLogsRequest, v1.UploadToMCLogsResponse](mux, srv+"UploadToMCLogs", ic, rec, b(srv+"UploadToMCLogs"))
	confRegister[v1.MigrateServerRequest, v1.MigrateServerResponse](mux, srv+"MigrateServer", ic, rec, b(srv+"MigrateServer"))
	confRegister[v1.ListServerPlayersRequest, v1.ListServerPlayersResponse](mux, srv+"ListServerPlayers", ic, rec, b(srv+"ListServerPlayers"))

	const cfg = "/carbonpanel.v1.ConfigService/"
	confRegister[v1.GetServerConfigRequest, v1.GetServerConfigResponse](mux, cfg+"GetServerConfig", ic, rec, b(cfg+"GetServerConfig"))
	confRegister[v1.UpdateServerConfigRequest, v1.UpdateServerConfigResponse](mux, cfg+"UpdateServerConfig", ic, rec, b(cfg+"UpdateServerConfig"))
	confRegister[v1.GetUpdateBatchRequest, v1.GetUpdateBatchResponse](mux, cfg+"GetUpdateBatch", ic, rec, b(cfg+"GetUpdateBatch"))
	confRegister[v1.DiscardUpdateBatchRequest, v1.DiscardUpdateBatchResponse](mux, cfg+"DiscardUpdateBatch", ic, rec, b(cfg+"DiscardUpdateBatch"))
	confRegister[v1.GetGlobalSettingsRequest, v1.GetGlobalSettingsResponse](mux, cfg+"GetGlobalSettings", ic, rec, b(cfg+"GetGlobalSettings"))
	confRegister[v1.UpdateGlobalSettingsRequest, v1.UpdateGlobalSettingsResponse](mux, cfg+"UpdateGlobalSettings", ic, rec, b(cfg+"UpdateGlobalSettings"))
	confRegister[v1.SyncGlobalSettingsToServersRequest, v1.SyncGlobalSettingsToServersResponse](mux, cfg+"SyncGlobalSettingsToServers", ic, rec, b(cfg+"SyncGlobalSettingsToServers"))
	confRegister[v1.ValidateProviderKeyRequest, v1.ValidateProviderKeyResponse](mux, cfg+"ValidateProviderKey", ic, rec, b(cfg+"ValidateProviderKey"))

	const task = "/carbonpanel.v1.TaskService/"
	confRegister[v1.ListTasksRequest, v1.ListTasksResponse](mux, task+"ListTasks", ic, rec, b(task+"ListTasks"))
	confRegister[v1.GetTaskRequest, v1.GetTaskResponse](mux, task+"GetTask", ic, rec, b(task+"GetTask"))
	confRegister[v1.CreateTaskRequest, v1.CreateTaskResponse](mux, task+"CreateTask", ic, rec, b(task+"CreateTask"))
	confRegister[v1.UpdateTaskRequest, v1.UpdateTaskResponse](mux, task+"UpdateTask", ic, rec, b(task+"UpdateTask"))
	confRegister[v1.DeleteTaskRequest, v1.DeleteTaskResponse](mux, task+"DeleteTask", ic, rec, b(task+"DeleteTask"))
	confRegister[v1.ToggleTaskRequest, v1.ToggleTaskResponse](mux, task+"ToggleTask", ic, rec, b(task+"ToggleTask"))
	confRegister[v1.TriggerTaskRequest, v1.TriggerTaskResponse](mux, task+"TriggerTask", ic, rec, b(task+"TriggerTask"))
	confRegister[v1.ListTaskExecutionsRequest, v1.ListTaskExecutionsResponse](mux, task+"ListTaskExecutions", ic, rec, b(task+"ListTaskExecutions"))
	confRegister[v1.ListServerExecutionsRequest, v1.ListServerExecutionsResponse](mux, task+"ListServerExecutions", ic, rec, b(task+"ListServerExecutions"))
	confRegister[v1.GetTaskExecutionRequest, v1.GetTaskExecutionResponse](mux, task+"GetTaskExecution", ic, rec, b(task+"GetTaskExecution"))
	confRegister[v1.CancelExecutionRequest, v1.CancelExecutionResponse](mux, task+"CancelExecution", ic, rec, b(task+"CancelExecution"))
	confRegister[v1.GetSchedulerStatusRequest, v1.GetSchedulerStatusResponse](mux, task+"GetSchedulerStatus", ic, rec, b(task+"GetSchedulerStatus"))

	const act = "/carbonpanel.v1.ActivityService/"
	confRegister[v1.ListActivityLogsRequest, v1.ListActivityLogsResponse](mux, act+"ListActivityLogs", ic, rec, b(act+"ListActivityLogs"))
	const op = "/carbonpanel.v1.OperationService/"
	confRegister[v1.ListOperationsRequest, v1.ListOperationsResponse](mux, op+"ListOperations", ic, rec, b(op+"ListOperations"))
	confRegister[v1.GetOperationRequest, v1.GetOperationResponse](mux, op+"GetOperation", ic, rec, b(op+"GetOperation"))
	confRegister[v1.ResolveOperationRequest, v1.ResolveOperationResponse](mux, op+"ResolveOperation", ic, rec, b(op+"ResolveOperation"))
	const bp = "/carbonpanel.v1.BlueprintService/"
	confRegister[v1.ListBlueprintsRequest, v1.ListBlueprintsResponse](mux, bp+"ListBlueprints", ic, rec, b(bp+"ListBlueprints"))
	confRegister[v1.GetBlueprintRequest, v1.GetBlueprintResponse](mux, bp+"GetBlueprint", ic, rec, b(bp+"GetBlueprint"))
	const mc = "/carbonpanel.v1.MinecraftService/"
	confRegister[v1.GetMinecraftVersionsRequest, v1.GetMinecraftVersionsResponse](mux, mc+"GetMinecraftVersions", ic, rec, b(mc+"GetMinecraftVersions"))
	confRegister[v1.GetModLoadersRequest, v1.GetModLoadersResponse](mux, mc+"GetModLoaders", ic, rec, b(mc+"GetModLoaders"))
	confRegister[v1.GetDockerImagesRequest, v1.GetDockerImagesResponse](mux, mc+"GetDockerImages", ic, rec, b(mc+"GetDockerImages"))
	confRegister[v1.GetLoaderVersionsRequest, v1.GetLoaderVersionsResponse](mux, mc+"GetLoaderVersions", ic, rec, b(mc+"GetLoaderVersions"))
	const sup = "/carbonpanel.v1.SupportService/"
	confRegister[v1.GetApplicationLogsRequest, v1.GetApplicationLogsResponse](mux, sup+"GetApplicationLogs", ic, rec, b(sup+"GetApplicationLogs"))
	confRegister[v1.GenerateSupportBundleRequest, v1.GenerateSupportBundleResponse](mux, sup+"GenerateSupportBundle", ic, rec, b(sup+"GenerateSupportBundle"))
	confRegister[v1.UploadSupportBundleRequest, v1.UploadSupportBundleResponse](mux, sup+"UploadSupportBundle", ic, rec, b(sup+"UploadSupportBundle"))
	confRegister[v1.DownloadSupportBundleRequest, v1.DownloadSupportBundleResponse](mux, sup+"DownloadSupportBundle", ic, rec, b(sup+"DownloadSupportBundle"))
	const node = "/carbonpanel.v1.NodeService/"
	confRegister[v1.PingNodeRequest, v1.PingNodeResponse](mux, node+"PingNode", ic, rec, b(node+"PingNode"))
	confRegister[v1.GetNodeRequest, v1.GetNodeResponse](mux, node+"GetNode", ic, rec, b(node+"GetNode"))
	confRegister[v1.ListNodesRequest, v1.ListNodesResponse](mux, node+"ListNodes", ic, rec, b(node+"ListNodes"))
	confRegister[v1.CreateNodeRequest, v1.CreateNodeResponse](mux, node+"CreateNode", ic, rec, b(node+"CreateNode"))
	confRegister[v1.UpdateNodeRequest, v1.UpdateNodeResponse](mux, node+"UpdateNode", ic, rec, b(node+"UpdateNode"))
	confRegister[v1.DeleteNodeRequest, v1.DeleteNodeResponse](mux, node+"DeleteNode", ic, rec, b(node+"DeleteNode"))
}

type confFixture struct {
	sessions map[string]*sdkmcp.ClientSession
	clients  map[string]*FamilyServerClients
	recorder *confRecorder
	server   *httptest.Server
}

func confNewStore(t *testing.T) *storage.Store {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_", "\\", "_").Replace(t.Name())
	cfg := &config.Config{Database: config.DatabaseConfig{
		Path: fmt.Sprintf("file:%s?mode=memory&cache=shared", name), AutoMigrate: true, MaxConnections: 5,
	}}
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

func confSeedToken(t *testing.T, store *storage.Store, enforcer *rbac.Enforcer, user, role string, perms []rbac.Permission) string {
	t.Helper()
	ctx := context.Background()
	if err := enforcer.SetPermissionsForRole(role, perms); err != nil {
		t.Fatalf("SetPermissionsForRole(%s): %v", role, err)
	}
	if err := store.CreateUser(ctx, &storage.User{ID: user, Username: user, AuthProvider: "local", IsActive: true}); err != nil {
		t.Fatalf("CreateUser(%s): %v", user, err)
	}
	if err := store.AssignRole(ctx, user, role, "manual"); err != nil {
		t.Fatalf("AssignRole(%s): %v", user, err)
	}
	raw := "dp_" + user + "token"
	sum := sha256.Sum256([]byte(raw))
	if err := store.CreateAPIToken(ctx, &storage.APIToken{UserID: user, Name: "conf", TokenHash: hex.EncodeToString(sum[:])}); err != nil {
		t.Fatalf("CreateAPIToken(%s): %v", user, err)
	}
	return raw
}

func confGlobal(resource string, actions ...string) []rbac.Permission {
	out := make([]rbac.Permission, 0, len(actions))
	for _, a := range actions {
		out = append(out, rbac.Permission{Resource: resource, Action: a, ObjectID: "*"})
	}
	return out
}

func confScoped(resource string, objectID string, actions ...string) []rbac.Permission {
	out := make([]rbac.Permission, 0, len(actions))
	for _, a := range actions {
		out = append(out, rbac.Permission{Resource: resource, Action: a, ObjectID: objectID})
	}
	return out
}

func confSession(t *testing.T, server *sdkmcp.Server) *sdkmcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	ss, err := server.Connect(ctx, &sdkmcp.IOTransport{Reader: sr, Writer: sw}, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "conf-client", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, &sdkmcp.IOTransport{Reader: cr, Writer: cw}, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })
	return cs
}

func confCall(t *testing.T, session *sdkmcp.ClientSession, tool string, args map[string]any) ToolOutput {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) protocol error: %v", tool, err)
	}
	if res.StructuredContent == nil {
		t.Fatalf("CallTool(%s) returned no structured content: %+v", tool, res)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out ToolOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if res.IsError != (out.Status == "error") {
		t.Fatalf("CallTool(%s) IsError=%v status=%q", tool, res.IsError, out.Status)
	}
	return out
}

func confNewFixture(t *testing.T, overrides map[string]confBehavior) *confFixture {
	t.Helper()
	store := confNewStore(t)
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatalf("SeedDefaultPolicies: %v", err)
	}
	manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
		Local: config.LocalConfig{Enabled: true}, OIDC: config.OIDCConfig{Enabled: false},
		AnonymousAccess: false, AllowNoAuth: false,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ic := rpc.NewAuthInterceptor(manager, enforcer, store, logger.New())
	rec := newConfRecorder()
	mux := http.NewServeMux()
	registerAuthHandlers(mux, ic, true)
	confRegisterAll(mux, ic, rec, overrides)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	all := append([]rbac.Permission{}, confGlobal("servers", "read", "start", "stop", "restart", "update", "create", "delete", "command")...)
	all = append(all, confGlobal("server_config", "read", "update")...)
	all = append(all, confGlobal("settings", "read", "update")...)
	all = append(all, confGlobal("tasks", "read", "create", "update", "delete")...)
	all = append(all, confGlobal("activity", "read")...)
	all = append(all, confGlobal("support", "read", "create")...)
	all = append(all, confGlobal("nodes", "read", "create", "update", "delete")...)

	scoped := append([]rbac.Permission{}, confScoped("servers", "srv-1", "read", "start", "stop", "restart", "update", "create", "delete", "command")...)
	scoped = append(scoped, confScoped("server_config", "srv-1", "read", "update")...)
	scoped = append(scoped, confScoped("tasks", "srv-1", "read", "create", "update", "delete")...)
	scoped = append(scoped, confScoped("activity", "srv-1", "read")...)
	scoped = append(scoped, confScoped("nodes", "node-1", "read")...)

	tokens := map[string]string{
		"allow":  confSeedToken(t, store, enforcer, "conf-allow", "conf-allow-role", all),
		"deny":   confSeedToken(t, store, enforcer, "conf-deny", "conf-deny-role", confGlobal("players", "read")),
		"scoped": confSeedToken(t, store, enforcer, "conf-scoped", "conf-scoped-role", scoped),
		"invalid": "dp_no_such_token",
	}
	f := &confFixture{
		sessions: make(map[string]*sdkmcp.ClientSession),
		clients:  make(map[string]*FamilyServerClients),
		recorder: rec, server: ts,
	}
	for name, token := range tokens {
		clients, err := NewFamilyServerClients(ts.Client(), ts.URL, token)
		if err != nil {
			t.Fatalf("NewFamilyServerClients(%s): %v", name, err)
		}
		s := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "conf-" + name, Version: "v0"}, nil)
		RegisterFamilyServerTools(s, clients)
		f.clients[name] = clients
		f.sessions[name] = confSession(t, s)
	}
	return f
}

type confCase struct {
	tool string
	args map[string]any
	// want is the set of exact protojson fragments the typed handler must
	// have received. An empty set is valid for no-argument RPCs.
	want []string
}

func confCases() []confCase {
	return []confCase{
		{"get_server_logs", map[string]any{"id": "srv-1", "tail": 10}, []string{`"id":"srv-1"`, `"tail":10`}},
		{"clear_server_logs", map[string]any{"id": "srv-1", "confirm": true}, []string{`"id":"srv-1"`}},
		{"get_next_available_port", map[string]any{}, nil},
		{"create_server", map[string]any{"name": "srv-2", "mc_version": "1.21.4", "mod_loader": "fabric", "request_id": "req-1", "confirm": true}, []string{`"name":"srv-2"`, `"mc_version":"1.21.4"`, `"mod_loader":"MOD_LOADER_FABRIC"`, `"request_id":"req-1"`}},
		{"update_server", map[string]any{"id": "srv-1", "name": "renamed", "confirm": true}, []string{`"id":"srv-1"`, `"name":"renamed"`}},
		{"delete_server", map[string]any{"id": "srv-1", "keep_data": true, "confirm": true}, []string{`"id":"srv-1"`, `"keep_data":true`}},
		{"recreate_server", map[string]any{"id": "srv-1", "confirm": true}, []string{`"id":"srv-1"`}},
		{"send_command", map[string]any{"id": "srv-1", "command": "list", "confirm": true}, []string{`"id":"srv-1"`, `"command":"list"`}},
		{"upload_to_mclogs", map[string]any{"id": "srv-1", "confirm": true}, []string{`"id":"srv-1"`}},
		{"migrate_server", map[string]any{"id": "srv-1", "target_node_id": "node-1", "request_id": "req-2", "confirm": true}, []string{`"id":"srv-1"`, `"target_node_id":"node-1"`, `"request_id":"req-2"`}},
		{"list_server_players", map[string]any{"server_id": "srv-1"}, []string{`"server_id":"srv-1"`}},
		{"get_server_config", map[string]any{"server_id": "srv-1"}, []string{`"server_id":"srv-1"`}},
		{"update_server_config", map[string]any{"server_id": "srv-1", "updates": map[string]string{"MOTD": "Welcome"}, "stage": true, "confirm": true}, []string{`"server_id":"srv-1"`, `"MOTD":"Welcome"`, `"stage":true`}},
		{"get_update_batch", map[string]any{"server_id": "srv-1"}, []string{`"server_id":"srv-1"`}},
		{"discard_update_batch", map[string]any{"server_id": "srv-1", "confirm": true}, []string{`"server_id":"srv-1"`}},
		{"get_global_settings", map[string]any{}, nil},
		{"update_global_settings", map[string]any{"updates": map[string]string{"DEFAULT_MOTD": "Welcome"}, "confirm": true}, []string{`"DEFAULT_MOTD":"Welcome"`}},
		{"sync_global_settings_to_servers", map[string]any{"ops_and_whitelist_only": true, "confirm": true}, []string{`"ops_and_whitelist_only":true`}},
		{"validate_provider_key", map[string]any{"provider": "curseforge", "api_key": "PROVIDER-API-KEY-DO-NOT-ECHO", "confirm": true}, []string{`"provider":"curseforge"`, `"api_key":`}},
		{"list_tasks", map[string]any{"server_id": "srv-1"}, []string{`"server_id":"srv-1"`}},
		{"get_task", map[string]any{"id": "task-1"}, []string{`"id":"task-1"`}},
		{"create_task", map[string]any{"server_id": "srv-1", "name": "nightly", "task_type": "backup", "schedule": "cron", "cron_expr": "0 3 * * *", "confirm": true}, []string{`"server_id":"srv-1"`, `"name":"nightly"`, `"task_type":"TASK_TYPE_BACKUP"`, `"schedule":"SCHEDULE_TYPE_CRON"`, `"cron_expr":"0 3 * * *"`}},
		{"update_task", map[string]any{"id": "task-1", "name": "renamed", "confirm": true}, []string{`"id":"task-1"`, `"name":"renamed"`}},
		{"delete_task", map[string]any{"id": "task-1", "confirm": true}, []string{`"id":"task-1"`}},
		{"toggle_task", map[string]any{"id": "task-1", "status": "enabled", "confirm": true}, []string{`"id":"task-1"`, `"status":"TASK_STATUS_ENABLED"`}},
		{"trigger_task", map[string]any{"id": "task-1", "confirm": true}, []string{`"id":"task-1"`}},
		{"list_task_executions", map[string]any{"task_id": "task-1", "limit": 10}, []string{`"task_id":"task-1"`, `"limit":10`}},
		{"list_server_executions", map[string]any{"server_id": "srv-1", "limit": 10}, []string{`"server_id":"srv-1"`, `"limit":10`}},
		{"get_task_execution", map[string]any{"id": "exec-1"}, []string{`"id":"exec-1"`}},
		{"cancel_execution", map[string]any{"id": "exec-1", "confirm": true}, []string{`"id":"exec-1"`}},
		{"get_scheduler_status", map[string]any{}, nil},
		{"list_activity_logs", map[string]any{"server_id": "srv-1", "limit": 10}, []string{`"server_id":"srv-1"`, `"limit":10`}},
		{"list_operations", map[string]any{"server_id": "srv-1", "limit": 10}, []string{`"server_id":"srv-1"`, `"limit":10`}},
		{"get_operation", map[string]any{"server_id": "srv-1", "operation_id": "op-1"}, []string{`"server_id":"srv-1"`, `"operation_id":"op-1"`}},
		{"resolve_operation", map[string]any{"server_id": "srv-1", "operation_id": "op-1", "confirm": true}, []string{`"server_id":"srv-1"`, `"operation_id":"op-1"`}},
		{"list_blueprints", map[string]any{}, nil},
		{"get_blueprint", map[string]any{"id": "bp-1"}, []string{`"id":"bp-1"`}},
		{"get_minecraft_versions", map[string]any{}, nil},
		{"get_mod_loaders", map[string]any{}, nil},
		{"get_docker_images", map[string]any{}, nil},
		{"get_loader_versions", map[string]any{"loader": "fabric", "mc_version": "1.21.4"}, []string{`"loader":"fabric"`, `"mc_version":"1.21.4"`}},
		{"get_application_logs", map[string]any{"tail": 10}, []string{`"tail":10`}},
		{"generate_support_bundle", map[string]any{"include_logs": true, "confirm": true}, []string{`"include_logs":true`}},
		{"upload_support_bundle", map[string]any{"issue_description": "server will not start", "confirm": true}, []string{`"issue_description":"server will not start"`}},
		{"download_support_bundle", map[string]any{"bundle_id": "bundle-1"}, []string{`"bundle_id":"bundle-1"`}},
		{"ping_node", map[string]any{"id": "node-1"}, []string{`"id":"node-1"`}},
		{"get_node", map[string]any{"id": "node-1"}, []string{`"id":"node-1"`}},
		{"list_nodes", map[string]any{}, nil},
		{"create_node", map[string]any{"name": "node-2", "host": "tcp://10.0.0.2:2375", "confirm": true}, []string{`"name":"node-2"`, `"host":"tcp://10.0.0.2:2375"`}},
		{"update_node", map[string]any{"id": "node-1", "name": "renamed", "confirm": true}, []string{`"id":"node-1"`, `"name":"renamed"`}},
		{"delete_node", map[string]any{"id": "node-1", "confirm": true}, []string{`"id":"node-1"`}},
	}
}

func confAssertCaptured(t *testing.T, f *confFixture, tc confCase) confCapture {
	t.Helper()
	d, ok := FamilyServerDescriptor(tc.tool)
	if !ok {
		t.Fatalf("no exported descriptor for %s", tc.tool)
	}
	all := f.recorder.snapshot()
	if n := f.recorder.total(); n != 1 {
		t.Fatalf("%s: handler calls = %d, want exactly 1: %+v", tc.tool, n, all)
	}
	calls := all[d.Procedure]
	if len(calls) != 1 {
		t.Fatalf("%s: expected one call to %s, got %+v", tc.tool, d.Procedure, all)
	}
	c := calls[0]
	for _, frag := range tc.want {
		if !strings.Contains(c.requestJSON, frag) {
			t.Errorf("%s: typed handler did not receive %s (request fields were not mapped)", tc.tool, frag)
		}
	}
	if len(c.requestJSON) > 4096 {
		t.Errorf("%s: unbounded request payload (%d bytes)", tc.tool, len(c.requestJSON))
	}
	return c
}

func confAssertHandlerOutcome(t *testing.T, tool, procedure string, out ToolOutput) {
	t.Helper()
	if out.Status != "ok" || out.Error != nil {
		t.Fatalf("%s: unexpected tool error: %+v", tool, out)
	}
	if !confResponseHasStringField(procedure) {
		return
	}
	raw, err := json.Marshal(out.Result)
	if err != nil {
		t.Fatal(err)
	}
	marker := confMarker(procedure)
	if !strings.Contains(string(raw), marker) {
		t.Errorf("%s: response did not carry the real handler marker %q: %s", tool, marker, raw)
	}
}

func TestConfEveryToolReachesTypedMethodWithBoundedArgs(t *testing.T) {
	f := confNewFixture(t, nil)
	verified := 0
	for _, tc := range confCases() {
		t.Run(tc.tool, func(t *testing.T) {
			f.recorder.reset()
			out := confCall(t, f.sessions["allow"], tc.tool, tc.args)
			confAssertHandlerOutcome(t, tc.tool, confDescriptor(t, tc.tool).Procedure, out)
			confAssertCaptured(t, f, tc)
			if got := f.recorder.snapshot()[confDescriptor(t, tc.tool).Procedure][0].user; got != "conf-allow" {
				t.Fatalf("%s: handler observed user %q, want conf-allow", tc.tool, got)
			}
			verified++
		})
	}
	t.Logf("CONF_VERIFIED_TYPED_METHODS=%d", verified)
	if verified != FamilyServerReachableCount() || verified != len(confCases()) {
		t.Fatalf("verified=%d reachable=%d cases=%d", verified, FamilyServerReachableCount(), len(confCases()))
	}
}

func confDescriptor(t *testing.T, tool string) FamilyServerToolDescriptor {
	t.Helper()
	d, ok := FamilyServerDescriptor(tool)
	if !ok {
		t.Fatalf("no descriptor for %s", tool)
	}
	return d
}

func confWithoutConfirm(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		if k != "confirm" {
			out[k] = v
		}
	}
	return out
}

func TestConfScopedPermissionDenialNeverReachesHandler(t *testing.T) {
	f := confNewFixture(t, nil)
	denials := 0
	for _, tc := range confCases() {
		d := confDescriptor(t, tc.tool)
		if d.Scope == "authenticated" {
			continue // no object scope exists for these four RPCs
		}
		t.Run(tc.tool, func(t *testing.T) {
			f.recorder.reset()
			out := confCall(t, f.sessions["deny"], tc.tool, tc.args)
			if out.Status != "error" || out.Error == nil {
				t.Fatalf("%s: expected denial, got %+v", tc.tool, out)
			}
			if out.Error.Kind != ErrKindPermissionDenied || out.Error.Code != "permission_denied" ||
				out.Error.Tool != tc.tool || out.Error.Procedure != d.Procedure {
				t.Fatalf("%s: incomplete denial: %+v", tc.tool, out.Error)
			}
			if n := f.recorder.total(); n != 0 {
				t.Fatalf("%s: handler was reached %d times despite denial", tc.tool, n)
			}
			denials++
		})
	}
	t.Logf("CONF_SCOPED_DENIALS=%d", denials)
}

func TestConfConfirmationGateBlocksBeforeRPC(t *testing.T) {
	f := confNewFixture(t, nil)
	gated := 0
	for _, tc := range confCases() {
		d := confDescriptor(t, tc.tool)
		if !d.Mutation {
			continue
		}
		t.Run(tc.tool, func(t *testing.T) {
			f.recorder.reset()
			out := confCall(t, f.sessions["allow"], tc.tool, confWithoutConfirm(tc.args))
			if out.Status != "error" || out.Error == nil || out.Error.Kind != ErrKindConfirmationRequired {
				t.Fatalf("%s: expected confirmation_required, got %+v", tc.tool, out)
			}
			if n := f.recorder.total(); n != 0 {
				t.Fatalf("%s: mutation reached handler without confirm (%d calls)", tc.tool, n)
			}
			out = confCall(t, f.sessions["allow"], tc.tool, tc.args)
			confAssertHandlerOutcome(t, tc.tool, d.Procedure, out)
			confAssertCaptured(t, f, tc)
			gated++
		})
	}
	t.Logf("CONF_CONFIRMATION_GATED=%d", gated)
}

type confScopeCase struct {
	tool  string
	field string
	right string
	wrong string
}

func TestConfResourceScopeRightAndWrongServerID(t *testing.T) {
	f := confNewFixture(t, nil)
	cases := []confScopeCase{
		{"get_server_logs", "id", "srv-1", "srv-2"},
		{"list_server_players", "server_id", "srv-1", "srv-2"},
		{"get_server_config", "server_id", "srv-1", "srv-2"},
		{"list_tasks", "server_id", "srv-1", "srv-2"},
		{"list_activity_logs", "server_id", "srv-1", "srv-2"},
		{"ping_node", "id", "node-1", "node-2"},
		{"get_node", "id", "node-1", "node-2"},
	}
	for _, tc := range cases {
		t.Run(tc.tool+"/right", func(t *testing.T) {
			f.recorder.reset()
			out := confCall(t, f.sessions["scoped"], tc.tool, map[string]any{tc.field: tc.right})
			confAssertHandlerOutcome(t, tc.tool, confDescriptor(t, tc.tool).Procedure, out)
			c := confAssertCaptured(t, f, confCase{tool: tc.tool})
			if !strings.Contains(c.requestJSON, `"`+tc.field+`":"`+tc.right+`"`) {
				t.Fatalf("%s: scoped id not mapped: %s", tc.tool, c.requestJSON)
			}
		})
		t.Run(tc.tool+"/wrong", func(t *testing.T) {
			f.recorder.reset()
			out := confCall(t, f.sessions["scoped"], tc.tool, map[string]any{tc.field: tc.wrong})
			if out.Status != "error" || out.Error == nil || out.Error.Kind != ErrKindPermissionDenied {
				t.Fatalf("%s: wrong %s was not denied: %+v", tc.tool, tc.field, out)
			}
			if n := f.recorder.total(); n != 0 {
				t.Fatalf("%s: wrong-scope request reached the handler", tc.tool)
			}
		})
	}
}

func TestConfUnauthenticatedNeverReachesHandler(t *testing.T) {
	f := confNewFixture(t, nil)
	n := 0
	for _, tc := range confCases() {
		t.Run(tc.tool, func(t *testing.T) {
			f.recorder.reset()
			out := confCall(t, f.sessions["invalid"], tc.tool, tc.args)
			if out.Status != "error" || out.Error == nil || out.Error.Kind != ErrKindUnauthenticated ||
				out.Error.Code != "unauthenticated" || out.Error.Procedure == "" {
				t.Fatalf("%s: bad unauthenticated error: %+v", tc.tool, out)
			}
			if got := f.recorder.total(); got != 0 {
				t.Fatalf("%s: invalid token reached %d handlers", tc.tool, got)
			}
			n++
		})
	}
	t.Logf("CONF_UNAUTHENTICATED=%d", n)
}

func TestConfHandlerStatusOutcomesAreHonest(t *testing.T) {
	const (
		getCfg    = "/carbonpanel.v1.ConfigService/GetServerConfig"
		getBatch  = "/carbonpanel.v1.ConfigService/GetUpdateBatch"
		getTask   = "/carbonpanel.v1.TaskService/GetTask"
	)
	f := confNewFixture(t, map[string]confBehavior{
		getCfg:   confNotFound,
		getBatch: confUnimplemented,
		getTask:  confUnavailable,
	})

	t.Run("absent_record_is_not_a_capability_error", func(t *testing.T) {
		f.recorder.reset()
		out := confCall(t, f.sessions["allow"], "get_server_config", map[string]any{"server_id": "missing"})
		e := out.Error
		if out.Status != "error" || e == nil || e.Kind != ErrKindNotFound || e.Code != "not_found" {
			t.Fatalf("unexpected outcome: %+v", out)
		}
		if strings.Contains(e.Message, "panel build") || strings.Contains(e.Message, "upgrade") || e.MinimumPanelBuild != "" {
			t.Fatalf("domain absence reported as capability gap: %+v", e)
		}
		if f.recorder.count(getCfg) != 1 {
			t.Fatal("notfound handler was not reached")
		}
	})

	t.Run("unimplemented_procedure_is_a_capability_error", func(t *testing.T) {
		f.recorder.reset()
		out := confCall(t, f.sessions["allow"], "get_update_batch", map[string]any{"server_id": "srv-1"})
		e := out.Error
		if out.Status != "error" || e == nil || e.Kind != ErrKindCapability || e.Code != "unimplemented" {
			t.Fatalf("unexpected outcome: %+v", out)
		}
		if e.MinimumPanelBuild != string(PanelBuildStableV140) || !strings.Contains(e.Message, "panel build") {
			t.Fatalf("capability error lacks build/procedure: %+v", e)
		}
		if f.recorder.count(getBatch) != 1 {
			t.Fatal("unimplemented handler was not reached")
		}
	})

	t.Run("backend_unavailable_is_an_upstream_error", func(t *testing.T) {
		f.recorder.reset()
		out := confCall(t, f.sessions["allow"], "get_task", map[string]any{"id": "task-1"})
		e := out.Error
		if out.Status != "error" || e == nil || e.Kind != ErrKindUpstream || e.Code != "unavailable" {
			t.Fatalf("unexpected outcome: %+v", out)
		}
		if f.recorder.count(getTask) != 1 {
			t.Fatal("unavailable handler was not reached")
		}
	})
}

func TestConfSendCommandAllowlistAndBoundedArgs(t *testing.T) {
	f := confNewFixture(t, nil)
	const proc = "/carbonpanel.v1.ServerService/SendCommand"

	f.recorder.reset()
	out := confCall(t, f.sessions["allow"], "send_command", map[string]any{"id": "srv-1", "command": "list", "confirm": true})
	confAssertHandlerOutcome(t, "send_command", proc, out)
	c := confAssertCaptured(t, f, confCase{tool: "send_command"})
	if !strings.Contains(c.requestJSON, `"command":"list"`) {
		t.Fatalf("allowlisted command was not mapped: %s", c.requestJSON)
	}

	for _, bad := range []map[string]any{
		{"id": "srv-1", "command": "rm -rf /", "confirm": true},
		{"id": "srv-1", "command": "stop", "confirm": true},
		{"id": "srv-1", "command": "", "confirm": true},
	} {
		f.recorder.reset()
		out := confCall(t, f.sessions["allow"], "send_command", bad)
		if out.Status != "error" || out.Error == nil || out.Error.Kind != ErrKindInvalidArguments {
			t.Fatalf("non-allowlisted command %v was accepted: %+v", bad["command"], out)
		}
		if n := f.recorder.total(); n != 0 {
			t.Fatalf("non-allowlisted command reached the handler %d times", n)
		}
	}
}

func TestConfNodeStatusByKnownIDOnlyNoDiscovery(t *testing.T) {
	f := confNewFixture(t, nil)
	for _, tool := range []string{"ping_node", "get_node"} {
		f.recorder.reset()
		out := confCall(t, f.sessions["allow"], tool, map[string]any{"id": "node-1"})
		proc := confDescriptor(t, tool).Procedure
		confAssertHandlerOutcome(t, tool, proc, out)
		c := confAssertCaptured(t, f, confCase{tool: tool})
		if !strings.Contains(c.requestJSON, `"id":"node-1"`) {
			t.Fatalf("%s: known configured node id was not mapped: %s", tool, c.requestJSON)
		}
	}

	// The typed node client exists, but the discovery procedure has no route
	// in the conformance mount (mirroring the unmounted catalog decision).
	f.recorder.reset()
	_, err := f.clients["allow"].Node.ScanNodes(context.Background(), connect.NewRequest(&v1.ScanNodesRequest{}))
	if err == nil {
		t.Fatal("ScanNodes unexpectedly succeeded; discovery must not be reachable")
	}
	var ce *connect.Error
	if !errors.As(err, &ce) || (ce.Code() != connect.CodeUnimplemented && ce.Code() != connect.CodeNotFound) {
		t.Fatalf("ScanNodes returned %v, want unimplemented/not found", err)
	}
	if n := f.recorder.total(); n != 0 {
		t.Fatalf("discovery reached a recorded handler %d times", n)
	}

	listed, err := f.sessions["allow"].ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "scan_nodes" {
			t.Fatal("scan_nodes is exposed as a tool")
		}
	}
	if len(listed.Tools) != FamilyServerReachableCount() {
		t.Fatalf("listed %d tools, want %d", len(listed.Tools), FamilyServerReachableCount())
	}
}

func TestConfExportedCoverageCountsMatchExecutedEvidence(t *testing.T) {
	if got := FamilyServerListedCount(); got != 63 {
		t.Fatalf("LISTED=%d, want 63", got)
	}
	if got := FamilyServerReachableCount(); got != 51 {
		t.Fatalf("REACHABLE=%d, want 51", got)
	}
	if got := len(FamilyServerExcludedDescriptors()); got != 1 || FamilyServerExcludedDescriptors()[0].Name != "scan_nodes" {
		t.Fatalf("excluded RPC set changed: %+v", FamilyServerExcludedDescriptors())
	}
	if len(confCases()) != 51 {
		t.Fatalf("executable cases=%d, want 51", len(confCases()))
	}
}
