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

// registerAdminOK mounts one generated procedure behind the production auth
// interceptor with a disposable, in-memory reply. No controller, tenant,
// Docker daemon, world or database state is contacted.
func registerAdminOK[Req, Res any](mux *http.ServeMux, procedure string,
	ic connect.UnaryInterceptorFunc, populate func(*Res)) {
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error) {
			var res Res
			if populate != nil {
				populate(&res)
			}
			return connect.NewResponse(&res), nil
		}, connect.WithInterceptors(ic)))
}

func adminSecretCategories() []*v1.ConfigCategory {
	return []*v1.ConfigCategory{{
		Name: "security",
		Properties: []*v1.ConfigProperty{
			{Key: "session_secret", Label: "Session secret", Type: "password", Value: "super-secret-value"},
			{Key: "motd", Label: "MOTD", Type: "text", Value: "Welcome"},
		},
	}}
}

func adminGlobalSettingsWithSecret(v *v1.GetGlobalSettingsResponse) {
	v.Categories = adminSecretCategories()
}

func adminServerConfigWithSecret(v *v1.GetServerConfigResponse) {
	v.Categories = adminSecretCategories()
}

func adminUpdatedServerConfigWithSecret(v *v1.UpdateServerConfigResponse) {
	v.Categories = adminSecretCategories()
	v.Staged = true
}

func adminTokenMetadata(v *v1.ListAPITokensResponse) {
	v.ApiTokens = []*v1.ApiToken{{Id: "tok-1", Name: "mcp-test"}}
}

func adminInviteProto() *v1.RegistrationInvite {
	return &v1.RegistrationInvite{Id: "inv-1", Code: "secret-invite-code", Description: "test invite"}
}

func adminCreatedInvite(v *v1.CreateInviteResponse) { v.Invite = adminInviteProto() }
func adminListedInvites(v *v1.ListInvitesResponse) {
	v.Invites = []*v1.RegistrationInvite{adminInviteProto()}
}
func adminGotInvite(v *v1.GetInviteResponse) { v.Invite = adminInviteProto() }

func adminPlaintextToken(v *v1.CreateAPITokenResponse) {
	v.PlaintextToken = "dp_plaintext_secret_value"
	v.ApiToken = &v1.ApiToken{Id: "tok-2", Name: "new-token"}
}

