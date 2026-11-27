// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

const (
	famSecretPassword = "hunter2-password-value"
	famSecretWebhook  = "webhook-signing-secret-value"
	famSecretToken    = "ghp_repository_token_secret"
	famSecretTLSKey   = "PRIVATE-DOCKER-TLS-KEY-MATERIAL"
	famProbeAPIKey    = "PROVIDER-API-KEY-DO-NOT-ECHO"
)

type famFixture struct {
	sessions map[string]*sdkmcp.ClientSession
	server   *httptest.Server
}

func famHandle[Req, Res any](mux *http.ServeMux, procedure string, ic connect.UnaryInterceptorFunc,
	fn func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error)) {
	mux.Handle(procedure, connect.NewUnaryHandler(procedure, fn, connect.WithInterceptors(ic)))
}

func famSecretCategories() []*v1.ConfigCategory {
	return []*v1.ConfigCategory{{
		Name: "security",
		Properties: []*v1.ConfigProperty{
			{Key: "LEVEL_PASSWORD", Label: "Password", Type: "password", Value: famSecretPassword, DefaultValue: famStringPtr(famSecretPassword)},
			{Key: "WEBHOOK_SECRET", Label: "Webhook secret", Type: "text", Value: famSecretWebhook},
			{Key: "MOTD", Label: "MOTD", Type: "text", Value: "Welcome"},
		},
	}}
}

func famSecretTask() *v1.ScheduledTask {
	return &v1.ScheduledTask{
		Id: "task-1", ServerId: "srv-1", Name: "nightly", TaskType: v1.TaskType_TASK_TYPE_BACKUP,
		Config: `{"command":"say hi","secret":"` + famSecretWebhook + `","auth_token":"` + famSecretToken + `"}`,
	}
}

func famTLSNode() *v1.Node {
	return &v1.Node{
		Id: "node-1", Name: "node-1", Host: "unix:///var/run/docker.sock",
		TlsCaCert: "CA-CERT-MATERIAL", TlsCert: "CLIENT-CERT-MATERIAL", TlsKey: famSecretTLSKey,
		Status: v1.NodeStatus_NODE_STATUS_ONLINE,
	}
}

