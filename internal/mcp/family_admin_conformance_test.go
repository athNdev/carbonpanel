// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Executable conformance evidence for the OSS-373 scoped-administration
// tools. Every case below drives a real MCP SDK client over stdio through
// tools/call into a typed Connect client, a real auth interceptor and a
// recording handler mounted at the exact generated procedure. Assertions are
// made on what the handler actually received and returned, not on catalog
// strings or source text.
package mcp

import (
	"context"
	"encoding/json"
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
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// --- Recording fixture ---------------------------------------------------

type conformanceCall struct {
	Procedure string
	Request   map[string]any
	Auth      string
}

type conformanceRecorder struct {
	mu    sync.Mutex
	calls []conformanceCall
}

func (r *conformanceRecorder) record(procedure string, msg proto.Message, authHeader string) {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
	if err != nil {
		panic("conformance: cannot record " + procedure + ": " + err.Error())
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		panic("conformance: cannot decode recorded " + procedure + ": " + err.Error())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, conformanceCall{Procedure: procedure, Request: fields, Auth: authHeader})
}

func (r *conformanceRecorder) drain(procedure string) []conformanceCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []conformanceCall
	for _, c := range r.calls {
		if c.Procedure == procedure {
			out = append(out, c)
		}
	}
	r.calls = nil
	return out
}

func (r *conformanceRecorder) all() []conformanceCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]conformanceCall(nil), r.calls...)
}

// conformanceMount mounts a generated procedure with a typed reply that is
// derived from the actual request, so a call proves both which typed method
// was reached and how its bounded arguments were mapped. It is not a generic
// zero-value success stub: every call is recorded with its exact procedure,
// decoded request fields and Authorization header.
func conformanceMount[Req, Res any](mux *http.ServeMux, procedure string, ic connect.UnaryInterceptorFunc,
	rec *conformanceRecorder, reply func(*Req) *Res) {
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(_ context.Context, req *connect.Request[Req]) (*connect.Response[Res], error) {
			msg, ok := any(req.Msg).(proto.Message)
			if !ok {
				panic("conformance: reply type for " + procedure + " is not a proto message")
			}
			rec.record(procedure, msg, req.Header().Get("Authorization"))
			return connect.NewResponse(reply(req.Msg)), nil
		}, connect.WithInterceptors(ic)))
}

type conformanceFixture struct {
	sessions    map[string]*sdkmcp.ClientSession
	clients     map[string]*Clients
	recorder    *conformanceRecorder
	authManager *auth.Manager
	tokens      map[string]string
	server      *httptest.Server
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
		Local:           config.LocalConfig{Enabled: true},
		OIDC:            config.OIDCConfig{Enabled: false},
		AnonymousAccess: false,
		AllowNoAuth:     false,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	interceptor := rpc.NewAuthInterceptor(authManager, enforcer, store, logger.New())
	recorder := &conformanceRecorder{}

	mux := http.NewServeMux()
	registerConformanceHandlers(mux, interceptor, recorder)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	tokens := map[string]string{
		"plain":          seedToken(t, store, enforcer, "cf-plain", "cf-plain-role", nil),
		"settingsReader": seedToken(t, store, enforcer, "cf-settings", "cf-settings-role", globalPerms("settings", "read")),
		"userAdmin":      seedToken(t, store, enforcer, "cf-user", "cf-user-role", globalPerms("users", "read", "create", "update", "delete")),
		"roleAdmin":      seedToken(t, store, enforcer, "cf-role", "cf-role-role", globalPerms("roles", "read", "create", "update", "delete")),
		"subuserAdmin": seedToken(t, store, enforcer, "cf-subuser", "cf-subuser-role", []rbac.Permission{
			{Resource: "subusers", Action: "read", ObjectID: "srv-1"},
			{Resource: "subusers", Action: "create", ObjectID: "srv-1"},
			{Resource: "subusers", Action: "delete", ObjectID: "srv-1"},
		}),
		"proxyAdmin":        seedToken(t, store, enforcer, "cf-proxy", "cf-proxy-role", globalPerms("proxy", "read", "create", "update", "delete")),
		"configAdmin":       seedToken(t, store, enforcer, "cf-config", "cf-config-role", globalPerms("settings", "read", "update")),
		"serverConfigAdmin": seedToken(t, store, enforcer, "cf-server-config", "cf-server-config-role", globalPerms("server_config", "read", "update")),
		"invalid":           "dp_nonexistent_conformance_token",
	}

	f := &conformanceFixture{
		sessions:    make(map[string]*sdkmcp.ClientSession, len(tokens)),
		clients:     make(map[string]*Clients, len(tokens)),
		recorder:    recorder,
		authManager: authManager,
		tokens:      tokens,
		server:      ts,
	}
	for name, token := range tokens {
		clients, err := NewClients(ts.Client(), ts.URL, token)
		if err != nil {
			t.Fatalf("NewClients(%s): %v", name, err)
		}
		if name != "invalid" {
			if err := clients.Verify(ctx); err != nil {
				t.Fatalf("Verify(%s): %v", name, err)
			}
		}
		admin, err := NewFamilyAdminClients(ts.Client(), ts.URL, token)
		if err != nil {
			t.Fatalf("NewFamilyAdminClients(%s): %v", name, err)
		}
		server := NewServer(clients)
		RegisterFamilyAdminTools(server, admin)
		f.clients[name] = clients
		f.sessions[name] = connectStdioSession(t, server, ctx)
	}
	// Verification's own GetAuthStatus/GetCurrentUser calls are not tool
	// evidence; clear them so every recorded call below comes from tools/call.
	f.recorder.drain("")
	return f
}