// registerFamilyAdminHandlers mounts every procedure exposed by this slice.
func registerFamilyAdminHandlers(mux *http.ServeMux, ic connect.UnaryInterceptorFunc) {
	// AuthService. GetAuthStatus and GetCurrentUser are already mounted by
	// registerAuthHandlers in mcp_test.go; reuse those real handlers so the
	// fixture exercises the same Verify path as the first-slice tests.
	registerAdminOK[v1.GetAuthConfigRequest, v1.GetAuthConfigResponse](mux, "/carbonpanel.v1.AuthService/GetAuthConfig", ic, nil)
	registerAdminOK[v1.ChangePasswordRequest, v1.ChangePasswordResponse](mux, "/carbonpanel.v1.AuthService/ChangePassword", ic, nil)
	registerAdminOK[v1.UpdateAuthSettingsRequest, v1.UpdateAuthSettingsResponse](mux, "/carbonpanel.v1.AuthService/UpdateAuthSettings", ic, nil)
	registerAdminOK[v1.CreateInviteRequest, v1.CreateInviteResponse](mux, "/carbonpanel.v1.AuthService/CreateInvite", ic, adminCreatedInvite)
	registerAdminOK[v1.ListInvitesRequest, v1.ListInvitesResponse](mux, "/carbonpanel.v1.AuthService/ListInvites", ic, adminListedInvites)
	registerAdminOK[v1.GetInviteRequest, v1.GetInviteResponse](mux, "/carbonpanel.v1.AuthService/GetInvite", ic, adminGotInvite)
	registerAdminOK[v1.DeleteInviteRequest, v1.DeleteInviteResponse](mux, "/carbonpanel.v1.AuthService/DeleteInvite", ic, nil)
	registerAdminOK[v1.ValidateInviteRequest, v1.ValidateInviteResponse](mux, "/carbonpanel.v1.AuthService/ValidateInvite", ic, nil)
	registerAdminOK[v1.CreateAPITokenRequest, v1.CreateAPITokenResponse](mux, "/carbonpanel.v1.AuthService/CreateAPIToken", ic, adminPlaintextToken)
	registerAdminOK[v1.ListAPITokensRequest, v1.ListAPITokensResponse](mux, "/carbonpanel.v1.AuthService/ListAPITokens", ic, adminTokenMetadata)
	registerAdminOK[v1.DeleteAPITokenRequest, v1.DeleteAPITokenResponse](mux, "/carbonpanel.v1.AuthService/DeleteAPIToken", ic, nil)

	// UserService.
	registerAdminOK[v1.ListUsersRequest, v1.ListUsersResponse](mux, "/carbonpanel.v1.UserService/ListUsers", ic, nil)
	registerAdminOK[v1.GetUserRequest, v1.GetUserResponse](mux, "/carbonpanel.v1.UserService/GetUser", ic, nil)
	registerAdminOK[v1.CreateUserRequest, v1.CreateUserResponse](mux, "/carbonpanel.v1.UserService/CreateUser", ic, nil)
	registerAdminOK[v1.UpdateUserRequest, v1.UpdateUserResponse](mux, "/carbonpanel.v1.UserService/UpdateUser", ic, nil)
	registerAdminOK[v1.DeleteUserRequest, v1.DeleteUserResponse](mux, "/carbonpanel.v1.UserService/DeleteUser", ic, nil)

	// RoleService.
	registerAdminOK[v1.ListRolesRequest, v1.ListRolesResponse](mux, "/carbonpanel.v1.RoleService/ListRoles", ic, nil)
	registerAdminOK[v1.GetRoleRequest, v1.GetRoleResponse](mux, "/carbonpanel.v1.RoleService/GetRole", ic, nil)
	registerAdminOK[v1.CreateRoleRequest, v1.CreateRoleResponse](mux, "/carbonpanel.v1.RoleService/CreateRole", ic, nil)
	registerAdminOK[v1.UpdateRoleRequest, v1.UpdateRoleResponse](mux, "/carbonpanel.v1.RoleService/UpdateRole", ic, nil)
	registerAdminOK[v1.DeleteRoleRequest, v1.DeleteRoleResponse](mux, "/carbonpanel.v1.RoleService/DeleteRole", ic, nil)
	registerAdminOK[v1.GetPermissionMatrixRequest, v1.GetPermissionMatrixResponse](mux, "/carbonpanel.v1.RoleService/GetPermissionMatrix", ic, nil)
	registerAdminOK[v1.UpdatePermissionsRequest, v1.UpdatePermissionsResponse](mux, "/carbonpanel.v1.RoleService/UpdatePermissions", ic, nil)
	registerAdminOK[v1.AssignRoleRequest, v1.AssignRoleResponse](mux, "/carbonpanel.v1.RoleService/AssignRole", ic, nil)
	registerAdminOK[v1.UnassignRoleRequest, v1.UnassignRoleResponse](mux, "/carbonpanel.v1.RoleService/UnassignRole", ic, nil)
	registerAdminOK[v1.GetUserRolesRequest, v1.GetUserRolesResponse](mux, "/carbonpanel.v1.RoleService/GetUserRoles", ic, nil)

	// SubuserService.
	registerAdminOK[v1.ListSubusersRequest, v1.ListSubusersResponse](mux, "/carbonpanel.v1.SubuserService/ListSubusers", ic, nil)
	registerAdminOK[v1.SetSubuserRequest, v1.SetSubuserResponse](mux, "/carbonpanel.v1.SubuserService/SetSubuser", ic, nil)
	registerAdminOK[v1.RemoveSubuserRequest, v1.RemoveSubuserResponse](mux, "/carbonpanel.v1.SubuserService/RemoveSubuser", ic, nil)

	// ProxyService.
	registerAdminOK[v1.GetProxyRoutesRequest, v1.GetProxyRoutesResponse](mux, "/carbonpanel.v1.ProxyService/GetProxyRoutes", ic, nil)
	registerAdminOK[v1.GetProxyStatusRequest, v1.GetProxyStatusResponse](mux, "/carbonpanel.v1.ProxyService/GetProxyStatus", ic, nil)
	registerAdminOK[v1.GetProxyListenersRequest, v1.GetProxyListenersResponse](mux, "/carbonpanel.v1.ProxyService/GetProxyListeners", ic, nil)
	registerAdminOK[v1.GetServerRoutingRequest, v1.GetServerRoutingResponse](mux, "/carbonpanel.v1.ProxyService/GetServerRouting", ic, nil)
	registerAdminOK[v1.UpdateProxyConfigRequest, v1.UpdateProxyConfigResponse](mux, "/carbonpanel.v1.ProxyService/UpdateProxyConfig", ic, nil)
	registerAdminOK[v1.CreateProxyListenerRequest, v1.CreateProxyListenerResponse](mux, "/carbonpanel.v1.ProxyService/CreateProxyListener", ic, nil)
	registerAdminOK[v1.UpdateProxyListenerRequest, v1.UpdateProxyListenerResponse](mux, "/carbonpanel.v1.ProxyService/UpdateProxyListener", ic, nil)
	registerAdminOK[v1.DeleteProxyListenerRequest, v1.DeleteProxyListenerResponse](mux, "/carbonpanel.v1.ProxyService/DeleteProxyListener", ic, nil)
	registerAdminOK[v1.UpdateServerRoutingRequest, v1.UpdateServerRoutingResponse](mux, "/carbonpanel.v1.ProxyService/UpdateServerRouting", ic, nil)

	// ConfigService global settings.
	registerAdminOK[v1.GetGlobalSettingsRequest, v1.GetGlobalSettingsResponse](mux, "/carbonpanel.v1.ConfigService/GetGlobalSettings", ic, adminGlobalSettingsWithSecret)
	registerAdminOK[v1.UpdateGlobalSettingsRequest, v1.UpdateGlobalSettingsResponse](mux, "/carbonpanel.v1.ConfigService/UpdateGlobalSettings", ic, nil)
	registerAdminOK[v1.GetServerConfigRequest, v1.GetServerConfigResponse](mux, "/carbonpanel.v1.ConfigService/GetServerConfig", ic, adminServerConfigWithSecret)
	registerAdminOK[v1.UpdateServerConfigRequest, v1.UpdateServerConfigResponse](mux, "/carbonpanel.v1.ConfigService/UpdateServerConfig", ic, adminUpdatedServerConfigWithSecret)
	registerAdminOK[v1.GetUpdateBatchRequest, v1.GetUpdateBatchResponse](mux, "/carbonpanel.v1.ConfigService/GetUpdateBatch", ic, nil)
	registerAdminOK[v1.DiscardUpdateBatchRequest, v1.DiscardUpdateBatchResponse](mux, "/carbonpanel.v1.ConfigService/DiscardUpdateBatch", ic, nil)
}