// registerFamilyRPCHandlers mounts every reachable procedure of the family on
// a disposable mux behind the real production auth interceptor. No controller,
// Docker daemon, world or database is contacted.
func registerFamilyRPCHandlers(mux *http.ServeMux, ic connect.UnaryInterceptorFunc) {
	// Plain zero-response handlers.
	registerOK[v1.GetServerLogsRequest, v1.GetServerLogsResponse](mux, "/carbonpanel.v1.ServerService/GetServerLogs", ic, "")
	registerOK[v1.ClearServerLogsRequest, v1.ClearServerLogsResponse](mux, "/carbonpanel.v1.ServerService/ClearServerLogs", ic, "")
	registerOK[v1.GetNextAvailablePortRequest, v1.GetNextAvailablePortResponse](mux, "/carbonpanel.v1.ServerService/GetNextAvailablePort", ic, "")
	registerOK[v1.ExportServerArchiveRequest, v1.ExportServerArchiveResponse](mux, "/carbonpanel.v1.ServerService/ExportServerArchive", ic, "")
	registerOK[v1.PreviewServerArchiveRequest, v1.PreviewServerArchiveResponse](mux, "/carbonpanel.v1.ServerService/PreviewServerArchive", ic, "")
	registerOK[v1.CreateServerRequest, v1.CreateServerResponse](mux, "/carbonpanel.v1.ServerService/CreateServer", ic, "")
	registerOK[v1.UpdateServerRequest, v1.UpdateServerResponse](mux, "/carbonpanel.v1.ServerService/UpdateServer", ic, "")
	registerOK[v1.DeleteServerRequest, v1.DeleteServerResponse](mux, "/carbonpanel.v1.ServerService/DeleteServer", ic, "")
	registerOK[v1.RecreateServerRequest, v1.RecreateServerResponse](mux, "/carbonpanel.v1.ServerService/RecreateServer", ic, "")
	registerOK[v1.PauseServerRequest, v1.PauseServerResponse](mux, "/carbonpanel.v1.ServerService/PauseServer", ic, "")
	registerOK[v1.DeepSleepServerRequest, v1.DeepSleepServerResponse](mux, "/carbonpanel.v1.ServerService/DeepSleepServer", ic, "")
	registerOK[v1.SendCommandRequest, v1.SendCommandResponse](mux, "/carbonpanel.v1.ServerService/SendCommand", ic, "")
	registerOK[v1.UploadToMCLogsRequest, v1.UploadToMCLogsResponse](mux, "/carbonpanel.v1.ServerService/UploadToMCLogs", ic, "")
	registerOK[v1.MigrateServerRequest, v1.MigrateServerResponse](mux, "/carbonpanel.v1.ServerService/MigrateServer", ic, "")
	registerOK[v1.ListServerPlayersRequest, v1.ListServerPlayersResponse](mux, "/carbonpanel.v1.ServerService/ListServerPlayers", ic, "")

	registerOK[v1.GetUpdateBatchRequest, v1.GetUpdateBatchResponse](mux, "/carbonpanel.v1.ConfigService/GetUpdateBatch", ic, "")
	registerOK[v1.DiscardUpdateBatchRequest, v1.DiscardUpdateBatchResponse](mux, "/carbonpanel.v1.ConfigService/DiscardUpdateBatch", ic, "")
	registerOK[v1.SyncGlobalSettingsToServersRequest, v1.SyncGlobalSettingsToServersResponse](mux, "/carbonpanel.v1.ConfigService/SyncGlobalSettingsToServers", ic, "")

	registerOK[v1.DeleteTaskRequest, v1.DeleteTaskResponse](mux, "/carbonpanel.v1.TaskService/DeleteTask", ic, "")
	registerOK[v1.TriggerTaskRequest, v1.TriggerTaskResponse](mux, "/carbonpanel.v1.TaskService/TriggerTask", ic, "")
	registerOK[v1.ListTaskExecutionsRequest, v1.ListTaskExecutionsResponse](mux, "/carbonpanel.v1.TaskService/ListTaskExecutions", ic, "")
	registerOK[v1.ListServerExecutionsRequest, v1.ListServerExecutionsResponse](mux, "/carbonpanel.v1.TaskService/ListServerExecutions", ic, "")
	registerOK[v1.GetTaskExecutionRequest, v1.GetTaskExecutionResponse](mux, "/carbonpanel.v1.TaskService/GetTaskExecution", ic, "")
	registerOK[v1.CancelExecutionRequest, v1.CancelExecutionResponse](mux, "/carbonpanel.v1.TaskService/CancelExecution", ic, "")
	registerOK[v1.GetSchedulerStatusRequest, v1.GetSchedulerStatusResponse](mux, "/carbonpanel.v1.TaskService/GetSchedulerStatus", ic, "")

	registerOK[v1.ListActivityLogsRequest, v1.ListActivityLogsResponse](mux, "/carbonpanel.v1.ActivityService/ListActivityLogs", ic, "")
	registerOK[v1.ListOperationsRequest, v1.ListOperationsResponse](mux, "/carbonpanel.v1.OperationService/ListOperations", ic, "")
	registerOK[v1.GetOperationRequest, v1.GetOperationResponse](mux, "/carbonpanel.v1.OperationService/GetOperation", ic, "")
	registerOK[v1.ResolveOperationRequest, v1.ResolveOperationResponse](mux, "/carbonpanel.v1.OperationService/ResolveOperation", ic, "")
	registerOK[v1.ListBlueprintsRequest, v1.ListBlueprintsResponse](mux, "/carbonpanel.v1.BlueprintService/ListBlueprints", ic, "")
	registerOK[v1.GetBlueprintRequest, v1.GetBlueprintResponse](mux, "/carbonpanel.v1.BlueprintService/GetBlueprint", ic, "")
	registerOK[v1.GetMinecraftVersionsRequest, v1.GetMinecraftVersionsResponse](mux, "/carbonpanel.v1.MinecraftService/GetMinecraftVersions", ic, "")
	registerOK[v1.GetModLoadersRequest, v1.GetModLoadersResponse](mux, "/carbonpanel.v1.MinecraftService/GetModLoaders", ic, "")
	registerOK[v1.GetDockerImagesRequest, v1.GetDockerImagesResponse](mux, "/carbonpanel.v1.MinecraftService/GetDockerImages", ic, "")
	registerOK[v1.GetLoaderVersionsRequest, v1.GetLoaderVersionsResponse](mux, "/carbonpanel.v1.MinecraftService/GetLoaderVersions", ic, "")
	registerOK[v1.GenerateSupportBundleRequest, v1.GenerateSupportBundleResponse](mux, "/carbonpanel.v1.SupportService/GenerateSupportBundle", ic, "")
	registerOK[v1.UploadSupportBundleRequest, v1.UploadSupportBundleResponse](mux, "/carbonpanel.v1.SupportService/UploadSupportBundle", ic, "")
	registerOK[v1.PingNodeRequest, v1.PingNodeResponse](mux, "/carbonpanel.v1.NodeService/PingNode", ic, "")
	registerOK[v1.DeleteNodeRequest, v1.DeleteNodeResponse](mux, "/carbonpanel.v1.NodeService/DeleteNode", ic, "")

	// Custom handlers carrying deliberately sensitive-looking data so the
	// client-side redaction can be proven with real Connect responses.
	famHandle[v1.GetServerConfigRequest, v1.GetServerConfigResponse](mux, "/carbonpanel.v1.ConfigService/GetServerConfig", ic,
		func(context.Context, *connect.Request[v1.GetServerConfigRequest]) (*connect.Response[v1.GetServerConfigResponse], error) {
			return connect.NewResponse(&v1.GetServerConfigResponse{Categories: famSecretCategories()}), nil
		})
	famHandle[v1.UpdateServerConfigRequest, v1.UpdateServerConfigResponse](mux, "/carbonpanel.v1.ConfigService/UpdateServerConfig", ic,
		func(context.Context, *connect.Request[v1.UpdateServerConfigRequest]) (*connect.Response[v1.UpdateServerConfigResponse], error) {
			return connect.NewResponse(&v1.UpdateServerConfigResponse{Categories: famSecretCategories(), Staged: true}), nil
		})
	famHandle[v1.GetGlobalSettingsRequest, v1.GetGlobalSettingsResponse](mux, "/carbonpanel.v1.ConfigService/GetGlobalSettings", ic,
		func(context.Context, *connect.Request[v1.GetGlobalSettingsRequest]) (*connect.Response[v1.GetGlobalSettingsResponse], error) {
			return connect.NewResponse(&v1.GetGlobalSettingsResponse{Categories: famSecretCategories()}), nil
		})
	famHandle[v1.UpdateGlobalSettingsRequest, v1.UpdateGlobalSettingsResponse](mux, "/carbonpanel.v1.ConfigService/UpdateGlobalSettings", ic,
		func(context.Context, *connect.Request[v1.UpdateGlobalSettingsRequest]) (*connect.Response[v1.UpdateGlobalSettingsResponse], error) {
			return connect.NewResponse(&v1.UpdateGlobalSettingsResponse{Categories: famSecretCategories()}), nil
		})
	famHandle[v1.ValidateProviderKeyRequest, v1.ValidateProviderKeyResponse](mux, "/carbonpanel.v1.ConfigService/ValidateProviderKey", ic,
		func(_ context.Context, req *connect.Request[v1.ValidateProviderKeyRequest]) (*connect.Response[v1.ValidateProviderKeyResponse], error) {
			// A badly behaved upstream echoes the key; the tool must scrub it.
			return connect.NewResponse(&v1.ValidateProviderKeyResponse{
				Valid: true, Message: "validated with key " + req.Msg.GetApiKey(),
			}), nil
		})
	famHandle[v1.ListTasksRequest, v1.ListTasksResponse](mux, "/carbonpanel.v1.TaskService/ListTasks", ic,
		func(context.Context, *connect.Request[v1.ListTasksRequest]) (*connect.Response[v1.ListTasksResponse], error) {
			return connect.NewResponse(&v1.ListTasksResponse{Tasks: []*v1.ScheduledTask{famSecretTask()}}), nil
		})
	for _, procedure := range []string{
		"/carbonpanel.v1.TaskService/GetTask", "/carbonpanel.v1.TaskService/CreateTask",
		"/carbonpanel.v1.TaskService/UpdateTask", "/carbonpanel.v1.TaskService/ToggleTask",
	} {
		switch procedure {
		case "/carbonpanel.v1.TaskService/GetTask":
			famHandle[v1.GetTaskRequest, v1.GetTaskResponse](mux, procedure, ic,
				func(context.Context, *connect.Request[v1.GetTaskRequest]) (*connect.Response[v1.GetTaskResponse], error) {
					return connect.NewResponse(&v1.GetTaskResponse{Task: famSecretTask()}), nil
				})
		case "/carbonpanel.v1.TaskService/CreateTask":
			famHandle[v1.CreateTaskRequest, v1.CreateTaskResponse](mux, procedure, ic,
				func(context.Context, *connect.Request[v1.CreateTaskRequest]) (*connect.Response[v1.CreateTaskResponse], error) {
					return connect.NewResponse(&v1.CreateTaskResponse{Task: famSecretTask()}), nil
				})
		case "/carbonpanel.v1.TaskService/UpdateTask":
			famHandle[v1.UpdateTaskRequest, v1.UpdateTaskResponse](mux, procedure, ic,
				func(context.Context, *connect.Request[v1.UpdateTaskRequest]) (*connect.Response[v1.UpdateTaskResponse], error) {
					return connect.NewResponse(&v1.UpdateTaskResponse{Task: famSecretTask()}), nil
				})
		case "/carbonpanel.v1.TaskService/ToggleTask":
			famHandle[v1.ToggleTaskRequest, v1.ToggleTaskResponse](mux, procedure, ic,
				func(context.Context, *connect.Request[v1.ToggleTaskRequest]) (*connect.Response[v1.ToggleTaskResponse], error) {
					return connect.NewResponse(&v1.ToggleTaskResponse{Task: famSecretTask()}), nil
				})
		}
	}
	famHandle[v1.GetNodeRequest, v1.GetNodeResponse](mux, "/carbonpanel.v1.NodeService/GetNode", ic,
		func(context.Context, *connect.Request[v1.GetNodeRequest]) (*connect.Response[v1.GetNodeResponse], error) {
			return connect.NewResponse(&v1.GetNodeResponse{Node: famTLSNode()}), nil
		})
	famHandle[v1.ListNodesRequest, v1.ListNodesResponse](mux, "/carbonpanel.v1.NodeService/ListNodes", ic,
		func(context.Context, *connect.Request[v1.ListNodesRequest]) (*connect.Response[v1.ListNodesResponse], error) {
			return connect.NewResponse(&v1.ListNodesResponse{Nodes: []*v1.Node{famTLSNode()}}), nil
		})
	famHandle[v1.CreateNodeRequest, v1.CreateNodeResponse](mux, "/carbonpanel.v1.NodeService/CreateNode", ic,
		func(context.Context, *connect.Request[v1.CreateNodeRequest]) (*connect.Response[v1.CreateNodeResponse], error) {
			return connect.NewResponse(&v1.CreateNodeResponse{Node: famTLSNode()}), nil
		})
	famHandle[v1.UpdateNodeRequest, v1.UpdateNodeResponse](mux, "/carbonpanel.v1.NodeService/UpdateNode", ic,
		func(context.Context, *connect.Request[v1.UpdateNodeRequest]) (*connect.Response[v1.UpdateNodeResponse], error) {
			return connect.NewResponse(&v1.UpdateNodeResponse{Node: famTLSNode()}), nil
		})
	famHandle[v1.GetApplicationLogsRequest, v1.GetApplicationLogsResponse](mux, "/carbonpanel.v1.SupportService/GetApplicationLogs", ic,
		func(context.Context, *connect.Request[v1.GetApplicationLogsRequest]) (*connect.Response[v1.GetApplicationLogsResponse], error) {
			return connect.NewResponse(&v1.GetApplicationLogsResponse{Content: "application-log-line\n", Filename: "carbon-panel.log", Size: 21}), nil
		})
	famHandle[v1.DownloadSupportBundleRequest, v1.DownloadSupportBundleResponse](mux, "/carbonpanel.v1.SupportService/DownloadSupportBundle", ic,
		func(context.Context, *connect.Request[v1.DownloadSupportBundleRequest]) (*connect.Response[v1.DownloadSupportBundleResponse], error) {
			return connect.NewResponse(&v1.DownloadSupportBundleResponse{Content: []byte("BUNDLE-DATA"), Filename: "bundle.zip"}), nil
		})
}