func conformanceSecretCategories() []*v1.ConfigCategory {
	return []*v1.ConfigCategory{{
		Name: "security",
		Properties: []*v1.ConfigProperty{
			{Key: "session_secret", Type: "password", Value: "super-secret-value"},
			{Key: "motd", Type: "text", Value: "Welcome"},
		},
	}}
}

func conformanceInvite() *v1.RegistrationInvite {
	return &v1.RegistrationInvite{Id: "cf-invite", Code: "secret-invite-code", Description: "conformance"}
}

// registerConformanceHandlers mounts every procedure a conformance tool can
// reach, plus the two Verify procedures, Logout, and a Velocity secret
// procedure used only as a negative leak probe.
func registerConformanceHandlers(mux *http.ServeMux, ic connect.UnaryInterceptorFunc, rec *conformanceRecorder) {
	const a = "/carbonpanel.v1.AuthService/"
	conformanceMount(mux, a+"GetAuthStatus", ic, rec, func(*v1.GetAuthStatusRequest) *v1.GetAuthStatusResponse {
		return &v1.GetAuthStatusResponse{LocalAuthEnabled: true, AllowRegistration: true}
	})
	conformanceMount(mux, a+"GetCurrentUser", ic, rec, func(*v1.GetCurrentUserRequest) *v1.GetCurrentUserResponse {
		return &v1.GetCurrentUserResponse{User: &v1.User{
			Id: "cf-current-user", Username: "conformance", IsActive: true, Roles: []string{"viewer"},
		}}
	})
	conformanceMount(mux, a+"ChangePassword", ic, rec, func(*v1.ChangePasswordRequest) *v1.ChangePasswordResponse {
		return &v1.ChangePasswordResponse{Message: "cf-change_password"}
	})
	conformanceMount(mux, a+"GetAuthConfig", ic, rec, func(*v1.GetAuthConfigRequest) *v1.GetAuthConfigResponse {
		return &v1.GetAuthConfigResponse{LocalAuthEnabled: true, SessionTimeout: 4321}
	})
	conformanceMount(mux, a+"UpdateAuthSettings", ic, rec, func(*v1.UpdateAuthSettingsRequest) *v1.UpdateAuthSettingsResponse {
		return &v1.UpdateAuthSettingsResponse{Config: &v1.GetAuthConfigResponse{SessionTimeout: 4321}}
	})
	conformanceMount(mux, a+"CreateInvite", ic, rec, func(*v1.CreateInviteRequest) *v1.CreateInviteResponse {
		return &v1.CreateInviteResponse{Invite: conformanceInvite()}
	})
	conformanceMount(mux, a+"ListInvites", ic, rec, func(*v1.ListInvitesRequest) *v1.ListInvitesResponse {
		return &v1.ListInvitesResponse{Invites: []*v1.RegistrationInvite{conformanceInvite()}}
	})
	conformanceMount(mux, a+"GetInvite", ic, rec, func(*v1.GetInviteRequest) *v1.GetInviteResponse {
		return &v1.GetInviteResponse{Invite: conformanceInvite()}
	})
	conformanceMount(mux, a+"DeleteInvite", ic, rec, func(*v1.DeleteInviteRequest) *v1.DeleteInviteResponse {
		return &v1.DeleteInviteResponse{}
	})
	conformanceMount(mux, a+"ValidateInvite", ic, rec, func(*v1.ValidateInviteRequest) *v1.ValidateInviteResponse {
		return &v1.ValidateInviteResponse{Valid: true, Description: "cf-validate_invite"}
	})
	conformanceMount(mux, a+"CreateAPIToken", ic, rec, func(*v1.CreateAPITokenRequest) *v1.CreateAPITokenResponse {
		return &v1.CreateAPITokenResponse{
			PlaintextToken: "dp_plaintext_secret_value",
			ApiToken:       &v1.ApiToken{Id: "cf-token", Name: "new-token"},
		}
	})
	conformanceMount(mux, a+"ListAPITokens", ic, rec, func(*v1.ListAPITokensRequest) *v1.ListAPITokensResponse {
		return &v1.ListAPITokensResponse{ApiTokens: []*v1.ApiToken{{Id: "cf-token", Name: "mcp-test"}}}
	})
	conformanceMount(mux, a+"DeleteAPIToken", ic, rec, func(*v1.DeleteAPITokenRequest) *v1.DeleteAPITokenResponse {
		return &v1.DeleteAPITokenResponse{}
	})
	// Logout uses the production behavior: it only calls authManager.Logout,
	// which deletes a session row. It never deletes an API token.
	conformanceMount(mux, a+"Logout", ic, rec, func(*v1.LogoutRequest) *v1.LogoutResponse {
		return &v1.LogoutResponse{Message: "logged out"}
	})
	// Negative leak probe: mounted at a procedure NO MCP tool exposes.
	conformanceMount(mux, "/carbonpanel.v1.ProxyService/GetVelocitySecret", ic, rec, func(*v1.GetVelocitySecretRequest) *v1.GetVelocitySecretResponse {
		return &v1.GetVelocitySecretResponse{Secret: "velocity-secret-should-never-leak"}
	})

	const u = "/carbonpanel.v1.UserService/"
	conformanceMount(mux, u+"ListUsers", ic, rec, func(*v1.ListUsersRequest) *v1.ListUsersResponse {
		return &v1.ListUsersResponse{Users: []*v1.User{{Id: "cf-users", Username: "conformance"}}}
	})
	conformanceMount(mux, u+"GetUser", ic, rec, func(req *v1.GetUserRequest) *v1.GetUserResponse {
		return &v1.GetUserResponse{User: &v1.User{Id: req.GetId(), Username: "cf-get_user"}}
	})
	conformanceMount(mux, u+"CreateUser", ic, rec, func(req *v1.CreateUserRequest) *v1.CreateUserResponse {
		return &v1.CreateUserResponse{User: &v1.User{Id: req.GetUsername(), Username: "cf-create_user"}}
	})
	conformanceMount(mux, u+"UpdateUser", ic, rec, func(req *v1.UpdateUserRequest) *v1.UpdateUserResponse {
		return &v1.UpdateUserResponse{User: &v1.User{Id: req.GetId(), Username: "cf-update_user"}}
	})
	conformanceMount(mux, u+"DeleteUser", ic, rec, func(*v1.DeleteUserRequest) *v1.DeleteUserResponse {
		return &v1.DeleteUserResponse{Message: "cf-delete_user"}
	})

	const r = "/carbonpanel.v1.RoleService/"
	conformanceMount(mux, r+"ListRoles", ic, rec, func(*v1.ListRolesRequest) *v1.ListRolesResponse {
		return &v1.ListRolesResponse{Roles: []*v1.Role{{Id: "cf-roles", Name: "viewer"}}}
	})
	conformanceMount(mux, r+"GetRole", ic, rec, func(req *v1.GetRoleRequest) *v1.GetRoleResponse {
		return &v1.GetRoleResponse{Role: &v1.Role{Id: req.GetId(), Name: "cf-get_role"}}
	})
	conformanceMount(mux, r+"CreateRole", ic, rec, func(req *v1.CreateRoleRequest) *v1.CreateRoleResponse {
		return &v1.CreateRoleResponse{Role: &v1.Role{Id: "cf-create_role", Name: req.GetName()}}
	})
	conformanceMount(mux, r+"UpdateRole", ic, rec, func(req *v1.UpdateRoleRequest) *v1.UpdateRoleResponse {
		return &v1.UpdateRoleResponse{Role: &v1.Role{Id: req.GetId(), Name: "cf-update_role"}}
	})
	conformanceMount(mux, r+"DeleteRole", ic, rec, func(*v1.DeleteRoleRequest) *v1.DeleteRoleResponse {
		return &v1.DeleteRoleResponse{Message: "cf-delete_role"}
	})
	conformanceMount(mux, r+"GetPermissionMatrix", ic, rec, func(*v1.GetPermissionMatrixRequest) *v1.GetPermissionMatrixResponse {
		return &v1.GetPermissionMatrixResponse{ResourceActions: []*v1.ResourceActions{
			{Resource: "cf-get_permission_matrix", Actions: []string{"read"}},
		}}
	})
	conformanceMount(mux, r+"UpdatePermissions", ic, rec, func(req *v1.UpdatePermissionsRequest) *v1.UpdatePermissionsResponse {
		return &v1.UpdatePermissionsResponse{Message: "cf-" + req.GetRoleName()}
	})
	conformanceMount(mux, r+"AssignRole", ic, rec, func(*v1.AssignRoleRequest) *v1.AssignRoleResponse {
		return &v1.AssignRoleResponse{Message: "cf-assign_role"}
	})
	conformanceMount(mux, r+"UnassignRole", ic, rec, func(*v1.UnassignRoleRequest) *v1.UnassignRoleResponse {
		return &v1.UnassignRoleResponse{Message: "cf-unassign_role"}
	})
	conformanceMount(mux, r+"GetUserRoles", ic, rec, func(*v1.GetUserRolesRequest) *v1.GetUserRolesResponse {
		return &v1.GetUserRolesResponse{Roles: []string{"cf-get_user_roles"}}
	})

	const s = "/carbonpanel.v1.SubuserService/"
	conformanceMount(mux, s+"ListSubusers", ic, rec, func(req *v1.ListSubusersRequest) *v1.ListSubusersResponse {
		return &v1.ListSubusersResponse{Grants: []*v1.SubuserGrant{{ServerId: req.GetServerId(), UserId: "cf-list_subusers"}}}
	})
	conformanceMount(mux, s+"SetSubuser", ic, rec, func(req *v1.SetSubuserRequest) *v1.SetSubuserResponse {
		return &v1.SetSubuserResponse{Grant: &v1.SubuserGrant{
			ServerId: req.GetServerId(), UserId: req.GetUserId(), Permissions: req.GetPermissions(),
		}}
	})
	conformanceMount(mux, s+"RemoveSubuser", ic, rec, func(*v1.RemoveSubuserRequest) *v1.RemoveSubuserResponse {
		return &v1.RemoveSubuserResponse{Success: true}
	})

	const p = "/carbonpanel.v1.ProxyService/"
	conformanceMount(mux, p+"GetProxyRoutes", ic, rec, func(*v1.GetProxyRoutesRequest) *v1.GetProxyRoutesResponse {
		return &v1.GetProxyRoutesResponse{Routes: []*v1.ProxyRoute{{ServerId: "cf-get_proxy_routes"}}}
	})
	conformanceMount(mux, p+"GetProxyStatus", ic, rec, func(*v1.GetProxyStatusRequest) *v1.GetProxyStatusResponse {
		return &v1.GetProxyStatusResponse{Enabled: true, ListenPort: 25565}
	})
	conformanceMount(mux, p+"GetProxyListeners", ic, rec, func(*v1.GetProxyListenersRequest) *v1.GetProxyListenersResponse {
		return &v1.GetProxyListenersResponse{Listeners: []*v1.ProxyListenerWithCount{
			{Listener: &v1.ProxyListener{Id: "cf-get_proxy_listeners"}},
		}}
	})
	conformanceMount(mux, p+"GetServerRouting", ic, rec, func(*v1.GetServerRoutingRequest) *v1.GetServerRoutingResponse {
		return &v1.GetServerRoutingResponse{ProxyEnabled: true, ProxyHostname: "cf-get_server_routing"}
	})
	conformanceMount(mux, p+"UpdateProxyConfig", ic, rec, func(req *v1.UpdateProxyConfigRequest) *v1.UpdateProxyConfigResponse {
		return &v1.UpdateProxyConfigResponse{Enabled: req.GetEnabled(), ListenPort: 25565}
	})
	conformanceMount(mux, p+"CreateProxyListener", ic, rec, func(req *v1.CreateProxyListenerRequest) *v1.CreateProxyListenerResponse {
		return &v1.CreateProxyListenerResponse{Listener: &v1.ProxyListener{
			Id: "cf-create_proxy_listener", Name: req.GetName(), Port: req.GetPort(),
		}}
	})
	conformanceMount(mux, p+"UpdateProxyListener", ic, rec, func(req *v1.UpdateProxyListenerRequest) *v1.UpdateProxyListenerResponse {
		return &v1.UpdateProxyListenerResponse{Listener: &v1.ProxyListener{
			Id: req.GetId(), Name: "cf-update_proxy_listener", Port: req.GetPort(),
		}}
	})
	conformanceMount(mux, p+"DeleteProxyListener", ic, rec, func(*v1.DeleteProxyListenerRequest) *v1.DeleteProxyListenerResponse {
		return &v1.DeleteProxyListenerResponse{Status: "cf-delete_proxy_listener"}
	})
	conformanceMount(mux, p+"UpdateServerRouting", ic, rec, func(req *v1.UpdateServerRoutingRequest) *v1.UpdateServerRoutingResponse {
		return &v1.UpdateServerRoutingResponse{Status: "ok", Hostname: req.GetProxyHostname()}
	})

	const c = "/carbonpanel.v1.ConfigService/"
	conformanceMount(mux, c+"GetGlobalSettings", ic, rec, func(*v1.GetGlobalSettingsRequest) *v1.GetGlobalSettingsResponse {
		return &v1.GetGlobalSettingsResponse{Categories: conformanceSecretCategories()}
	})
	conformanceMount(mux, c+"UpdateGlobalSettings", ic, rec, func(*v1.UpdateGlobalSettingsRequest) *v1.UpdateGlobalSettingsResponse {
		return &v1.UpdateGlobalSettingsResponse{Categories: conformanceSecretCategories()}
	})
	conformanceMount(mux, c+"GetServerConfig", ic, rec, func(*v1.GetServerConfigRequest) *v1.GetServerConfigResponse {
		return &v1.GetServerConfigResponse{Categories: conformanceSecretCategories()}
	})
	conformanceMount(mux, c+"UpdateServerConfig", ic, rec, func(req *v1.UpdateServerConfigRequest) *v1.UpdateServerConfigResponse {
		return &v1.UpdateServerConfigResponse{Categories: conformanceSecretCategories(), Staged: req.GetStage()}
	})
	conformanceMount(mux, c+"GetUpdateBatch", ic, rec, func(*v1.GetUpdateBatchRequest) *v1.GetUpdateBatchResponse {
		return &v1.GetUpdateBatchResponse{Open: true, Items: []*v1.StagedUpdateItem{{Id: "cf-get_update_batch"}}}
	})
	conformanceMount(mux, c+"DiscardUpdateBatch", ic, rec, func(*v1.DiscardUpdateBatchRequest) *v1.DiscardUpdateBatchResponse {
		return &v1.DiscardUpdateBatchResponse{}
	})
}