type adminFixture struct {
	sessions map[string]*sdkmcp.ClientSession
	server   *httptest.Server
}

// newAdminFixture builds real SDK MCP clients over stdio pipes, real typed
// Connect clients with scoped bearer tokens and disposable httptest handlers
// behind the exact production auth interceptor.
func newAdminFixture(t *testing.T) *adminFixture {
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
	registerAuthHandlers(mux, interceptor, true)
	registerFamilyAdminHandlers(mux, interceptor)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	scopedSubusers := []rbac.Permission{
		{Resource: "subusers", Action: "read", ObjectID: "srv-1"},
		{Resource: "subusers", Action: "create", ObjectID: "srv-1"},
		{Resource: "subusers", Action: "delete", ObjectID: "srv-1"},
	}
	tokens := map[string]string{
		"plain":             seedToken(t, store, enforcer, "u-plain", "plain-user", nil),
		"settingsReader":    seedToken(t, store, enforcer, "u-settings", "settings-reader", globalPerms("settings", "read")),
		"userAdmin":         seedToken(t, store, enforcer, "u-user-admin", "family-user-admin", globalPerms("users", "read", "create", "update", "delete")),
		"roleAdmin":         seedToken(t, store, enforcer, "u-role-admin", "family-role-admin", globalPerms("roles", "read", "create", "update", "delete")),
		"subuserAdmin":      seedToken(t, store, enforcer, "u-subuser-admin", "family-subuser-admin", scopedSubusers),
		"proxyAdmin":        seedToken(t, store, enforcer, "u-proxy-admin", "family-proxy-admin", globalPerms("proxy", "read", "create", "update", "delete")),
		"configAdmin":       seedToken(t, store, enforcer, "u-config-admin", "family-config-admin", globalPerms("settings", "read", "update")),
		"serverConfigAdmin": seedToken(t, store, enforcer, "u-server-config-admin", "family-server-config-admin", globalPerms("server_config", "read", "update")),
		"invalid":           "dp_nonexistent_token",
	}

	f := &adminFixture{sessions: make(map[string]*sdkmcp.ClientSession, len(tokens)), server: ts}
	for name, token := range tokens {
		base, err := NewClients(ts.Client(), ts.URL, token)
		if err != nil {
			t.Fatalf("NewClients(%s): %v", name, err)
		}
		if name != "invalid" {
			if err := base.Verify(ctx); err != nil {
				t.Fatalf("Verify(%s): %v", name, err)
			}
		}
		admin, err := NewFamilyAdminClients(ts.Client(), ts.URL, token)
		if err != nil {
			t.Fatalf("NewFamilyAdminClients(%s): %v", name, err)
		}
		server := NewServer(base)
		RegisterFamilyAdminTools(server, admin)
		f.sessions[name] = connectStdioSession(t, server, ctx)
	}
	return f
}