func newFamilyFixture(t *testing.T) *famFixture {
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
		AnonymousAccess: false, AllowNoAuth: false,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	interceptor := rpc.NewAuthInterceptor(authManager, enforcer, store, logger.New())

	mux := http.NewServeMux()
	registerAuthHandlers(mux, interceptor, true)
	registerFamilyRPCHandlers(mux, interceptor)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	f := &famFixture{sessions: make(map[string]*sdkmcp.ClientSession), server: ts}
	tokens := map[string]struct {
		user  string
		role  string
		perms []rbac.Permission
	}{
		"serverOps": {"u-server-ops", "server-ops", append(globalPerms("servers", "read", "start", "stop", "restart", "update", "create", "delete", "command"), globalPerms("files", "read")...)},
		"configOps": {"u-config-ops", "config-ops", append(globalPerms("server_config", "read", "update"), globalPerms("settings", "read", "update")...)},
		"taskOps":   {"u-task-ops", "task-ops", globalPerms("tasks", "read", "create", "update", "delete")},
		"observer":  {"u-observer", "observer", append(append(globalPerms("activity", "read"), globalPerms("support", "read", "create")...), append(globalPerms("servers", "read"), globalPerms("nodes", "read")...)...)},
		"nodeAdmin": {"u-node-admin", "node-admin", globalPerms("nodes", "read", "create", "update", "delete")},
		"minecraft": {"u-minecraft", "minecraft", nil},
		"deny":      {"u-deny", "deny", globalPerms("players", "read")},
	}
	for name, tc := range tokens {
		raw := seedToken(t, store, enforcer, tc.user, tc.role, tc.perms)
		clients, err := NewFamilyServerClients(ts.Client(), ts.URL, raw)
		if err != nil {
			t.Fatalf("NewFamilyServerClients(%s): %v", name, err)
		}
		srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "fam-test-" + name, Version: "v0"}, nil)
		RegisterFamilyServerTools(srv, clients)
		f.sessions[name] = connectStdioSession(t, srv, ctx)
	}
	f.sessions["invalid"] = func() *sdkmcp.ClientSession {
		clients, err := NewFamilyServerClients(ts.Client(), ts.URL, "dp_nonexistent_token")
		if err != nil {
			t.Fatal(err)
		}
		srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "fam-test-invalid", Version: "v0"}, nil)
		RegisterFamilyServerTools(srv, clients)
		return connectStdioSession(t, srv, ctx)
	}()
	return f
}