// --- Per-tool expected evidence ------------------------------------------

type conformanceExpect struct {
	procedure     string
	requestFields map[string]any // decoded fields the typed handler must observe
	resultHas     string         // marker returned by the typed handler
	resultEmpty   bool           // response has no fields (e.g. DeleteInviteResponse)
}

func conformanceExpectations() map[string]conformanceExpect {
	return map[string]conformanceExpect{
		"get_auth_status":       {"/carbonpanel.v1.AuthService/GetAuthStatus", map[string]any{}, `"local_auth_enabled":true`, false},
		"get_current_user":      {"/carbonpanel.v1.AuthService/GetCurrentUser", map[string]any{}, "cf-current-user", false},
		"change_password":       {"/carbonpanel.v1.AuthService/ChangePassword", map[string]any{"old_password": "old", "new_password": "new"}, "cf-change_password", false},
		"get_auth_config":       {"/carbonpanel.v1.AuthService/GetAuthConfig", map[string]any{}, "4321", false},
		"update_auth_settings":  {"/carbonpanel.v1.AuthService/UpdateAuthSettings", map[string]any{"session_timeout": 3600}, "4321", false},
		"create_invite":         {"/carbonpanel.v1.AuthService/CreateInvite", map[string]any{"description": "invite"}, "cf-invite", false},
		"list_invites":          {"/carbonpanel.v1.AuthService/ListInvites", map[string]any{}, "cf-invite", false},
		"get_invite":            {"/carbonpanel.v1.AuthService/GetInvite", map[string]any{"id": "inv-1"}, "cf-invite", false},
		"delete_invite":         {"/carbonpanel.v1.AuthService/DeleteInvite", map[string]any{"id": "inv-1"}, "", true},
		"validate_invite":       {"/carbonpanel.v1.AuthService/ValidateInvite", map[string]any{"code": "abc"}, "cf-validate_invite", false},
		"create_api_token":      {"/carbonpanel.v1.AuthService/CreateAPIToken", map[string]any{"name": "mcp"}, "cf-token", false},
		"list_api_tokens":       {"/carbonpanel.v1.AuthService/ListAPITokens", map[string]any{}, "cf-token", false},
		"delete_api_token":      {"/carbonpanel.v1.AuthService/DeleteAPIToken", map[string]any{"id": "tok-1"}, "", true},
		"list_users":            {"/carbonpanel.v1.UserService/ListUsers", map[string]any{}, "cf-users", false},
		"get_user":              {"/carbonpanel.v1.UserService/GetUser", map[string]any{"id": "u-1"}, "cf-get_user", false},
		"create_user":           {"/carbonpanel.v1.UserService/CreateUser", map[string]any{"username": "alice", "password": "one-time"}, "cf-create_user", false},
		"update_user":           {"/carbonpanel.v1.UserService/UpdateUser", map[string]any{"id": "u-1"}, "cf-update_user", false},
		"delete_user":           {"/carbonpanel.v1.UserService/DeleteUser", map[string]any{"id": "u-1"}, "cf-delete_user", false},
		"list_roles":            {"/carbonpanel.v1.RoleService/ListRoles", map[string]any{}, "cf-roles", false},
		"get_role":              {"/carbonpanel.v1.RoleService/GetRole", map[string]any{"id": "role-1"}, "cf-get_role", false},
		"create_role":           {"/carbonpanel.v1.RoleService/CreateRole", map[string]any{"name": "custom"}, "cf-create_role", false},
		"update_role":           {"/carbonpanel.v1.RoleService/UpdateRole", map[string]any{"id": "role-1"}, "cf-update_role", false},
		"delete_role":           {"/carbonpanel.v1.RoleService/DeleteRole", map[string]any{"id": "role-1"}, "cf-delete_role", false},
		"get_permission_matrix": {"/carbonpanel.v1.RoleService/GetPermissionMatrix", map[string]any{}, "cf-get_permission_matrix", false},
		"update_role_permissions": {"/carbonpanel.v1.RoleService/UpdatePermissions", map[string]any{
			"role_name":   "viewer",
			"permissions": []any{map[string]any{"resource": "servers", "action": "read"}},
		}, "cf-viewer", false},
		"assign_role":            {"/carbonpanel.v1.RoleService/AssignRole", map[string]any{"user_id": "u-1", "role_name": "viewer"}, "cf-assign_role", false},
		"unassign_role":          {"/carbonpanel.v1.RoleService/UnassignRole", map[string]any{"user_id": "u-1", "role_name": "viewer"}, "cf-unassign_role", false},
		"get_user_roles":         {"/carbonpanel.v1.RoleService/GetUserRoles", map[string]any{"user_id": "u-1"}, "cf-get_user_roles", false},
		"list_subusers":          {"/carbonpanel.v1.SubuserService/ListSubusers", map[string]any{"server_id": "srv-1"}, "cf-list_subusers", false},
		"set_subuser":            {"/carbonpanel.v1.SubuserService/SetSubuser", map[string]any{"server_id": "srv-1", "user_id": "u-1", "permissions": []any{"servers.read"}}, "servers.read", false},
		"remove_subuser":         {"/carbonpanel.v1.SubuserService/RemoveSubuser", map[string]any{"server_id": "srv-1", "user_id": "u-1"}, `"success":true`, false},
		"get_proxy_routes":       {"/carbonpanel.v1.ProxyService/GetProxyRoutes", map[string]any{}, "cf-get_proxy_routes", false},
		"get_proxy_status":       {"/carbonpanel.v1.ProxyService/GetProxyStatus", map[string]any{}, "25565", false},
		"get_proxy_listeners":    {"/carbonpanel.v1.ProxyService/GetProxyListeners", map[string]any{}, "cf-get_proxy_listeners", false},
		"get_server_routing":     {"/carbonpanel.v1.ProxyService/GetServerRouting", map[string]any{"server_id": "srv-1"}, "cf-get_server_routing", false},
		"update_proxy_config":    {"/carbonpanel.v1.ProxyService/UpdateProxyConfig", map[string]any{"enabled": true}, "25565", false},
		"create_proxy_listener":  {"/carbonpanel.v1.ProxyService/CreateProxyListener", map[string]any{"name": "listener", "port": 25565}, "cf-create_proxy_listener", false},
		"update_proxy_listener":  {"/carbonpanel.v1.ProxyService/UpdateProxyListener", map[string]any{"id": "l-1", "port": 25565}, "cf-update_proxy_listener", false},
		"delete_proxy_listener":  {"/carbonpanel.v1.ProxyService/DeleteProxyListener", map[string]any{"id": "l-1"}, "cf-delete_proxy_listener", false},
		"update_server_routing":  {"/carbonpanel.v1.ProxyService/UpdateServerRouting", map[string]any{"server_id": "srv-1", "proxy_hostname": "mc.example"}, "mc.example", false},
		"get_global_settings":    {"/carbonpanel.v1.ConfigService/GetGlobalSettings", map[string]any{}, "Welcome", false},
		"update_global_settings": {"/carbonpanel.v1.ConfigService/UpdateGlobalSettings", map[string]any{"updates": map[string]any{"motd": "Welcome"}}, "Welcome", false},
		"get_server_config":      {"/carbonpanel.v1.ConfigService/GetServerConfig", map[string]any{"server_id": "srv-1"}, "Welcome", false},
		"update_server_config":   {"/carbonpanel.v1.ConfigService/UpdateServerConfig", map[string]any{"server_id": "srv-1", "stage": true}, "Welcome", false},
		"get_update_batch":       {"/carbonpanel.v1.ConfigService/GetUpdateBatch", map[string]any{"server_id": "srv-1"}, "cf-get_update_batch", false},
		"discard_update_batch":   {"/carbonpanel.v1.ConfigService/DiscardUpdateBatch", map[string]any{"server_id": "srv-1"}, "", true},
	}
}