// adminVerifyCase is one VERIFIED tool: a scoped success plus, where the
// procedure has an authorization gate, a structured denial and an
// unauthenticated rejection. Public procedures have no gate to deny.
type adminVerifyCase struct {
	tool   string
	args   map[string]any
	allow  string
	deny   string // empty for public and authenticated-only procedures
	public bool   // public procedures skip both auth checks
}

func adminVerifyCases() []adminVerifyCase {
	return []adminVerifyCase{
		// AuthService: 13 reachable.
		{"get_auth_status", map[string]any{}, "plain", "", true},
		{"get_current_user", map[string]any{}, "plain", "", false},
		{"change_password", map[string]any{"old_password": "old", "new_password": "new", "confirm": true}, "plain", "", false},
		{"get_auth_config", map[string]any{}, "settingsReader", "userAdmin", false},
		{"update_auth_settings", map[string]any{"session_timeout": 3600, "confirm": true}, "configAdmin", "settingsReader", false},
		{"create_invite", map[string]any{"description": "invite", "confirm": true}, "userAdmin", "settingsReader", false},
		{"list_invites", map[string]any{}, "userAdmin", "settingsReader", false},
		{"get_invite", map[string]any{"id": "inv-1"}, "userAdmin", "settingsReader", false},
		{"delete_invite", map[string]any{"id": "inv-1", "confirm": true}, "userAdmin", "settingsReader", false},
		{"validate_invite", map[string]any{"code": "abc"}, "plain", "", true},
		{"create_api_token", map[string]any{"name": "mcp", "confirm": true}, "plain", "", false},
		{"list_api_tokens", map[string]any{}, "plain", "", false},
		{"delete_api_token", map[string]any{"id": "tok-1", "confirm": true}, "plain", "", false},

		// UserService: 5 reachable.
		{"list_users", map[string]any{}, "userAdmin", "roleAdmin", false},
		{"get_user", map[string]any{"id": "u-1"}, "userAdmin", "roleAdmin", false},
		{"create_user", map[string]any{"username": "alice", "password": "one-time", "confirm": true}, "userAdmin", "roleAdmin", false},
		{"update_user", map[string]any{"id": "u-1", "set_is_active": true, "is_active": false, "confirm": true}, "userAdmin", "roleAdmin", false},
		{"delete_user", map[string]any{"id": "u-1", "confirm": true}, "userAdmin", "roleAdmin", false},

		// RoleService: 10 reachable.
		{"list_roles", map[string]any{}, "roleAdmin", "userAdmin", false},
		{"get_role", map[string]any{"id": "role-1"}, "roleAdmin", "userAdmin", false},
		{"create_role", map[string]any{"name": "custom", "confirm": true}, "roleAdmin", "userAdmin", false},
		{"update_role", map[string]any{"id": "role-1", "confirm": true}, "roleAdmin", "userAdmin", false},
		{"delete_role", map[string]any{"id": "role-1", "confirm": true}, "roleAdmin", "userAdmin", false},
		{"get_permission_matrix", map[string]any{}, "roleAdmin", "userAdmin", false},
		{"update_role_permissions", map[string]any{
			"role_name":   "viewer",
			"permissions": []any{map[string]any{"resource": "servers", "action": "read"}},
			"confirm":     true,
		}, "roleAdmin", "userAdmin", false},
		{"assign_role", map[string]any{"user_id": "u-1", "role_name": "viewer", "confirm": true}, "roleAdmin", "userAdmin", false},
		{"unassign_role", map[string]any{"user_id": "u-1", "role_name": "viewer", "confirm": true}, "roleAdmin", "userAdmin", false},
		{"get_user_roles", map[string]any{"user_id": "u-1"}, "roleAdmin", "userAdmin", false},

		// SubuserService: 3 reachable, all scoped by server_id.
		{"list_subusers", map[string]any{"server_id": "srv-1"}, "subuserAdmin", "userAdmin", false},
		{"set_subuser", map[string]any{"server_id": "srv-1", "user_id": "u-1", "permissions": []any{"servers.read"}, "confirm": true}, "subuserAdmin", "userAdmin", false},
		{"remove_subuser", map[string]any{"server_id": "srv-1", "user_id": "u-1", "confirm": true}, "subuserAdmin", "userAdmin", false},

		// ProxyService: 9 reachable; all three Velocity secret procedures stay out.
		{"get_proxy_routes", map[string]any{}, "proxyAdmin", "userAdmin", false},
		{"get_proxy_status", map[string]any{}, "proxyAdmin", "userAdmin", false},
		{"get_proxy_listeners", map[string]any{}, "proxyAdmin", "userAdmin", false},
		{"get_server_routing", map[string]any{"server_id": "srv-1"}, "proxyAdmin", "userAdmin", false},
		{"update_proxy_config", map[string]any{"enabled": true, "confirm": true}, "proxyAdmin", "userAdmin", false},
		{"create_proxy_listener", map[string]any{"name": "listener", "port": 25565, "confirm": true}, "proxyAdmin", "userAdmin", false},
		{"update_proxy_listener", map[string]any{"id": "l-1", "port": 25565, "confirm": true}, "proxyAdmin", "userAdmin", false},
		{"delete_proxy_listener", map[string]any{"id": "l-1", "confirm": true}, "proxyAdmin", "userAdmin", false},
		{"update_server_routing", map[string]any{"server_id": "srv-1", "proxy_hostname": "mc.example", "confirm": true}, "proxyAdmin", "userAdmin", false},

		// ConfigService: 6 reachable; bulk sync and provider-key validation stay out.
		{"get_global_settings", map[string]any{}, "configAdmin", "userAdmin", false},
		{"update_global_settings", map[string]any{"updates": map[string]any{"motd": "Welcome"}, "confirm": true}, "configAdmin", "settingsReader", false},
		{"get_server_config", map[string]any{"server_id": "srv-1"}, "serverConfigAdmin", "userAdmin", false},
		{"update_server_config", map[string]any{"server_id": "srv-1", "updates": map[string]any{"motd": "Welcome"}, "stage": true, "confirm": true}, "serverConfigAdmin", "settingsReader", false},
		{"get_update_batch", map[string]any{"server_id": "srv-1"}, "serverConfigAdmin", "userAdmin", false},
		{"discard_update_batch", map[string]any{"server_id": "srv-1", "confirm": true}, "serverConfigAdmin", "settingsReader", false},
	}
}