type famCase struct {
	family string
	tool   string
	args   map[string]any
	allow  string
	scope  bool // whether a scoped PermissionDenied case applies
}

func famCases() []famCase {
	return []famCase{
		// ServerService — 13
		{"server", "get_server_logs", map[string]any{"id": "srv-1", "tail": 10}, "serverOps", true},
		{"server", "clear_server_logs", map[string]any{"id": "srv-1", "confirm": true}, "serverOps", true},
		{"server", "get_next_available_port", map[string]any{}, "serverOps", true},
		{"server", "export_server_archive", map[string]any{"server_id": "srv-1", "format": "zip"}, "serverOps", true},
		{"server", "preview_server_archive", map[string]any{"upload_id": "upload-1"}, "serverOps", true},
		{"server", "create_server", map[string]any{"name": "srv-2", "mc_version": "1.21.4", "mod_loader": "fabric", "request_id": "req-1", "confirm": true}, "serverOps", true},
		{"server", "update_server", map[string]any{"id": "srv-1", "name": "renamed", "confirm": true}, "serverOps", true},
		{"server", "delete_server", map[string]any{"id": "srv-1", "keep_data": true, "confirm": true}, "serverOps", true},
		{"server", "recreate_server", map[string]any{"id": "srv-1", "confirm": true}, "serverOps", true},
		{"server", "pause_server", map[string]any{"id": "srv-1", "request_id": "req-pause-1", "confirm": true}, "serverOps", true},
		{"server", "deep_sleep_server", map[string]any{"id": "srv-1", "request_id": "req-sleep-1", "confirm": true}, "serverOps", true},
		{"server", "send_command", map[string]any{"id": "srv-1", "command": "list", "confirm": true}, "serverOps", true},
		{"server", "upload_to_mclogs", map[string]any{"id": "srv-1", "confirm": true}, "serverOps", true},
		{"server", "migrate_server", map[string]any{"id": "srv-1", "target_node_id": "node-1", "request_id": "req-2", "confirm": true}, "serverOps", true},
		{"server", "list_server_players", map[string]any{"server_id": "srv-1"}, "serverOps", true},
		// ConfigService — 8
		{"config", "get_server_config", map[string]any{"server_id": "srv-1"}, "configOps", true},
		{"config", "update_server_config", map[string]any{"server_id": "srv-1", "updates": map[string]string{"MOTD": "Welcome"}, "stage": true, "confirm": true}, "configOps", true},
		{"config", "get_update_batch", map[string]any{"server_id": "srv-1"}, "configOps", true},
		{"config", "discard_update_batch", map[string]any{"server_id": "srv-1", "confirm": true}, "configOps", true},
		{"config", "get_global_settings", map[string]any{}, "configOps", true},
		{"config", "update_global_settings", map[string]any{"updates": map[string]string{"DEFAULT_MOTD": "Welcome"}, "confirm": true}, "configOps", true},
		{"config", "sync_global_settings_to_servers", map[string]any{"ops_and_whitelist_only": true, "confirm": true}, "configOps", true},
		{"config", "validate_provider_key", map[string]any{"provider": "curseforge", "api_key": famProbeAPIKey, "confirm": true}, "configOps", true},
		// TaskService — 12
		{"task", "list_tasks", map[string]any{"server_id": "srv-1"}, "taskOps", true},
		{"task", "get_task", map[string]any{"id": "task-1"}, "taskOps", true},
		{"task", "create_task", map[string]any{"server_id": "srv-1", "name": "nightly", "task_type": "backup", "schedule": "cron", "cron_expr": "0 3 * * *", "confirm": true}, "taskOps", true},
		{"task", "update_task", map[string]any{"id": "task-1", "name": "renamed", "confirm": true}, "taskOps", true},
		{"task", "delete_task", map[string]any{"id": "task-1", "confirm": true}, "taskOps", true},
		{"task", "toggle_task", map[string]any{"id": "task-1", "status": "enabled", "confirm": true}, "taskOps", true},
		{"task", "trigger_task", map[string]any{"id": "task-1", "confirm": true}, "taskOps", true},
		{"task", "list_task_executions", map[string]any{"task_id": "task-1", "limit": 10}, "taskOps", true},
		{"task", "list_server_executions", map[string]any{"server_id": "srv-1", "limit": 10}, "taskOps", true},
		{"task", "get_task_execution", map[string]any{"id": "exec-1"}, "taskOps", true},
		{"task", "cancel_execution", map[string]any{"id": "exec-1", "confirm": true}, "taskOps", true},
		{"task", "get_scheduler_status", map[string]any{}, "taskOps", true},
		// ActivityService — 1
		{"activity", "list_activity_logs", map[string]any{"server_id": "srv-1", "limit": 10}, "observer", true},
		// OperationService — 3
		{"operation", "list_operations", map[string]any{"server_id": "srv-1", "limit": 10}, "serverOps", true},
		{"operation", "get_operation", map[string]any{"server_id": "srv-1", "operation_id": "op-1"}, "serverOps", true},
		{"operation", "resolve_operation", map[string]any{"server_id": "srv-1", "operation_id": "op-1", "confirm": true}, "serverOps", true},
		// BlueprintService — 2
		{"blueprint", "list_blueprints", map[string]any{}, "observer", true},
		{"blueprint", "get_blueprint", map[string]any{"id": "bp-1"}, "observer", true},
		// MinecraftService — 4 (authenticated-only: no object scope)
		{"minecraft", "get_minecraft_versions", map[string]any{}, "minecraft", false},
		{"minecraft", "get_mod_loaders", map[string]any{}, "minecraft", false},
		{"minecraft", "get_docker_images", map[string]any{}, "minecraft", false},
		{"minecraft", "get_loader_versions", map[string]any{"loader": "fabric", "mc_version": "1.21.4"}, "minecraft", false},
		// SupportService — 4
		{"support", "get_application_logs", map[string]any{"tail": 10}, "observer", true},
		{"support", "generate_support_bundle", map[string]any{"include_logs": true, "confirm": true}, "observer", true},
		{"support", "upload_support_bundle", map[string]any{"issue_description": "server will not start", "confirm": true}, "observer", true},
		{"support", "download_support_bundle", map[string]any{"bundle_id": "bundle-1"}, "observer", true},
		// NodeService — 6 (scan_nodes is the one excluded RPC)
		{"node", "ping_node", map[string]any{"id": "node-1"}, "nodeAdmin", true},
		{"node", "get_node", map[string]any{"id": "node-1"}, "nodeAdmin", true},
		{"node", "list_nodes", map[string]any{}, "nodeAdmin", true},
		{"node", "create_node", map[string]any{"name": "node-2", "host": "tcp://10.0.0.2:2375", "confirm": true}, "nodeAdmin", true},
		{"node", "update_node", map[string]any{"id": "node-1", "name": "renamed", "confirm": true}, "nodeAdmin", true},
		{"node", "delete_node", map[string]any{"id": "node-1", "confirm": true}, "nodeAdmin", true},
	}
}