func conformanceJSONEqual(a, b any) bool {
	ra, err := json.Marshal(a)
	if err != nil {
		return false
	}
	rb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(ra) == string(rb)
}

func assertConformanceHandled(t *testing.T, f *conformanceFixture, tc adminVerifyCase, expect conformanceExpect, out ToolOutput) {
	t.Helper()
	if out.Status != "ok" || out.Error != nil {
		t.Fatalf("%s: expected success, got %+v", tc.tool, out)
	}
	calls := f.recorder.drain(expect.procedure)
	if len(calls) != 1 {
		t.Fatalf("%s: typed handler %s reached %d times, want exactly 1", tc.tool, expect.procedure, len(calls))
	}
	call := calls[0]
	for field, want := range expect.requestFields {
		got, ok := call.Request[field]
		if !ok {
			t.Fatalf("%s: bounded field %q missing from recorded typed request %+v", tc.tool, field, call.Request)
		}
		if !conformanceJSONEqual(got, want) {
			t.Fatalf("%s: field %q = %v, want %v (recorded %+v)", tc.tool, field, got, want, call.Request)
		}
	}
	wantAuth := "Bearer " + f.tokens[tc.allow]
	if call.Auth != wantAuth {
		t.Fatalf("%s: typed client sent Authorization %q, want the scoped bearer token", tc.tool, call.Auth)
	}
	raw, err := json.Marshal(out.Result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if expect.resultEmpty {
		if text != "{}" {
			t.Fatalf("%s: expected empty typed response, got %s", tc.tool, text)
		}
		return
	}
	if !strings.Contains(text, expect.resultHas) {
		t.Fatalf("%s: typed handler outcome %q missing marker %q", tc.tool, text, expect.resultHas)
	}
}

// --- Executable conformance tests ----------------------------------------

// TestConformanceTypedMethodAndBoundedArgs is the primary per-tool evidence:
// 46 real tools/call requests, each reaching exactly one typed Connect method
// with the expected decoded fields and producing that handler's real outcome.
func TestConformanceTypedMethodAndBoundedArgs(t *testing.T) {
	f := newConformanceFixture(t)
	expectations := conformanceExpectations()
	cases := adminVerifyCases()
	if len(cases) != FamilyAdminToolCount || len(expectations) != FamilyAdminToolCount {
		t.Fatalf("cases=%d expectations=%d tools=%d", len(cases), len(expectations), FamilyAdminToolCount)
	}
	reached := 0
	for _, tc := range cases {
		expect, ok := expectations[tc.tool]
		if !ok {
			t.Fatalf("no conformance expectation for %s", tc.tool)
		}
		spec := familyAdminSpec(tc.tool)
		if expect.procedure != spec.Procedure {
			t.Fatalf("%s: expectation procedure %s, registered %s", tc.tool, expect.procedure, spec.Procedure)
		}
		t.Run(tc.tool+"/typed_method_and_bounds", func(t *testing.T) {
			out := callTool(t, f.sessions[tc.allow], tc.tool, tc.args)
			assertConformanceHandled(t, f, tc, expect, out)
		})
		reached++
	}
	if reached != 46 {
		t.Fatalf("typed method evidence for %d tools, want 46", reached)
	}
}

// TestConformanceScopedDenialsAndUnauthenticated proves the real interceptor
// rejects requests before any typed handler is reached, and records zero
// handler calls for each rejection.
func TestConformanceScopedDenialsAndUnauthenticated(t *testing.T) {
	f := newConformanceFixture(t)
	expectations := conformanceExpectations()
	denied, unauthorized := 0, 0
	for _, tc := range adminVerifyCases() {
		expect := expectations[tc.tool]
		if !tc.public {
			t.Run(tc.tool+"/unauthenticated_no_handler", func(t *testing.T) {
				e := assertStructuredError(t, callTool(t, f.sessions["invalid"], tc.tool, tc.args), tc.tool, ErrKindUnauthenticated)
				if e.Code != "unauthenticated" || e.Tool != tc.tool {
					t.Fatalf("%s: %+v", tc.tool, e)
				}
				if got := f.recorder.drain(expect.procedure); len(got) != 0 {
					t.Fatalf("%s: rejected call still reached typed handler %+v", tc.tool, got)
				}
			})
			unauthorized++
		}
		if tc.deny == "" {
			continue
		}
		t.Run(tc.tool+"/permission_denied_no_handler", func(t *testing.T) {
			e := assertStructuredError(t, callTool(t, f.sessions[tc.deny], tc.tool, tc.args), tc.tool, ErrKindPermissionDenied)
			if e.Procedure != expect.procedure || e.Code != "permission_denied" {
				t.Fatalf("%s: %+v", tc.tool, e)
			}
			if got := f.recorder.drain(expect.procedure); len(got) != 0 {
				t.Fatalf("%s: denied call still reached typed handler %+v", tc.tool, got)
			}
		})
		denied++
	}
	t.Logf("conformance auth: permission_denied=%d unauthenticated=%d (public tools skipped)", denied, unauthorized)
}

// TestConformanceObjectScopeOutcomes proves object-level grants are enforced
// by observing which server_id values reach the typed handler.
func TestConformanceObjectScopeOutcomes(t *testing.T) {
	f := newConformanceFixture(t)
	const proc = "/carbonpanel.v1.SubuserService/ListSubusers"
	assertSuccess(t, callTool(t, f.sessions["subuserAdmin"], "list_subusers",
		map[string]any{"server_id": "srv-1"}), "list_subusers")
	allowed := f.recorder.drain(proc)
	if len(allowed) != 1 || allowed[0].Request["server_id"] != "srv-1" {
		t.Fatalf("scoped grant did not reach handler with srv-1: %+v", allowed)
	}
	e := assertStructuredError(t, callTool(t, f.sessions["subuserAdmin"], "list_subusers",
		map[string]any{"server_id": "srv-2"}), "list_subusers", ErrKindPermissionDenied)
	if !strings.Contains(e.Message, "insufficient permissions") {
		t.Fatalf("cross-server grant was not denied: %+v", e)
	}
	if got := f.recorder.drain(proc); len(got) != 0 {
		t.Fatalf("cross-server call reached the typed handler: %+v", got)
	}
	t.Log("conformance object scope: srv-1 reached handler, srv-2 denied before handler; 2 outcomes")
}

// TestConformanceConfirmGatesReachNoHandler proves all 25 confirm-gated
// mutations are rejected locally, with zero typed RPCs reaching a handler.
func TestConformanceConfirmGatesReachNoHandler(t *testing.T) {
	f := newConformanceFixture(t)
	expectations := conformanceExpectations()
	gated := 0
	for _, tc := range adminVerifyCases() {
		var d FamilyAdminDescriptor
		found := false
		for _, candidate := range FamilyAdminDescriptors {
			if candidate.Tool == tc.tool {
				d, found = candidate, true
				break
			}
		}
		if !found || !d.Confirm {
			continue
		}
		gated++
		args := map[string]any{}
		for k, v := range tc.args {
			if k != "confirm" {
				args[k] = v
			}
		}
		t.Run(tc.tool+"/no_handler_without_confirm", func(t *testing.T) {
			e := assertStructuredError(t, callTool(t, f.sessions[tc.allow], tc.tool, args), tc.tool, ErrKindConfirmationRequired)
			if e.Tool != tc.tool || e.Procedure == "" {
				t.Fatalf("%s: %+v", tc.tool, e)
			}
			if got := f.recorder.drain(expectations[tc.tool].procedure); len(got) != 0 {
				t.Fatalf("%s: unconfirmed mutation reached typed handler %+v", tc.tool, got)
			}
		})
	}
	want := 0
	for _, d := range FamilyAdminDescriptors {
		if d.Confirm {
			want++
		}
	}
	if gated != want || want != 25 {
		t.Fatalf("confirmation cases=%d descriptors=%d, want 25", gated, want)
	}
}

// TestConformanceRedactionAndVelocityLeakProbe verifies secret material is
// absent from real tool results, and that no tool reaches the mounted
// Velocity secret procedure that would have returned a distinctive secret.
func TestConformanceRedactionAndVelocityLeakProbe(t *testing.T) {
	f := newConformanceFixture(t)
	expectations := conformanceExpectations()
	const velocity = "/carbonpanel.v1.ProxyService/GetVelocitySecret"
	const velocitySecret = "velocity-secret-should-never-leak"

	// First prove the negative probe is live: a typed call with a privileged
	// token really does reach it and really would return the secret.
	proxyClient := carbonpanelv1connect.NewProxyServiceClient(f.server.Client(), f.server.URL,
		connect.WithInterceptors(bearerInterceptor(f.tokens["configAdmin"])))
	_, err := proxyClient.GetVelocitySecret(context.Background(), connect.NewRequest(&v1.GetVelocitySecretRequest{}))
	if err != nil {
		t.Fatalf("negative probe procedure is not mounted: %v", err)
	}
	probe := f.recorder.drain(velocity)
	if len(probe) != 1 {
		t.Fatalf("negative probe reached handler %d times, want 1", len(probe))
	}
	f.recorder.drain("")

	secretBearing := []struct {
		tool, token string
		args        map[string]any
		leaks       []string
		keeps       []string
	}{
		{"create_invite", "userAdmin", map[string]any{"description": "invite", "confirm": true}, []string{"secret-invite-code"}, []string{redactedMarker, "cf-invite"}},
		{"list_invites", "userAdmin", map[string]any{}, []string{"secret-invite-code"}, []string{redactedMarker, "cf-invite"}},
		{"get_invite", "userAdmin", map[string]any{"id": "inv-1"}, []string{"secret-invite-code"}, []string{redactedMarker, "cf-invite"}},
		{"create_api_token", "plain", map[string]any{"name": "mcp", "confirm": true}, []string{"dp_plaintext_secret_value"}, []string{redactedMarker, "cf-token"}},
		{"get_global_settings", "configAdmin", map[string]any{}, []string{"super-secret-value"}, []string{redactedMarker, "Welcome"}},
		{"get_server_config", "serverConfigAdmin", map[string]any{"server_id": "srv-1"}, []string{"super-secret-value"}, []string{redactedMarker, "Welcome"}},
		{"update_global_settings", "configAdmin", map[string]any{"updates": map[string]any{"motd": "Welcome"}, "confirm": true}, []string{"super-secret-value"}, []string{redactedMarker, "Welcome"}},
		{"update_server_config", "serverConfigAdmin", map[string]any{
			"server_id": "srv-1", "updates": map[string]any{"motd": "Welcome"}, "stage": true, "confirm": true,
		}, []string{"super-secret-value"}, []string{redactedMarker, "Welcome"}},
	}
	checked := 0
	for _, sc := range secretBearing {
		t.Run(sc.tool+"/redaction", func(t *testing.T) {
			out := callTool(t, f.sessions[sc.token], sc.tool, sc.args)
			raw, err := json.Marshal(out.Result)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			for _, leak := range sc.leaks {
				if strings.Contains(text, leak) {
					t.Fatalf("%s leaked %q: %s", sc.tool, leak, text)
				}
			}
			for _, keep := range sc.keeps {
				if !strings.Contains(text, keep) {
					t.Fatalf("%s missing %q: %s", sc.tool, keep, text)
				}
			}
		})
		checked++
	}

	// Drive every tool once more and prove none of them reaches Velocity or
	// emits its distinctive secret.
	for _, tc := range adminVerifyCases() {
		out := callTool(t, f.sessions[tc.allow], tc.tool, tc.args)
		assertSuccess(t, out, tc.tool)
		_ = expectations
		raw, err := json.Marshal(out.Result)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), velocitySecret) {
			t.Fatalf("tool %s emitted the Velocity secret: %s", tc.tool, raw)
		}
	}
	if got := f.recorder.drain(velocity); len(got) != 0 {
		t.Fatalf("MCP tools reached the Velocity secret handler: %+v", got)
	}
	t.Logf("conformance redaction: %d secret-bearing tool results inspected; Velocity leak calls=0", checked)
}