// TestFamilyAdminEveryToolIsVerified asserts that all 46 REACHABLE tools have
// scoped fixture evidence: success with an allowed token plus structured
// PermissionDenied and Unauthenticated where the procedure has an auth gate.
func TestFamilyAdminEveryToolIsVerified(t *testing.T) {
	f := newAdminFixture(t)
	cases := adminVerifyCases()
	if len(cases) != FamilyAdminVerifiedCount || FamilyAdminVerifiedCount != FamilyAdminToolCount {
		t.Fatalf("verified=%d tool count=%d", len(cases), FamilyAdminToolCount)
	}
	seen := make(map[string]bool, len(cases))
	for _, tc := range cases {
		if seen[tc.tool] {
			t.Fatalf("duplicate verification case for %s", tc.tool)
		}
		seen[tc.tool] = true
		if _, ok := familyAdminSpecs[tc.tool]; !ok {
			t.Fatalf("verification case for unregistered tool %s", tc.tool)
		}
		t.Run(tc.tool+"/success", func(t *testing.T) {
			assertSuccess(t, callTool(t, f.sessions[tc.allow], tc.tool, tc.args), tc.tool)
		})
		if tc.public {
			continue
		}
		t.Run(tc.tool+"/unauthenticated", func(t *testing.T) {
			e := assertStructuredError(t, callTool(t, f.sessions["invalid"], tc.tool, tc.args), tc.tool, ErrKindUnauthenticated)
			if e.Code != "unauthenticated" || e.Tool != tc.tool {
				t.Fatalf("%s: unexpected unauthenticated error: %+v", tc.tool, e)
			}
		})
		if tc.deny == "" {
			continue
		}
		t.Run(tc.tool+"/permission_denied", func(t *testing.T) {
			e := assertStructuredError(t, callTool(t, f.sessions[tc.deny], tc.tool, tc.args), tc.tool, ErrKindPermissionDenied)
			spec := familyAdminSpec(tc.tool)
			if e.Tool != tc.tool || e.Procedure != spec.Procedure || e.Code != "permission_denied" {
				t.Fatalf("%s: incomplete structured error: %+v", tc.tool, e)
			}
		})
	}
	for name := range familyAdminSpecs {
		if !seen[name] {
			t.Errorf("reachable tool %s has no verification case", name)
		}
	}
}