func TestFamilyServerDescriptorInventory(t *testing.T) {
	if len(FamilyServerDescriptors) != 56 {
		t.Fatalf("listed RPC descriptors = %d, want 56", len(FamilyServerDescriptors))
	}
	included := FamilyServerIncludedDescriptors()
	if len(included) != FamilyServerToolCount || FamilyServerReachableCount() != 55 {
		t.Fatalf("included=%d count=%d reachable=%d, want 55 each", len(included), FamilyServerToolCount, FamilyServerReachableCount())
	}
	excluded := FamilyServerExcludedDescriptors()
	if len(excluded) != FamilyServerExcludedRPCCount || excluded[0].Name != "scan_nodes" {
		t.Fatalf("excluded RPCs = %+v, want only scan_nodes", excluded)
	}
	if excluded[0].ExclusionReason != ExcludeDiscovery || !strings.Contains(excluded[0].ExclusionDetail, "mapping.go:173") {
		t.Fatalf("scan_nodes lacks exact source reason: %+v", excluded[0])
	}
	if len(FamilyServerExcludedSurfaces) != 11 {
		t.Fatalf("excluded surfaces = %d, want 11", len(FamilyServerExcludedSurfaces))
	}
	if got := FamilyServerListedCount(); got != 67 {
		t.Fatalf("listed count = %d, want 67", got)
	}
	if len(famCases()) != FamilyServerReachableCount() {
		t.Fatalf("test cases = %d, reachable tools = %d", len(famCases()), FamilyServerReachableCount())
	}

	seenName, seenProc := map[string]bool{}, map[string]bool{}
	for _, d := range FamilyServerDescriptors {
		if d.Source == "" {
			t.Errorf("%s has no exact file:line source", d.Procedure)
		}
		if seenName[d.Name] || seenProc[d.Procedure] {
			t.Errorf("duplicate descriptor %s / %s", d.Name, d.Procedure)
		}
		seenName[d.Name], seenProc[d.Procedure] = true, true
		if want := expectedScope(d.Procedure); d.Scope != want {
			t.Errorf("%s: scope %q, live rbac %q", d.Procedure, d.Scope, want)
		}
		short := d.Service[strings.LastIndex(d.Service, ".")+1:]
		if d.Client != "New"+short+"Client" || d.Method != d.Procedure[strings.LastIndex(d.Procedure, "/")+1:] {
			t.Errorf("%s: inconsistent client/method tuple", d.Procedure)
		}
		if strings.Contains(d.Procedure, "NodeControlService") || d.Name == "health" {
			t.Errorf("private/health surface declared as a tool: %s", d.Name)
		}
		if d.Class == ClassSafe || d.Class == ClassReview {
			if d.Mutation && !d.Confirm {
				t.Errorf("%s: mutation without confirm gate", d.Name)
			}
			if d.Mutation && !hasBoolArg(d.Args, "confirm") {
				t.Errorf("%s: mutation descriptor lacks confirm argument", d.Name)
			}
		}
	}
	for _, c := range famCases() {
		if _, ok := FamilyServerDescriptor(c.tool); !ok {
			t.Errorf("test case tool %s is not a declared descriptor", c.tool)
		}
	}
	for _, s := range FamilyServerExcludedSurfaces {
		if s.Source == "" {
			t.Errorf("surface %s has no exact source", s.Surface)
		}
	}
}