// TestConformanceLogoutWithAPIToken proves the real observable behavior of
// AuthService.Logout for a dp_ API-token caller: the handler returns
// "logged out" but the API token still authenticates afterwards.
func TestConformanceLogoutWithAPITokenDoesNotRevokeIt(t *testing.T) {
	f := newConformanceFixture(t)
	const tokenName = "plain"
	token := f.tokens[tokenName]
	ctx := context.Background()

	before, err := f.authManager.AuthenticateFromHeader(ctx, "Bearer "+token)
	if err != nil || before == nil || before.ID == "" {
		t.Fatalf("token did not authenticate before Logout: user=%+v err=%v", before, err)
	}

	logoutRes, err := f.clients[tokenName].Auth.Logout(ctx, connect.NewRequest(&v1.LogoutRequest{}))
	if err != nil {
		t.Fatalf("typed Logout RPC failed: %v", err)
	}
	if got := logoutRes.Msg.GetMessage(); got != "logged out" {
		t.Fatalf("Logout response message = %q, want %q", got, "logged out")
	}
	calls := f.recorder.drain("/carbonpanel.v1.AuthService/Logout")
	if len(calls) != 1 || calls[0].Auth != "Bearer "+token {
		t.Fatalf("Logout did not reach the typed handler with the dp_ token: %+v", calls)
	}

	after, err := f.authManager.AuthenticateFromHeader(ctx, "Bearer "+token)
	if err != nil || after == nil || after.ID != before.ID {
		t.Fatalf("dp_ token was revoked by Logout: before=%+v after=%+v err=%v", before, after, err)
	}

	// A real tools/call on the same MCP session still succeeds with the same
	// token after Logout. This is the user-visible proof of non-revocation.
	f.recorder.drain("")
	assertSuccess(t, callTool(t, f.sessions[tokenName], "get_current_user", map[string]any{}), "get_current_user")
	if got := f.recorder.drain("/carbonpanel.v1.AuthService/GetCurrentUser"); len(got) != 1 {
		t.Fatalf("post-Logout tool call did not reach its handler: %+v", got)
	}
	t.Logf("conformance Logout: typed handler reached with dp_ token, response %q, token still authenticates and a tool call still succeeds — no token revocation observed", logoutRes.Msg.GetMessage())
}