// TestFamilyAdminMutationConfirmationGates derives its cases from the
// descriptors, so every confirm-gated mutation must be exercised without
// confirm and must fail before any RPC is attempted.
func TestFamilyAdminMutationConfirmationGates(t *testing.T) {
	f := newAdminFixture(t)
	gated := 0
	for _, d := range FamilyAdminDescriptors {
		if d.Confirm {
			gated++
		}
	}
	checked := 0
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
		checked++
		t.Run(tc.tool+"/confirmation_required", func(t *testing.T) {
			args := make(map[string]any, len(tc.args))
			for k, v := range tc.args {
				if k != "confirm" {
					args[k] = v
				}
			}
			e := assertStructuredError(t, callTool(t, f.sessions[tc.allow], tc.tool, args), tc.tool, ErrKindConfirmationRequired)
			if e.Tool != tc.tool || e.Procedure == "" {
				t.Fatalf("%s: incomplete confirmation error: %+v", tc.tool, e)
			}
		})
	}
	if checked != gated {
		t.Fatalf("confirmation cases %d, confirm-gated descriptors %d", checked, gated)
	}
}

func TestFamilyAdminSecretRedaction(t *testing.T) {
	f := newAdminFixture(t)

	settings := callTool(t, f.sessions["configAdmin"], "get_global_settings", map[string]any{})
	assertSuccess(t, settings, "get_global_settings")
	raw, err := json.Marshal(settings.Result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "super-secret-value") {
		t.Fatalf("secret value leaked into tool result: %s", text)
	}
	if !strings.Contains(text, redactedMarker) {
		t.Fatalf("password-typed property was not redacted: %s", text)
	}
	if !strings.Contains(text, "Welcome") {
		t.Fatalf("non-secret property was over-redacted: %s", text)
	}

	// Token metadata uses the key "api_tokens" (plural) and must not be
	// confused with a secret-bearing field.
	tokens := callTool(t, f.sessions["plain"], "list_api_tokens", map[string]any{})
	assertSuccess(t, tokens, "list_api_tokens")
	raw, err = json.Marshal(tokens.Result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "tok-1") {
		t.Fatalf("token metadata was incorrectly redacted: %s", raw)
	}

	// Invite codes and one-time API token plaintext must never be emitted.
	invite := callTool(t, f.sessions["userAdmin"], "create_invite", map[string]any{"description": "invite", "confirm": true})
	assertSuccess(t, invite, "create_invite")
	raw, err = json.Marshal(invite.Result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-invite-code") || !strings.Contains(string(raw), redactedMarker) {
		t.Fatalf("invite code was not redacted: %s", raw)
	}
	token := callTool(t, f.sessions["plain"], "create_api_token", map[string]any{"name": "mcp", "confirm": true})
	assertSuccess(t, token, "create_api_token")
	raw, err = json.Marshal(token.Result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "dp_plaintext_secret_value") || !strings.Contains(string(raw), redactedMarker) {
		t.Fatalf("plaintext API token was not redacted: %s", raw)
	}
	if !strings.Contains(string(raw), "tok-2") {
		t.Fatalf("token metadata was over-redacted: %s", raw)
	}
}

func TestFamilyAdminScopedSubuserGrant(t *testing.T) {
	f := newAdminFixture(t)
	assertSuccess(t, callTool(t, f.sessions["subuserAdmin"], "list_subusers",
		map[string]any{"server_id": "srv-1"}), "list_subusers")
	e := assertStructuredError(t, callTool(t, f.sessions["subuserAdmin"], "list_subusers",
		map[string]any{"server_id": "srv-2"}), "list_subusers", ErrKindPermissionDenied)
	if !strings.Contains(e.Message, "insufficient permissions") {
		t.Fatalf("cross-server grant was not denied by RBAC: %+v", e)
	}
}