func hasBoolArg(args []FamilyServerArg, name string) bool {
	for _, a := range args {
		if a.Name == name && a.Kind == FamilyArgBool {
			return true
		}
	}
	return false
}

func TestFamilyServerAllReachableToolsVerified(t *testing.T) {
	f := newFamilyFixture(t)
	verified := 0
	for _, tc := range famCases() {
		t.Run(tc.family+"/"+tc.tool+"/success", func(t *testing.T) {
			assertSuccess(t, callTool(t, f.sessions[tc.allow], tc.tool, tc.args), tc.tool)
		})
		verified++
	}
	t.Logf("VERIFIED_REACHABLE_TOOLS=%d", verified)
	if verified != FamilyServerReachableCount() {
		t.Fatalf("verified %d of %d reachable tools", verified, FamilyServerReachableCount())
	}
}

func TestFamilyServerScopedPermissionDenied(t *testing.T) {
	f := newFamilyFixture(t)
	for _, tc := range famCases() {
		if !tc.scope {
			continue
		}
		t.Run(tc.family+"/"+tc.tool+"/permission_denied", func(t *testing.T) {
			e := assertStructuredError(t, callTool(t, f.sessions["deny"], tc.tool, tc.args), tc.tool, ErrKindPermissionDenied)
			d, ok := FamilyServerDescriptor(tc.tool)
			if !ok {
				t.Fatalf("no descriptor for %s", tc.tool)
			}
			if e.Tool != tc.tool || e.Procedure != d.Procedure || e.Code != "permission_denied" {
				t.Fatalf("%s: incomplete structured denial: %+v", tc.tool, e)
			}
		})
	}
}