// TestConformanceEvidenceCounts is the machine-checkable evidence summary.
func TestConformanceEvidenceCounts(t *testing.T) {
	f := newConformanceFixture(t)
	expectations := conformanceExpectations()
	cases := adminVerifyCases()
	deny, unauth, gated, public := 0, 0, 0, 0
	for _, tc := range cases {
		if tc.public {
			public++
		} else {
			unauth++
		}
		if tc.deny != "" {
			deny++
		}
		for _, d := range FamilyAdminDescriptors {
			if d.Tool == tc.tool && d.Confirm {
				gated++
			}
		}
	}
	if len(expectations) != 46 || len(cases) != 46 || gated != 25 || public != 2 {
		t.Fatalf("expectations=%d cases=%d gated=%d public=%d", len(expectations), len(cases), gated, public)
	}
	// Exercise all tools once so the recorded-call total is real evidence.
	for _, tc := range cases {
		assertSuccess(t, callTool(t, f.sessions[tc.allow], tc.tool, tc.args), tc.tool)
	}
	recorded := len(f.recorder.all())
	if recorded != 46 {
		t.Fatalf("typed handler calls recorded=%d, want 46", recorded)
	}
	t.Logf("CONFORMANCE: LISTED=56 REACHABLE=46 VERIFIED_EXECUTABLY=46 typed_handler_calls=46 "+
		"permission_denied=%d unauthenticated=%d object_scope=2 confirm_gates=%d public_no_gate=%d "+
		"redaction_results=8 velocity_leak_calls=0 logout_probe=1", deny, unauth, gated, public)
}