func TestFamilyAdminBoundedArguments(t *testing.T) {
	f := newAdminFixture(t)
	cases := []struct {
		tool  string
		token string
		args  map[string]any
	}{
		{"list_subusers", "subuserAdmin", map[string]any{"server_id": ""}},
		{"get_user", "userAdmin", map[string]any{"id": strings.Repeat("x", 129)}},
		{"create_user", "userAdmin", map[string]any{
			"username": "bob", "password": strings.Repeat("p", 300), "confirm": true,
		}},
		{"create_proxy_listener", "proxyAdmin", map[string]any{
			"name": "listener", "port": 99999, "confirm": true,
		}},
		{"update_proxy_config", "proxyAdmin", map[string]any{
			"enabled": true, "base_url": "file:///etc/passwd", "confirm": true,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			assertStructuredError(t, callTool(t, f.sessions[tc.token], tc.tool, tc.args), tc.tool, ErrKindInvalidArguments)
		})
	}
	// An oversized updates map is rejected too.
	updates := make(map[string]any, 65)
	for i := 0; i < 65; i++ {
		updates[string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
	}
	assertStructuredError(t, callTool(t, f.sessions["configAdmin"], "update_global_settings",
		map[string]any{"updates": updates, "confirm": true}), "update_global_settings", ErrKindInvalidArguments)
}

func TestFamilyAdminDescriptorsMatchLiveRBAC(t *testing.T) {
	if len(FamilyAdminDescriptors) != FamilyAdminDescriptorCount {
		t.Fatalf("descriptor count %d, want %d", len(FamilyAdminDescriptors), FamilyAdminDescriptorCount)
	}
	seenProcedures := make(map[string]bool, len(FamilyAdminDescriptors))
	seenTools := make(map[string]bool, FamilyAdminToolCount)
	toolCount := 0
	for _, d := range FamilyAdminDescriptors {
		if seenProcedures[d.Procedure] {
			t.Fatalf("duplicate descriptor procedure %s", d.Procedure)
		}
		seenProcedures[d.Procedure] = true
		if want := expectedScope(d.Procedure); d.Scope != want {
			t.Errorf("%s: descriptor scope %q, live rbac scope %q", d.Procedure, d.Scope, want)
		}
		entry, ok := Catalog[d.Procedure]
		if !ok {
			t.Errorf("%s: procedure is not in the generated catalog", d.Procedure)
			continue
		}
		if entry.Service != d.Service || entry.Method != d.Method || entry.Client != d.Client {
			t.Errorf("%s: typed client tuple mismatch: %+v", d.Procedure, entry)
		}
		if d.Class == ClassExclude {
			if d.Tool != "" || d.Confirm || d.Reason == "" || d.SourceReason == "" {
				t.Errorf("%s: excluded descriptor lacks a source-cited reason: %+v", d.Procedure, d)
			}
			continue
		}
		toolCount++
		if d.Class != ClassSafe && d.Class != ClassReview {
			t.Errorf("%s: unexpected class %q", d.Procedure, d.Class)
		}
		if d.Confirm != !d.ReadOnly {
			t.Errorf("%s: read=%v confirm=%v; every mutation must be confirm-gated", d.Procedure, d.ReadOnly, d.Confirm)
		}
		if d.Tool == "" || d.Reason != "" || seenTools[d.Tool] {
			t.Errorf("%s: malformed or duplicate tool name %q", d.Procedure, d.Tool)
		}
		seenTools[d.Tool] = true
		spec, ok := familyAdminSpecs[d.Tool]
		if !ok {
			t.Errorf("%s: tool %s was not registered", d.Procedure, d.Tool)
			continue
		}
		if spec.Procedure != d.Procedure || spec.Class != d.Class || spec.Confirm != d.Confirm {
			t.Errorf("%s: registered spec does not match descriptor: %+v", d.Procedure, spec)
		}
	}
	if toolCount != FamilyAdminToolCount || len(familyAdminSpecs) != FamilyAdminToolCount {
		t.Fatalf("tool count: descriptors=%d registered=%d want=%d", toolCount, len(familyAdminSpecs), FamilyAdminToolCount)
	}
}

func TestFamilyAdminExcludedSurfacesStayExcluded(t *testing.T) {
	excluded := map[string]bool{
		"/carbonpanel.v1.AuthService/Login":                         true,
		"/carbonpanel.v1.AuthService/Logout":                        true,
		"/carbonpanel.v1.AuthService/Register":                      true,
		"/carbonpanel.v1.AuthService/GetOIDCLoginURL":               true,
		"/carbonpanel.v1.AuthService/UseRecoveryKey":                true,
		"/carbonpanel.v1.ProxyService/GetVelocitySecret":            true,
		"/carbonpanel.v1.ProxyService/RotateVelocitySecret":         true,
		"/carbonpanel.v1.ProxyService/SyncVelocitySecretToServer":   true,
		"/carbonpanel.v1.ConfigService/SyncGlobalSettingsToServers": true,
		"/carbonpanel.v1.ConfigService/ValidateProviderKey":         true,
	}
	if len(excluded) != FamilyAdminExcludedCount {
		t.Fatalf("exclusion list has %d entries, constant says %d", len(excluded), FamilyAdminExcludedCount)
	}
	count := 0
	for _, d := range FamilyAdminDescriptors {
		if d.Class != ClassExclude {
			continue
		}
		count++
		if !excluded[d.Procedure] {
			t.Fatalf("unexpected excluded descriptor %s: %+v", d.Procedure, d)
		}
	}
	if count != len(excluded) {
		t.Fatalf("excluded descriptors %d, exclusion list %d", count, len(excluded))
	}
	for procedure := range excluded {
		d, ok := findFamilyAdminDescriptor(procedure)
		if !ok {
			t.Fatalf("excluded procedure %s is missing from the descriptor list", procedure)
		}
		if d.Class != ClassExclude || d.Tool != "" {
			t.Fatalf("excluded procedure %s was promoted to a tool: %+v", procedure, d)
		}
		if d.SourceReason == "" {
			t.Fatalf("excluded procedure %s has no exact file:line source reason", procedure)
		}
		if _, registered := familyAdminSpecs[d.Tool]; registered {
			t.Fatalf("excluded procedure %s has a registered spec", procedure)
		}
	}
	// Surfaces outside the six families must never appear as exposed tools.
	for _, d := range FamilyAdminDescriptors {
		if d.Class == ClassExclude {
			continue
		}
		for _, fragment := range []string{"NodeControlService", "SendCommand", "ScanNodes", "WebSocket", "Enrollment"} {
			if strings.Contains(d.Procedure, fragment) {
				t.Errorf("explicit EXCLUDE surface leaked into tools: %s", d.Procedure)
			}
		}
	}
}

func findFamilyAdminDescriptor(procedure string) (FamilyAdminDescriptor, bool) {
	for _, d := range FamilyAdminDescriptors {
		if d.Procedure == procedure {
			return d, true
		}
	}
	return FamilyAdminDescriptor{}, false
}

func TestFamilyAdminDescriptorsAreJSONReadable(t *testing.T) {
	raw, err := MarshalFamilyAdminDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	var parsed []FamilyAdminDescriptor
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != FamilyAdminDescriptorCount {
		t.Fatalf("json descriptors %d, want %d", len(parsed), FamilyAdminDescriptorCount)
	}
	if !strings.Contains(string(raw), `"scope"`) || !strings.Contains(string(raw), `"class"`) {
		t.Fatal("descriptor JSON lacks scope or class fields")
	}
	if !strings.Contains(string(raw), `"source_reason"`) || !strings.Contains(string(raw), `"equivalent"`) {
		t.Fatal("descriptor JSON lacks source-cited exclusion evidence")
	}
}

// TestFamilyAdminExportDescriptors is the integration-owner handoff seam: set
// FAMILY_ADMIN_DESCRIPTOR_OUT to regenerate the machine-readable descriptor
// JSON. It is a no-op in ordinary test runs.
func TestFamilyAdminExportDescriptors(t *testing.T) {
	path := os.Getenv("FAMILY_ADMIN_DESCRIPTOR_OUT")
	if path == "" {
		t.Skip("set FAMILY_ADMIN_DESCRIPTOR_OUT to export descriptors")
	}
	raw, err := MarshalFamilyAdminDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d bytes of descriptor JSON to %s", len(raw), path)
}

// TestFamilyAdminCoverageCounts reports LISTED / REACHABLE / VERIFIED
// separately. A green tool test is not a claim of full API coverage.
func TestFamilyAdminCoverageCounts(t *testing.T) {
	listed := len(FamilyAdminDescriptors)
	reachable := len(familyAdminSpecs)
	verified := len(adminVerifyCases())
	excluded := 0
	for _, d := range FamilyAdminDescriptors {
		if d.Class == ClassExclude {
			excluded++
		}
	}
	if listed != FamilyAdminDescriptorCount || reachable != FamilyAdminToolCount ||
		verified != FamilyAdminVerifiedCount || excluded != FamilyAdminExcludedCount {
		t.Fatalf("LISTED=%d/%d REACHABLE=%d/%d VERIFIED=%d/%d EXCLUDED=%d/%d",
			listed, FamilyAdminDescriptorCount, reachable, FamilyAdminToolCount,
			verified, FamilyAdminVerifiedCount, excluded, FamilyAdminExcludedCount)
	}
	if listed != reachable+excluded {
		t.Fatalf("listed %d != reachable %d + excluded %d", listed, reachable, excluded)
	}
	t.Logf("OSS-373 coverage: LISTED=%d REACHABLE=%d VERIFIED=%d EXCLUDED=%d",
		listed, reachable, verified, excluded)
}