func TestFamilyServerAuthenticatedOnlyHasNoObjectScope(t *testing.T) {
	f := newFamilyFixture(t)
	for _, tc := range famCases() {
		if tc.scope {
			continue
		}
		t.Run(tc.tool+"/valid_token_succeeds", func(t *testing.T) {
			// A valid token with unrelated grants is enough for an
			// authenticated-only RPC; there is no object scope to deny.
			assertSuccess(t, callTool(t, f.sessions["deny"], tc.tool, tc.args), tc.tool)
		})
	}
}

func TestFamilyServerUnauthenticated(t *testing.T) {
	f := newFamilyFixture(t)
	for _, tc := range famCases() {
		t.Run(tc.family+"/"+tc.tool+"/unauthenticated", func(t *testing.T) {
			e := assertStructuredError(t, callTool(t, f.sessions["invalid"], tc.tool, tc.args), tc.tool, ErrKindUnauthenticated)
			if e.Code != "unauthenticated" || e.Procedure == "" {
				t.Fatalf("%s: incomplete unauthenticated error: %+v", tc.tool, e)
			}
		})
	}
}

func TestFamilyServerConfirmationAndBounds(t *testing.T) {
	f := newFamilyFixture(t)
	negatives := []struct {
		tool string
		args map[string]any
		kind string
	}{
		{"update_server", map[string]any{"id": "srv-1", "name": "x"}, ErrKindConfirmationRequired},
		{"delete_server", map[string]any{"id": "srv-1", "confirm": true}, ErrKindInvalidArguments},
		{"send_command", map[string]any{"id": "srv-1", "command": "rm -rf /", "confirm": true}, ErrKindInvalidArguments},
		{"get_server_logs", map[string]any{"id": "srv-1", "tail": 5000}, ErrKindInvalidArguments},
		{"list_task_executions", map[string]any{"task_id": "task-1", "limit": 5000}, ErrKindInvalidArguments},
		{"update_server_config", map[string]any{"server_id": "srv-1", "updates": map[string]string{"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6", "g": "7", "h": "8", "i": "9", "j": "10", "k": "11", "l": "12", "m": "13", "n": "14", "o": "15", "p": "16", "q": "17"}, "confirm": true}, ErrKindInvalidArguments},
		{"create_node", map[string]any{"name": "n", "host": strings.Repeat("h", 600), "confirm": true}, ErrKindInvalidArguments},
	}
	for _, n := range negatives {
		t.Run(n.tool+"/negative", func(t *testing.T) {
			_ = assertStructuredError(t, callTool(t, f.sessions["serverOps"], n.tool, n.args), n.tool, n.kind)
		})
	}
}

func famResultJSON(t *testing.T, out ToolOutput) string {
	t.Helper()
	raw, err := json.Marshal(out.Result)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestFamilyServerRedaction(t *testing.T) {
	f := newFamilyFixture(t)
	for _, tc := range famCases() {
		out := callTool(t, f.sessions[tc.allow], tc.tool, tc.args)
		assertSuccess(t, out, tc.tool)
		got := famResultJSON(t, out)
		for _, secret := range []string{famSecretPassword, famSecretWebhook, famSecretToken, famSecretTLSKey, famProbeAPIKey} {
			if strings.Contains(got, secret) {
				t.Errorf("%s leaked %q in result: %s", tc.tool, secret, got)
			}
		}
		switch tc.tool {
		case "get_server_config", "update_server_config", "get_global_settings", "update_global_settings":
			if !strings.Contains(got, "[REDACTED]") || !strings.Contains(got, "Welcome") {
				t.Errorf("%s: redaction missing or over-broad: %s", tc.tool, got)
			}
		case "get_node", "list_nodes", "create_node", "update_node":
			if !strings.Contains(got, "node-1") {
				t.Errorf("%s: non-secret node identity was removed: %s", tc.tool, got)
			}
		}
	}
}

func TestFamilyServerRESTAndProtocolSurfacesListed(t *testing.T) {
	want := map[string]string{
		"/api/v2/metrics/*":                    "servers/read",
		"/api/v1/settings/validate-key":        "settings/read",
		"/api/v1/staged-config/*":              "server_config/read|create",
		"/api/v1/nodes/scan":                   "nodes/read",
		"/carbonpanel.v1.NodeControlService/*": "",
	}
	found := map[string]bool{}
	for _, s := range FamilyServerExcludedSurfaces {
		if scope, ok := want[s.Surface]; ok {
			found[s.Surface] = true
			if s.Scope != scope {
				t.Errorf("surface %s scope %q, want %q", s.Surface, s.Scope, scope)
			}
			if s.Equivalent == "" || !strings.Contains(s.Source, ":") {
				t.Errorf("surface %s lacks equivalent or exact source", s.Surface)
			}
		}
	}
	for surface := range want {
		if !found[surface] {
			t.Errorf("REST/protocol surface %s is not listed", surface)
		}
	}
}
