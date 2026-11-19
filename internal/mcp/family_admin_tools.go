// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// This file is the OSS-373 scoped-administration slice. It is deliberately a
// separate module: catalog.go, catalog_data.go, tools.go and server.go belong
// to the integration owner. RegisterFamilyAdminTools is called by the owner
// from NewServer, and FamilyAdminDescriptors is the machine-readable handoff
// used to promote the declared tuples into the generated catalog.
//
// Every included tool uses a typed generated Connect client, the exact live
// RBAC scope, bounded arguments in Go as well as in the schema, and a
// confirmation gate on every mutation and every authorization change. No
// secret-bearing procedure is exposed: login/register/OIDC/recovery, invite
// codes, plaintext API tokens, provider keys, Velocity secrets and node TLS
// material stay excluded.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"connectrpc.com/connect"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Coverage counts for this slice, reported separately so neither a listed
// descriptor nor a registered tool is mistaken for a verified workflow:
//   - LISTED:   every RPC in the six families (FamilyAdminDescriptorCount).
//   - REACHABLE: tools registered with the MCP server (FamilyAdminToolCount).
//   - VERIFIED:  reachable tools with scoped fixture success + denial evidence
//     (asserted by TestFamilyAdminEveryToolIsVerified).
const (
	FamilyAdminDescriptorCount = 56
	FamilyAdminToolCount       = 46
	FamilyAdminExcludedCount   = 10
	FamilyAdminVerifiedCount   = 46
)

// FamilyAdminDescriptor is the machine-readable declaration for one RPC in a
// scoped-administration family. Excluded entries keep Tool empty and Class
// ClassExclude so the integration owner can assert they were not promoted.
type FamilyAdminDescriptor struct {
	Family      string `json:"family"`
	Tool        string `json:"tool,omitempty"`
	Procedure   string `json:"procedure"`
	Service     string `json:"service"`
	Method      string `json:"method"`
	Client      string `json:"client"`
	Scope       string `json:"scope"`
	ReadOnly    bool   `json:"read_only"`
	Confirm     bool   `json:"confirm"`
	Class       Class  `json:"class"`
	BoundedArgs string `json:"bounded_args,omitempty"`
	Description string `json:"description"`
	// Reason is a short human-readable summary of an EXCLUDE decision.
	Reason string `json:"reason,omitempty"`
	// SourceReason is the exact file:line evidence for an EXCLUDE decision.
	SourceReason string `json:"source_reason,omitempty"`
	// Equivalent names the supported flow that replaces an excluded RPC, when
	// one exists in this catalog or in the documented client configuration.
	Equivalent string `json:"equivalent,omitempty"`
}

// FamilyAdminDescriptors lists every RPC of AuthService (18), UserService (5),
// RoleService (10), SubuserService (3), ProxyService (12) and ConfigService (8)
// = 56 LISTED entries. 46 are REACHABLE tools; 10 are EXCLUDE with exact source
// line evidence and, where available, a supported equivalent flow. Scope is
// copied from the live rbac mapping and asserted by
// TestFamilyAdminDescriptorsMatchLiveRBAC.
var FamilyAdminDescriptors = []FamilyAdminDescriptor{
	// --- AuthService: self-service identity plus token lifecycle ----------
	{Family: "auth", Tool: "get_auth_status", Procedure: "/carbonpanel.v1.AuthService/GetAuthStatus", Service: "carbonpanel.v1.AuthService", Method: "GetAuthStatus", Client: "NewAuthServiceClient", Scope: "public", ReadOnly: true, Class: ClassSafe, BoundedArgs: "no arguments", Description: "Read the controller's public auth-provider state (booleans only). The MCP client is still token-authenticated."},
	{Family: "auth", Procedure: "/carbonpanel.v1.AuthService/Login", Service: "carbonpanel.v1.AuthService", Method: "Login", Client: "NewAuthServiceClient", Scope: "public", Class: ClassExclude, Reason: "session-token bootstrap", SourceReason: "internal/rpc/services/auth.go:93-155 mints a session token at :150-154; proto/carbonpanel/v1/auth.proto:60-61; public at internal/rbac/mapping.go:20", Equivalent: "client.go:32-47 TokenFromEnv + :81-98 NewClients use a dp_ API token", Description: "Excluded: private session-token bootstrap."},
	{Family: "auth", Procedure: "/carbonpanel.v1.AuthService/Logout", Service: "carbonpanel.v1.AuthService", Method: "Logout", Client: "NewAuthServiceClient", Scope: "authenticated", Class: ClassExclude, Reason: "browser session invalidation", SourceReason: "internal/rpc/services/auth.go:198-215 reads the bearer session token from the Authorization header and invalidates that session; an API-token MCP client has none", Equivalent: "delete_api_token revokes the credential actually presented by this client", Description: "Excluded: private browser-session mechanic."},
	{Family: "auth", Procedure: "/carbonpanel.v1.AuthService/Register", Service: "carbonpanel.v1.AuthService", Method: "Register", Client: "NewAuthServiceClient", Scope: "public", Class: ClassExclude, Reason: "public account bootstrap", SourceReason: "internal/rpc/services/auth.go:217-... creates a first local account with an optional invite; proto/carbonpanel/v1/auth.proto:64-65; public at internal/rbac/mapping.go:20", Equivalent: "create_user (users/create) is the authenticated administration path", Description: "Excluded: public bootstrap flow."},
	{Family: "auth", Tool: "get_current_user", Procedure: "/carbonpanel.v1.AuthService/GetCurrentUser", Service: "carbonpanel.v1.AuthService", Method: "GetCurrentUser", Client: "NewAuthServiceClient", Scope: "authenticated", ReadOnly: true, Class: ClassSafe, BoundedArgs: "no arguments", Description: "Read the authenticated identity and its effective permissions. Self-service, no secrets."},
	{Family: "auth", Tool: "change_password", Procedure: "/carbonpanel.v1.AuthService/ChangePassword", Service: "carbonpanel.v1.AuthService", Method: "ChangePassword", Client: "NewAuthServiceClient", Scope: "authenticated", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "old_password 1-256, new_password 1-256; confirm=true; no password in the response", Description: "Rotate the authenticated user's own password. Confirm-gated self-service; only a short status message is returned."},
	{Family: "auth", Procedure: "/carbonpanel.v1.AuthService/GetOIDCLoginURL", Service: "carbonpanel.v1.AuthService", Method: "GetOIDCLoginURL", Client: "NewAuthServiceClient", Scope: "public", Class: ClassExclude, Reason: "browser IdP handshake", SourceReason: "internal/rpc/services/auth.go:401-409 returns the browser path /api/v1/auth/oidc/login; internal/rpc/server.go:287-294 mounts the OIDC login/callback/exchange routes", Equivalent: "API-token auth via TokenFromEnv (client.go:32-47)", Description: "Excluded: private browser handshake."},
	{Family: "auth", Tool: "get_auth_config", Procedure: "/carbonpanel.v1.AuthService/GetAuthConfig", Service: "carbonpanel.v1.AuthService", Method: "GetAuthConfig", Client: "NewAuthServiceClient", Scope: "settings/read", ReadOnly: true, Class: ClassSafe, BoundedArgs: "no arguments; responses redacted", Description: "Read non-secret auth configuration display fields (issuer, client id, scopes). settings/read only."},
	{Family: "auth", Tool: "update_auth_settings", Procedure: "/carbonpanel.v1.AuthService/UpdateAuthSettings", Service: "carbonpanel.v1.AuthService", Method: "UpdateAuthSettings", Client: "NewAuthServiceClient", Scope: "settings/update", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "local_auth_enabled/allow_registration/anonymous_access booleans, session_timeout 1-86400; confirm=true", Description: "Update the controller's mutable auth settings. Confirm-gated settings/update admin; ErrNoAuthProvider guard prevents disabling every provider."},
	{Family: "auth", Tool: "create_invite", Procedure: "/carbonpanel.v1.AuthService/CreateInvite", Service: "carbonpanel.v1.AuthService", Method: "CreateInvite", Client: "NewAuthServiceClient", Scope: "users/create", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "description<=256, roles<=16, pin<=256, max_uses 0-10000, expires_in_hours 1-8760; confirm=true; invite code redacted", Description: "Create one registration invite. Confirm-gated users/create admin; the generated code/PIN is redacted from tool output."},
	{Family: "auth", Tool: "list_invites", Procedure: "/carbonpanel.v1.AuthService/ListInvites", Service: "carbonpanel.v1.AuthService", Method: "ListInvites", Client: "NewAuthServiceClient", Scope: "users/read", ReadOnly: true, Class: ClassSafe, BoundedArgs: "no arguments; invite codes redacted", Description: "List registration invite metadata and usage. Read-only; secret codes are redacted."},
	{Family: "auth", Tool: "get_invite", Procedure: "/carbonpanel.v1.AuthService/GetInvite", Service: "carbonpanel.v1.AuthService", Method: "GetInvite", Client: "NewAuthServiceClient", Scope: "users/read", ReadOnly: true, Class: ClassSafe, BoundedArgs: "id 1-128 chars; invite code redacted", Description: "Get one registration invite by id. Read-only; secret code is redacted."},
	{Family: "auth", Tool: "delete_invite", Procedure: "/carbonpanel.v1.AuthService/DeleteInvite", Service: "carbonpanel.v1.AuthService", Method: "DeleteInvite", Client: "NewAuthServiceClient", Scope: "users/delete", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "id 1-128 chars; confirm=true", Description: "Revoke one registration invite by exact id. Confirm-gated; response is empty and contains no invite code."},
	{Family: "auth", Tool: "validate_invite", Procedure: "/carbonpanel.v1.AuthService/ValidateInvite", Service: "carbonpanel.v1.AuthService", Method: "ValidateInvite", Client: "NewAuthServiceClient", Scope: "public", ReadOnly: true, Class: ClassSafe, BoundedArgs: "code 1-256 chars; response never echoes the code", Description: "Check whether an invite code is still valid. Public read; only validity/PIN-required/description is returned."},
	{Family: "auth", Tool: "create_api_token", Procedure: "/carbonpanel.v1.AuthService/CreateAPIToken", Service: "carbonpanel.v1.AuthService", Method: "CreateAPIToken", Client: "NewAuthServiceClient", Scope: "authenticated", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "name 1-128, expires_in_days 0-3650; confirm=true; plaintext_token redacted", Description: "Create one API token for the authenticated user. Confirm-gated token lifecycle; the one-time plaintext value is redacted from tool output."},
	{Family: "auth", Tool: "list_api_tokens", Procedure: "/carbonpanel.v1.AuthService/ListAPITokens", Service: "carbonpanel.v1.AuthService", Method: "ListAPITokens", Client: "NewAuthServiceClient", Scope: "authenticated", ReadOnly: true, Class: ClassSafe, BoundedArgs: "no arguments; metadata only", Description: "List API token metadata (id, name, timestamps). Never returns token values."},
	{Family: "auth", Tool: "delete_api_token", Procedure: "/carbonpanel.v1.AuthService/DeleteAPIToken", Service: "carbonpanel.v1.AuthService", Method: "DeleteAPIToken", Client: "NewAuthServiceClient", Scope: "authenticated", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "id 1-128 chars; confirm=true", Description: "Revoke one API token by exact id. Confirm-gated lifecycle mutation; no secret in the response."},
	{Family: "auth", Procedure: "/carbonpanel.v1.AuthService/UseRecoveryKey", Service: "carbonpanel.v1.AuthService", Method: "UseRecoveryKey", Client: "NewAuthServiceClient", Scope: "public", Class: ClassExclude, Reason: "panel-wide destructive reset", SourceReason: "internal/rpc/services/auth.go:662-684; the comment at :682-683 states a successful reset destroys every session, token, invite and user and is the highest-consequence unauthenticated operation in the product", Equivalent: "create_user + delete_api_token cover ordinary account/credential recovery", Description: "Excluded: operator-only panel-wide reset."},

	// --- UserService: users/* ---------------------------------------------
	{Family: "user", Tool: "list_users", Procedure: "/carbonpanel.v1.UserService/ListUsers", Service: "carbonpanel.v1.UserService", Method: "ListUsers", Client: "NewUserServiceClient", Scope: "users/read", ReadOnly: true, Class: ClassSafe, BoundedArgs: "no arguments", Description: "List system users visible to this token. Read-only."},
	{Family: "user", Tool: "get_user", Procedure: "/carbonpanel.v1.UserService/GetUser", Service: "carbonpanel.v1.UserService", Method: "GetUser", Client: "NewUserServiceClient", Scope: "users/read", ReadOnly: true, Class: ClassSafe, BoundedArgs: "id 1-128 chars", Description: "Get one system user by exact id. Read-only."},
	{Family: "user", Tool: "create_user", Procedure: "/carbonpanel.v1.UserService/CreateUser", Service: "carbonpanel.v1.UserService", Method: "CreateUser", Client: "NewUserServiceClient", Scope: "users/create", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "username 1-128, email<=256, password 1-256 (one-time), roles<=16 of <=64 chars; confirm=true", Description: "Create one user. Confirm-gated; supply a one-time password out of band."},
	{Family: "user", Tool: "update_user", Procedure: "/carbonpanel.v1.UserService/UpdateUser", Service: "carbonpanel.v1.UserService", Method: "UpdateUser", Client: "NewUserServiceClient", Scope: "users/update", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "id 1-128, email<=256, optional is_active with set_is_active, roles<=16; confirm=true", Description: "Update one user's email, active flag or roles. Confirm-gated authorization mutation."},
	{Family: "user", Tool: "delete_user", Procedure: "/carbonpanel.v1.UserService/DeleteUser", Service: "carbonpanel.v1.UserService", Method: "DeleteUser", Client: "NewUserServiceClient", Scope: "users/delete", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "id 1-128 chars; confirm=true", Description: "Delete one user by exact id. Destructive and confirm-gated."},

	// --- RoleService: roles/* ---------------------------------------------
	{Family: "role", Tool: "list_roles", Procedure: "/carbonpanel.v1.RoleService/ListRoles", Service: "carbonpanel.v1.RoleService", Method: "ListRoles", Client: "NewRoleServiceClient", Scope: "roles/read", ReadOnly: true, Class: ClassSafe, BoundedArgs: "no arguments", Description: "List roles visible to this token. Read-only."},
	{Family: "role", Tool: "get_role", Procedure: "/carbonpanel.v1.RoleService/GetRole", Service: "carbonpanel.v1.RoleService", Method: "GetRole", Client: "NewRoleServiceClient", Scope: "roles/read", ReadOnly: true, Class: ClassSafe, BoundedArgs: "id 1-128 chars", Description: "Get one role by exact id. Read-only."},
	{Family: "role", Tool: "create_role", Procedure: "/carbonpanel.v1.RoleService/CreateRole", Service: "carbonpanel.v1.RoleService", Method: "CreateRole", Client: "NewRoleServiceClient", Scope: "roles/create", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "name 1-128, description<=256, permissions<=64; confirm=true", Description: "Create one custom role with bounded permission entries. Confirm-gated authorization mutation."},
	{Family: "role", Tool: "update_role", Procedure: "/carbonpanel.v1.RoleService/UpdateRole", Service: "carbonpanel.v1.RoleService", Method: "UpdateRole", Client: "NewRoleServiceClient", Scope: "roles/update", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "id 1-128, name<=128, description<=256, optional is_default with set_is_default; confirm=true", Description: "Update one role's metadata. Confirm-gated authorization mutation."},
	{Family: "role", Tool: "delete_role", Procedure: "/carbonpanel.v1.RoleService/DeleteRole", Service: "carbonpanel.v1.RoleService", Method: "DeleteRole", Client: "NewRoleServiceClient", Scope: "roles/delete", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "id 1-128 chars; confirm=true", Description: "Delete one custom role. Destructive and confirm-gated."},
	{Family: "role", Tool: "get_permission_matrix", Procedure: "/carbonpanel.v1.RoleService/GetPermissionMatrix", Service: "carbonpanel.v1.RoleService", Method: "GetPermissionMatrix", Client: "NewRoleServiceClient", Scope: "roles/read", ReadOnly: true, Class: ClassSafe, BoundedArgs: "include_objects boolean", Description: "Read the role/permission matrix. Read-only."},
	{Family: "role", Tool: "update_role_permissions", Procedure: "/carbonpanel.v1.RoleService/UpdatePermissions", Service: "carbonpanel.v1.RoleService", Method: "UpdatePermissions", Client: "NewRoleServiceClient", Scope: "roles/update", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "role_name 1-128, permissions<=64; confirm=true", Description: "Replace one role's permissions. Confirm-gated authorization mutation."},
	{Family: "role", Tool: "assign_role", Procedure: "/carbonpanel.v1.RoleService/AssignRole", Service: "carbonpanel.v1.RoleService", Method: "AssignRole", Client: "NewRoleServiceClient", Scope: "roles/create", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "user_id 1-128, role_name 1-128; confirm=true", Description: "Assign one role to one user. Confirm-gated authorization mutation."},
	{Family: "role", Tool: "unassign_role", Procedure: "/carbonpanel.v1.RoleService/UnassignRole", Service: "carbonpanel.v1.RoleService", Method: "UnassignRole", Client: "NewRoleServiceClient", Scope: "roles/delete", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "user_id 1-128, role_name 1-128; confirm=true", Description: "Unassign one role from one user. Confirm-gated authorization mutation."},
	{Family: "role", Tool: "get_user_roles", Procedure: "/carbonpanel.v1.RoleService/GetUserRoles", Service: "carbonpanel.v1.RoleService", Method: "GetUserRoles", Client: "NewRoleServiceClient", Scope: "roles/read", ReadOnly: true, Class: ClassSafe, BoundedArgs: "user_id 1-128 chars", Description: "List roles assigned to one user. Read-only."},

	// --- SubuserService: subusers/*[server_id] ----------------------------
	{Family: "subuser", Tool: "list_subusers", Procedure: "/carbonpanel.v1.SubuserService/ListSubusers", Service: "carbonpanel.v1.SubuserService", Method: "ListSubusers", Client: "NewSubuserServiceClient", Scope: "subusers/read[server_id]", ReadOnly: true, Class: ClassSafe, BoundedArgs: "server_id 1-128 chars, never empty", Description: "List per-server subuser grants. Read-only; the server_id is the RBAC object."},
	{Family: "subuser", Tool: "set_subuser", Procedure: "/carbonpanel.v1.SubuserService/SetSubuser", Service: "carbonpanel.v1.SubuserService", Method: "SetSubuser", Client: "NewSubuserServiceClient", Scope: "subusers/create[server_id]", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "server_id 1-128, user_id 1-128, permissions<=32 of <=128 chars; confirm=true", Description: "Grant or replace one server-scoped subuser grant. Confirm-gated authorization mutation."},
	{Family: "subuser", Tool: "remove_subuser", Procedure: "/carbonpanel.v1.SubuserService/RemoveSubuser", Service: "carbonpanel.v1.SubuserService", Method: "RemoveSubuser", Client: "NewSubuserServiceClient", Scope: "subusers/delete[server_id]", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "server_id 1-128, user_id 1-128; confirm=true", Description: "Revoke one server-scoped subuser grant. Confirm-gated authorization mutation."},

	// --- ProxyService: read/config with secret redaction -------------------
	{Family: "proxy", Tool: "get_proxy_routes", Procedure: "/carbonpanel.v1.ProxyService/GetProxyRoutes", Service: "carbonpanel.v1.ProxyService", Method: "GetProxyRoutes", Client: "NewProxyServiceClient", Scope: "proxy/read", ReadOnly: true, Class: ClassSafe, BoundedArgs: "no arguments; responses redacted", Description: "List active proxy routes. Read-only."},
	{Family: "proxy", Tool: "get_proxy_status", Procedure: "/carbonpanel.v1.ProxyService/GetProxyStatus", Service: "carbonpanel.v1.ProxyService", Method: "GetProxyStatus", Client: "NewProxyServiceClient", Scope: "proxy/read", ReadOnly: true, Class: ClassSafe, BoundedArgs: "no arguments; responses redacted", Description: "Read proxy system status. Read-only."},
	{Family: "proxy", Tool: "get_proxy_listeners", Procedure: "/carbonpanel.v1.ProxyService/GetProxyListeners", Service: "carbonpanel.v1.ProxyService", Method: "GetProxyListeners", Client: "NewProxyServiceClient", Scope: "proxy/read", ReadOnly: true, Class: ClassSafe, BoundedArgs: "no arguments; responses redacted", Description: "List configured proxy listeners with usage counts. Read-only."},
	{Family: "proxy", Tool: "get_server_routing", Procedure: "/carbonpanel.v1.ProxyService/GetServerRouting", Service: "carbonpanel.v1.ProxyService", Method: "GetServerRouting", Client: "NewProxyServiceClient", Scope: "proxy/read[server_id]", ReadOnly: true, Class: ClassSafe, BoundedArgs: "server_id 1-128 chars; responses redacted", Description: "Read one server's proxy routing. Read-only."},
	{Family: "proxy", Tool: "update_proxy_config", Procedure: "/carbonpanel.v1.ProxyService/UpdateProxyConfig", Service: "carbonpanel.v1.ProxyService", Method: "UpdateProxyConfig", Client: "NewProxyServiceClient", Scope: "proxy/update", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "enabled boolean, base_url<=512 (http/https); confirm=true", Description: "Update proxy enabled state and base URL. Confirm-gated config mutation."},
	{Family: "proxy", Tool: "create_proxy_listener", Procedure: "/carbonpanel.v1.ProxyService/CreateProxyListener", Service: "carbonpanel.v1.ProxyService", Method: "CreateProxyListener", Client: "NewProxyServiceClient", Scope: "proxy/create", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "name 1-128, description<=256, port 1-65535; confirm=true", Description: "Create one proxy listener. Confirm-gated config mutation."},
	{Family: "proxy", Tool: "update_proxy_listener", Procedure: "/carbonpanel.v1.ProxyService/UpdateProxyListener", Service: "carbonpanel.v1.ProxyService", Method: "UpdateProxyListener", Client: "NewProxyServiceClient", Scope: "proxy/update[id]", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "id 1-128, name<=128, description<=256, port 1-65535; confirm=true", Description: "Update one proxy listener. Confirm-gated config mutation."},
	{Family: "proxy", Tool: "delete_proxy_listener", Procedure: "/carbonpanel.v1.ProxyService/DeleteProxyListener", Service: "carbonpanel.v1.ProxyService", Method: "DeleteProxyListener", Client: "NewProxyServiceClient", Scope: "proxy/delete[id]", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "id 1-128 chars; confirm=true", Description: "Delete one proxy listener. Destructive and confirm-gated."},
	{Family: "proxy", Tool: "update_server_routing", Procedure: "/carbonpanel.v1.ProxyService/UpdateServerRouting", Service: "carbonpanel.v1.ProxyService", Method: "UpdateServerRouting", Client: "NewProxyServiceClient", Scope: "proxy/update[server_id]", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "server_id 1-128, hostname<=256, listener_id<=128; confirm=true", Description: "Update one server's proxy hostname and listener. Confirm-gated config mutation."},
	{Family: "proxy", Procedure: "/carbonpanel.v1.ProxyService/GetVelocitySecret", Service: "carbonpanel.v1.ProxyService", Method: "GetVelocitySecret", Client: "NewProxyServiceClient", Scope: "settings/read", Class: ClassExclude, Reason: "Velocity forwarding secret dump", SourceReason: "internal/rpc/services/proxy.go:609-629 lazily generates and returns the raw secret in GetVelocitySecretResponse.Secret at :625-627; proto/carbonpanel/v1/proxy.proto:185-193", Equivalent: "get_proxy_status and get_proxy_routes expose non-secret proxy state", Description: "Excluded: secret dump."},
	{Family: "proxy", Procedure: "/carbonpanel.v1.ProxyService/RotateVelocitySecret", Service: "carbonpanel.v1.ProxyService", Method: "RotateVelocitySecret", Client: "NewProxyServiceClient", Scope: "proxy/update", Class: ClassExclude, Reason: "bulk secret rotation and propagation", SourceReason: "internal/rpc/services/proxy.go:631-673 persists a new secret at :642-644, writes it into every reachable server at :647-665 and returns new_secret at :668", Equivalent: "get_proxy_status / get_proxy_routes (read-only proxy state)", Description: "Excluded: operator-only bulk secret rotation."},
	{Family: "proxy", Procedure: "/carbonpanel.v1.ProxyService/SyncVelocitySecretToServer", Service: "carbonpanel.v1.ProxyService", Method: "SyncVelocitySecretToServer", Client: "NewProxyServiceClient", Scope: "proxy/update[server_id]", Class: ClassExclude, Reason: "writes secret bytes into server files", SourceReason: "internal/rpc/services/proxy.go:675-708 reads or generates the secret at :677-685 and writes it into the server data path with SyncVelocitySecretToDataPath at :697", Equivalent: "get_server_routing (non-secret routing state)", Description: "Excluded: secret propagation."},

	// --- ConfigService: global settings only where the ACL permits ---------
	{Family: "config", Tool: "get_global_settings", Procedure: "/carbonpanel.v1.ConfigService/GetGlobalSettings", Service: "carbonpanel.v1.ConfigService", Method: "GetGlobalSettings", Client: "NewConfigServiceClient", Scope: "settings/read", ReadOnly: true, Class: ClassSafe, BoundedArgs: "no arguments; password/key values redacted", Description: "Read system-wide defaults with password and credential values redacted. settings/read only."},
	{Family: "config", Tool: "update_global_settings", Procedure: "/carbonpanel.v1.ConfigService/UpdateGlobalSettings", Service: "carbonpanel.v1.ConfigService", Method: "UpdateGlobalSettings", Client: "NewConfigServiceClient", Scope: "settings/update", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "updates<=64 keys of <=128 chars and <=512 chars each; confirm=true", Description: "Update bounded system-wide default values. Confirm-gated; settings/update ACL only."},
	{Family: "config", Tool: "get_server_config", Procedure: "/carbonpanel.v1.ConfigService/GetServerConfig", Service: "carbonpanel.v1.ConfigService", Method: "GetServerConfig", Client: "NewConfigServiceClient", Scope: "server_config/read[server_id]", ReadOnly: true, Class: ClassSafe, BoundedArgs: "server_id 1-128 chars; password/key values redacted", Description: "Read one server's categorized environment settings. Read-only; credential values are redacted."},
	{Family: "config", Tool: "update_server_config", Procedure: "/carbonpanel.v1.ConfigService/UpdateServerConfig", Service: "carbonpanel.v1.ConfigService", Method: "UpdateServerConfig", Client: "NewConfigServiceClient", Scope: "server_config/update[server_id]", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "server_id 1-128, updates<=64 keys/values, stage recommended, window from enum, apply_at_unix/idle_minutes bounded; confirm=true", Description: "Update one server's environment settings. Confirm-gated; stage=true records a diff without recreating the container."},
	{Family: "config", Tool: "get_update_batch", Procedure: "/carbonpanel.v1.ConfigService/GetUpdateBatch", Service: "carbonpanel.v1.ConfigService", Method: "GetUpdateBatch", Client: "NewConfigServiceClient", Scope: "server_config/read[server_id]", ReadOnly: true, Class: ClassSafe, BoundedArgs: "server_id 1-128 chars", Description: "Read one server's open staged config batch. Read-only."},
	{Family: "config", Tool: "discard_update_batch", Procedure: "/carbonpanel.v1.ConfigService/DiscardUpdateBatch", Service: "carbonpanel.v1.ConfigService", Method: "DiscardUpdateBatch", Client: "NewConfigServiceClient", Scope: "server_config/update[server_id]", ReadOnly: false, Confirm: true, Class: ClassReview, BoundedArgs: "server_id 1-128, item_id<=128 (empty discards whole batch); confirm=true", Description: "Discard one staged item or the whole open batch. Confirm-gated."},
	{Family: "config", Procedure: "/carbonpanel.v1.ConfigService/SyncGlobalSettingsToServers", Service: "carbonpanel.v1.ConfigService", Method: "SyncGlobalSettingsToServers", Client: "NewConfigServiceClient", Scope: "settings/update", Class: ClassExclude, Reason: "bulk operator maintenance across all servers", SourceReason: "internal/rpc/services/config.go:214-260 lists every server at :222 and rewrites each server config at :231-259; a bounded per-server alternative exists", Equivalent: "update_global_settings followed by confirm-gated update_server_config on an explicit server_id", Description: "Excluded: intentionally operator-only bulk propagation."},
	{Family: "config", Procedure: "/carbonpanel.v1.ConfigService/ValidateProviderKey", Service: "carbonpanel.v1.ConfigService", Method: "ValidateProviderKey", Client: "NewConfigServiceClient", Scope: "settings/read", Class: ClassExclude, Reason: "provider credential and third-party egress", SourceReason: "internal/rpc/services/config.go:308-318 forwards GetApiKey() to indexers.ValidateProviderKey at :313; proto/carbonpanel/v1/config.proto:161-175 carries api_key", Equivalent: "none; validate provider credentials out of band", Description: "Excluded: provider key."},
}

// MarshalFamilyAdminDescriptors emits the descriptor list as indented JSON for
// the integration owner's catalog generation step.
func MarshalFamilyAdminDescriptors() ([]byte, error) {
	return json.MarshalIndent(FamilyAdminDescriptors, "", "  ")
}

// FamilyAdminClients owns the six additional typed Connect clients used by
// this slice. It is constructed from the same token and base URL as Clients;
// no shared client.go field is modified.
type FamilyAdminClients struct {
	Auth    carbonpanelv1connect.AuthServiceClient
	User    carbonpanelv1connect.UserServiceClient
	Role    carbonpanelv1connect.RoleServiceClient
	Subuser carbonpanelv1connect.SubuserServiceClient
	Proxy   carbonpanelv1connect.ProxyServiceClient
	Config  carbonpanelv1connect.ConfigServiceClient
}

// NewFamilyAdminClients builds typed clients with the same bearer interceptor
// as the first-slice clients. An empty token fails closed.
func NewFamilyAdminClients(httpClient connect.HTTPClient, baseURL, token string) (*FamilyAdminClients, error) {
	if httpClient == nil {
		httpClient = newHTTPClient()
	}
	if token == "" {
		return nil, fmt.Errorf("API token is required")
	}
	opts := []connect.ClientOption{connect.WithInterceptors(bearerInterceptor(token))}
	return &FamilyAdminClients{
		Auth:    carbonpanelv1connect.NewAuthServiceClient(httpClient, baseURL, opts...),
		User:    carbonpanelv1connect.NewUserServiceClient(httpClient, baseURL, opts...),
		Role:    carbonpanelv1connect.NewRoleServiceClient(httpClient, baseURL, opts...),
		Subuser: carbonpanelv1connect.NewSubuserServiceClient(httpClient, baseURL, opts...),
		Proxy:   carbonpanelv1connect.NewProxyServiceClient(httpClient, baseURL, opts...),
		Config:  carbonpanelv1connect.NewConfigServiceClient(httpClient, baseURL, opts...),
	}, nil
}

// --- Secret redaction -----------------------------------------------------

const redactedMarker = "[REDACTED]"

// redactProto normalizes a typed response to protojson field names and scrubs
// secret-bearing keys before it is returned to the MCP client. It also redacts
// a property's value when a sibling type/key marks it as a credential, which
// covers ConfigProperty entries whose "type" is "password".
func redactProto(msg proto.Message) map[string]any {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
	if err != nil {
		return map[string]any{"error": "response could not be safely rendered"}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{"error": "response could not be safely rendered"}
	}
	return scrubSecrets(out).(map[string]any)
}

func isSecretKey(key string) bool {
	lower := strings.ToLower(key)
	// Metadata about a token is not the token value. Keep it visible so
	// list/create results remain useful after redaction.
	if lower == "api_token" || lower == "api_tokens" {
		return false
	}
	switch lower {
	case "secret", "new_secret", "custom_secret", "token", "plaintext_token",
		"password", "old_password", "new_password", "api_key", "apikey",
		"client_secret", "private_key", "recovery_key", "pin", "code":
		return true
	}
	for _, suffix := range []string{"_secret", "_password", "_api_key", "_token", "_credential", "_private_key"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func scrubSecrets(value any) any {
	switch node := value.(type) {
	case map[string]any:
		typeName, _ := node["type"].(string)
		keyName, _ := node["key"].(string)
		sensitiveValue := strings.EqualFold(typeName, "password") || isSecretKey(keyName)
		out := make(map[string]any, len(node))
		for k, v := range node {
			if isSecretKey(k) || (sensitiveValue && k == "value") {
				out[k] = redactedMarker
				continue
			}
			out[k] = scrubSecrets(v)
		}
		return out
	case []any:
		out := make([]any, len(node))
		for i, v := range node {
			out[i] = scrubSecrets(v)
		}
		return out
	default:
		return value
	}
}

// invokeFamilyAdmin calls a typed client and returns a redacted, normalized
// result. Controller errors are returned unchanged for toToolError to map.
func invokeFamilyAdmin[Req, Res any](ctx context.Context,
	fn func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error), req Req) (any, error) {
	res, err := fn(ctx, connect.NewRequest(&req))
	if err != nil {
		return nil, err
	}
	if msg, ok := any(res.Msg).(proto.Message); ok {
		return redactProto(msg), nil
	}
	return res.Msg, nil
}

// --- Bounded argument types ----------------------------------------------

const (
	maxAdminRoles     = 16
	maxAdminPerms     = 64
	maxAdminValues    = 64
	maxAdminNameLen   = 128
	maxAdminTextLen   = 256
	maxAdminSecretLen = 256
)

type adminEmptyArgs struct{}

type adminIDArgs struct {
	ID string `json:"id" jsonschema:"exact resource id, 1 to 128 characters"`
}

type adminConfirmIDArgs struct {
	ID      string `json:"id" jsonschema:"exact resource id, 1 to 128 characters"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true to perform this mutation"`
}

func (a adminConfirmIDArgs) Confirmation() bool { return a.Confirm }

type adminPermissionArg struct {
	Resource string `json:"resource" jsonschema:"existing RBAC resource, 1 to 128 characters"`
	Action   string `json:"action" jsonschema:"existing RBAC action, 1 to 128 characters"`
	ObjectID string `json:"object_id,omitempty" jsonschema:"object id or *, at most 128 characters"`
}

type adminUserCreateArgs struct {
	Username string   `json:"username" jsonschema:"1 to 128 characters"`
	Email    string   `json:"email,omitempty" jsonschema:"at most 256 characters"`
	Password string   `json:"password" jsonschema:"one-time password, 1 to 256 characters"`
	Roles    []string `json:"roles,omitempty" jsonschema:"at most 16 role names of at most 128 characters"`
	Confirm  bool     `json:"confirm,omitempty" jsonschema:"must be true to create a user"`
}

func (a adminUserCreateArgs) Confirmation() bool { return a.Confirm }

type adminUserUpdateArgs struct {
	ID          string   `json:"id" jsonschema:"exact user id, 1 to 128 characters"`
	Email       string   `json:"email,omitempty" jsonschema:"at most 256 characters"`
	IsActive    bool     `json:"is_active,omitempty" jsonschema:"new active flag, applied only with set_is_active"`
	SetIsActive bool     `json:"set_is_active,omitempty" jsonschema:"set true to apply is_active"`
	Roles       []string `json:"roles,omitempty" jsonschema:"at most 16 role names of at most 128 characters"`
	Confirm     bool     `json:"confirm,omitempty" jsonschema:"must be true to update a user"`
}

func (a adminUserUpdateArgs) Confirmation() bool { return a.Confirm }

type adminRoleCreateArgs struct {
	Name        string               `json:"name" jsonschema:"1 to 128 characters"`
	Description string               `json:"description,omitempty" jsonschema:"at most 256 characters"`
	IsDefault   bool                 `json:"is_default,omitempty" jsonschema:"whether the role is a default"`
	Permissions []adminPermissionArg `json:"permissions,omitempty" jsonschema:"at most 64 permission entries"`
	Confirm     bool                 `json:"confirm,omitempty" jsonschema:"must be true to create a role"`
}

func (a adminRoleCreateArgs) Confirmation() bool { return a.Confirm }

type adminRoleUpdateArgs struct {
	ID           string `json:"id" jsonschema:"exact role id, 1 to 128 characters"`
	Name         string `json:"name,omitempty" jsonschema:"at most 128 characters"`
	Description  string `json:"description,omitempty" jsonschema:"at most 256 characters"`
	IsDefault    bool   `json:"is_default,omitempty" jsonschema:"new default flag, applied only with set_is_default"`
	SetIsDefault bool   `json:"set_is_default,omitempty" jsonschema:"set true to apply is_default"`
	Confirm      bool   `json:"confirm,omitempty" jsonschema:"must be true to update a role"`
}

func (a adminRoleUpdateArgs) Confirmation() bool { return a.Confirm }

type adminPermissionMatrixArgs struct {
	IncludeObjects bool `json:"include_objects,omitempty" jsonschema:"include scopeable objects in the matrix"`
}

type adminUpdatePermissionsArgs struct {
	RoleName    string               `json:"role_name" jsonschema:"exact role name, 1 to 128 characters"`
	Permissions []adminPermissionArg `json:"permissions" jsonschema:"at most 64 permission entries"`
	Confirm     bool                 `json:"confirm,omitempty" jsonschema:"must be true to replace role permissions"`
}

func (a adminUpdatePermissionsArgs) Confirmation() bool { return a.Confirm }

type adminUserRoleArgs struct {
	UserID   string `json:"user_id" jsonschema:"exact user id, 1 to 128 characters"`
	RoleName string `json:"role_name" jsonschema:"exact role name, 1 to 128 characters"`
	Confirm  bool   `json:"confirm,omitempty" jsonschema:"must be true to change role assignment"`
}

func (a adminUserRoleArgs) Confirmation() bool { return a.Confirm }

type adminUserIDArgs struct {
	UserID string `json:"user_id" jsonschema:"exact user id, 1 to 128 characters"`
}

type adminSubuserArgs struct {
	ServerID    string   `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	UserID      string   `json:"user_id" jsonschema:"exact user id, 1 to 128 characters"`
	Permissions []string `json:"permissions,omitempty" jsonschema:"at most 32 permissions of at most 128 characters"`
	Confirm     bool     `json:"confirm,omitempty" jsonschema:"must be true to change a subuser grant"`
}

func (a adminSubuserArgs) Confirmation() bool { return a.Confirm }

type adminProxyConfigArgs struct {
	Enabled bool   `json:"enabled" jsonschema:"proxy enabled state"`
	BaseURL string `json:"base_url,omitempty" jsonschema:"http or https base URL, at most 512 characters"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true to update proxy config"`
}

func (a adminProxyConfigArgs) Confirmation() bool { return a.Confirm }

type adminListenerCreateArgs struct {
	Name          string `json:"name" jsonschema:"1 to 128 characters"`
	Description   string `json:"description,omitempty" jsonschema:"at most 256 characters"`
	Port          int32  `json:"port" jsonschema:"TCP port from 1 to 65535"`
	Enabled       bool   `json:"enabled,omitempty" jsonschema:"listener enabled state"`
	IsDefault     bool   `json:"is_default,omitempty" jsonschema:"listener is the default"`
	ProxyProtocol bool   `json:"proxy_protocol,omitempty" jsonschema:"accept PROXY protocol"`
	Confirm       bool   `json:"confirm,omitempty" jsonschema:"must be true to create a listener"`
}

func (a adminListenerCreateArgs) Confirmation() bool { return a.Confirm }

type adminListenerUpdateArgs struct {
	ID            string `json:"id" jsonschema:"exact listener id, 1 to 128 characters"`
	Name          string `json:"name,omitempty" jsonschema:"at most 128 characters"`
	Description   string `json:"description,omitempty" jsonschema:"at most 256 characters"`
	Port          int32  `json:"port,omitempty" jsonschema:"TCP port from 1 to 65535"`
	Enabled       bool   `json:"enabled,omitempty" jsonschema:"listener enabled state"`
	IsDefault     bool   `json:"is_default,omitempty" jsonschema:"listener is the default"`
	ProxyProtocol bool   `json:"proxy_protocol,omitempty" jsonschema:"set with set_proxy_protocol"`
	SetProxyProto bool   `json:"set_proxy_protocol,omitempty" jsonschema:"set true to apply proxy_protocol"`
	Confirm       bool   `json:"confirm,omitempty" jsonschema:"must be true to update a listener"`
}

func (a adminListenerUpdateArgs) Confirmation() bool { return a.Confirm }

type adminRoutingArgs struct {
	ServerID        string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	ProxyHostname   string `json:"proxy_hostname,omitempty" jsonschema:"hostname, at most 256 characters"`
	ProxyListenerID string `json:"proxy_listener_id,omitempty" jsonschema:"listener id, at most 128 characters"`
	Confirm         bool   `json:"confirm,omitempty" jsonschema:"must be true to update server routing"`
}

func (a adminRoutingArgs) Confirmation() bool { return a.Confirm }

type adminGlobalSettingsArgs struct {
	Updates map[string]string `json:"updates" jsonschema:"at most 64 keys of at most 128 characters and values of at most 512 characters"`
	Confirm bool              `json:"confirm,omitempty" jsonschema:"must be true to update global settings"`
}

func (a adminGlobalSettingsArgs) Confirmation() bool { return a.Confirm }

type adminPasswordArgs struct {
	OldPassword string `json:"old_password" jsonschema:"current password, 1 to 256 characters"`
	NewPassword string `json:"new_password" jsonschema:"new password, 1 to 256 characters"`
	Confirm     bool   `json:"confirm,omitempty" jsonschema:"must be true to rotate the password"`
}

func (a adminPasswordArgs) Confirmation() bool { return a.Confirm }

type adminAuthSettingsArgs struct {
	LocalAuthEnabled     bool  `json:"local_auth_enabled,omitempty" jsonschema:"new local-auth state, applied only with set_local_auth_enabled"`
	SetLocalAuth         bool  `json:"set_local_auth_enabled,omitempty" jsonschema:"set true to apply local_auth_enabled"`
	AllowRegistration    bool  `json:"allow_registration,omitempty" jsonschema:"new registration state, applied only with set_allow_registration"`
	SetAllowRegistration bool  `json:"set_allow_registration,omitempty" jsonschema:"set true to apply allow_registration"`
	AnonymousAccess      bool  `json:"anonymous_access,omitempty" jsonschema:"new anonymous state, applied only with set_anonymous_access"`
	SetAnonymousAccess   bool  `json:"set_anonymous_access,omitempty" jsonschema:"set true to apply anonymous_access"`
	SessionTimeout       int32 `json:"session_timeout,omitempty" jsonschema:"session timeout in seconds, 0 leaves it unchanged"`
	Confirm              bool  `json:"confirm,omitempty" jsonschema:"must be true to update auth settings"`
}

func (a adminAuthSettingsArgs) Confirmation() bool { return a.Confirm }

type adminInviteCreateArgs struct {
	Description    string   `json:"description,omitempty" jsonschema:"at most 256 characters"`
	Roles          []string `json:"roles,omitempty" jsonschema:"at most 16 existing role names of at most 128 characters"`
	Pin            string   `json:"pin,omitempty" jsonschema:"optional PIN, at most 256 characters; redacted from output"`
	MaxUses        int32    `json:"max_uses,omitempty" jsonschema:"0 for unlimited, otherwise 1 to 10000"`
	ExpiresInHours int32    `json:"expires_in_hours,omitempty" jsonschema:"0 for no expiry, otherwise 1 to 8760"`
	Confirm        bool     `json:"confirm,omitempty" jsonschema:"must be true to create an invite"`
}

func (a adminInviteCreateArgs) Confirmation() bool { return a.Confirm }

type adminValidateInviteArgs struct {
	Code string `json:"code" jsonschema:"invite code, 1 to 256 characters; never echoed back"`
}

type adminAPITokenCreateArgs struct {
	Name          string `json:"name" jsonschema:"1 to 128 characters"`
	ExpiresInDays int32  `json:"expires_in_days,omitempty" jsonschema:"0 for no expiry, otherwise 1 to 3650"`
	Confirm       bool   `json:"confirm,omitempty" jsonschema:"must be true to create a token"`
}

func (a adminAPITokenCreateArgs) Confirmation() bool { return a.Confirm }

type adminServerConfigUpdateArgs struct {
	ServerID    string            `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	Updates     map[string]string `json:"updates" jsonschema:"at most 64 keys of at most 128 characters and values of at most 512 characters"`
	Stage       bool              `json:"stage,omitempty" jsonschema:"stage a diff instead of recreating the container; recommended"`
	Window      string            `json:"window,omitempty" jsonschema:"ConfigApplyWindow enum name"`
	ApplyAtUnix int64             `json:"apply_at_unix,omitempty" jsonschema:"UTC unix seconds, required for AT_TIME"`
	IdleMinutes int32             `json:"idle_minutes,omitempty" jsonschema:"idle minutes, 1 to 10080, required for IDLE"`
	Confirm     bool              `json:"confirm,omitempty" jsonschema:"must be true to update server config"`
}

func (a adminServerConfigUpdateArgs) Confirmation() bool { return a.Confirm }

type adminDiscardBatchArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	ItemID   string `json:"item_id,omitempty" jsonschema:"staged item id, at most 128 characters; empty discards the whole batch"`
	Confirm  bool   `json:"confirm,omitempty" jsonschema:"must be true to discard a staged batch"`
}

func (a adminDiscardBatchArgs) Confirmation() bool { return a.Confirm }

// --- Validation helpers --------------------------------------------------

func boundedStringList(tool, field string, list []string, maxItems, maxLen int) error {
	if len(list) > maxItems {
		return invalidArgument(tool, field, fmt.Sprintf("at most %d items", maxItems))
	}
	for _, v := range list {
		if v == "" || len(v) > maxLen {
			return invalidArgument(tool, field, fmt.Sprintf("each item must be 1 to %d characters", maxLen))
		}
	}
	return nil
}

func boundedPermissions(tool string, perms []adminPermissionArg) ([]*v1.Permission, error) {
	if len(perms) > maxAdminPerms {
		return nil, invalidArgument(tool, "permissions", fmt.Sprintf("at most %d entries", maxAdminPerms))
	}
	out := make([]*v1.Permission, 0, len(perms))
	for _, p := range perms {
		if err := requireID(tool, "permission.resource", p.Resource); err != nil {
			return nil, err
		}
		if err := requireID(tool, "permission.action", p.Action); err != nil {
			return nil, err
		}
		if err := optionalID(tool, "permission.object_id", p.ObjectID); err != nil {
			return nil, err
		}
		out = append(out, &v1.Permission{Resource: p.Resource, Action: p.Action, ObjectId: p.ObjectID})
	}
	return out, nil
}

func boundedPort(tool string, port int32) error {
	if port < 1 || port > 65535 {
		return invalidArgument(tool, "port", "must be between 1 and 65535")
	}
	return nil
}

func boundedBaseURL(tool, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > maxPathLen {
		return invalidArgument(tool, "base_url", fmt.Sprintf("must be at most %d characters", maxPathLen))
	}
	if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
		return invalidArgument(tool, "base_url", "must be an http or https URL")
	}
	return nil
}

func boundedUpdates(tool string, updates map[string]string) error {
	if len(updates) > maxAdminValues {
		return invalidArgument(tool, "updates", fmt.Sprintf("at most %d keys", maxAdminValues))
	}
	for k, v := range updates {
		if k == "" || len(k) > maxAdminNameLen {
			return invalidArgument(tool, "updates", fmt.Sprintf("keys must be 1 to %d characters", maxAdminNameLen))
		}
		if len(v) > maxAdminTextLen*2 {
			return invalidArgument(tool, "updates", fmt.Sprintf("values must be at most %d characters", maxAdminTextLen*2))
		}
	}
	return nil
}

// --- Registration --------------------------------------------------------

var familyAdminSpecs = func() map[string]ToolSpec {
	m := make(map[string]ToolSpec, FamilyAdminToolCount)
	for _, d := range FamilyAdminDescriptors {
		if d.Class == ClassExclude {
			continue
		}
		m[d.Tool] = ToolSpec{
			Name:        d.Tool,
			Procedure:   d.Procedure,
			Class:       d.Class,
			Confirm:     d.Confirm,
			Description: d.Description,
		}
	}
	return m
}()

func familyAdminSpec(name string) ToolSpec { return familyAdminSpecs[name] }

var familyAdminRegistered sync.Map // map[*sdkmcp.Server]struct{} keeps registration idempotent

// RegisterFamilyAdminTools adds this slice's tools to an MCP server. It is
// idempotent per server so the integration owner can call it from NewServer
// without a duplicate registration if a caller also invokes it explicitly.
func RegisterFamilyAdminTools(s *sdkmcp.Server, c *FamilyAdminClients) {
	if s == nil || c == nil {
		return
	}
	if _, loaded := familyAdminRegistered.LoadOrStore(s, struct{}{}); loaded {
		return
	}
	addFamilyAdminTool(s, c, "get_auth_status", func(ctx context.Context, cl *FamilyAdminClients, _ adminEmptyArgs) (any, error) {
		return invokeFamilyAdmin(ctx, cl.Auth.GetAuthStatus, v1.GetAuthStatusRequest{})
	})
	addFamilyAdminTool(s, c, "get_current_user", func(ctx context.Context, cl *FamilyAdminClients, _ adminEmptyArgs) (any, error) {
		return invokeFamilyAdmin(ctx, cl.Auth.GetCurrentUser, v1.GetCurrentUserRequest{})
	})
	addFamilyAdminTool(s, c, "change_password", func(ctx context.Context, cl *FamilyAdminClients, in adminPasswordArgs) (any, error) {
		if err := requireText("change_password", "old_password", in.OldPassword, maxAdminSecretLen); err != nil {
			return nil, err
		}
		if err := requireText("change_password", "new_password", in.NewPassword, maxAdminSecretLen); err != nil {
			return nil, err
		}
		if in.OldPassword == "" || in.NewPassword == "" {
			return nil, invalidArgument("change_password", "password", "both current and new passwords are required")
		}
		return invokeFamilyAdmin(ctx, cl.Auth.ChangePassword, v1.ChangePasswordRequest{
			OldPassword: in.OldPassword, NewPassword: in.NewPassword,
		})
	})
	addFamilyAdminTool(s, c, "get_auth_config", func(ctx context.Context, cl *FamilyAdminClients, _ adminEmptyArgs) (any, error) {
		return invokeFamilyAdmin(ctx, cl.Auth.GetAuthConfig, v1.GetAuthConfigRequest{})
	})
	addFamilyAdminTool(s, c, "update_auth_settings", func(ctx context.Context, cl *FamilyAdminClients, in adminAuthSettingsArgs) (any, error) {
		if in.SetLocalAuth && in.SessionTimeout > 86400 {
			return nil, invalidArgument("update_auth_settings", "session_timeout", "must be at most 86400 seconds")
		}
		if in.SessionTimeout < 0 {
			return nil, invalidArgument("update_auth_settings", "session_timeout", "must not be negative")
		}
		var local, registration, anonymous *bool
		if in.SetLocalAuth {
			local = &in.LocalAuthEnabled
		}
		if in.SetAllowRegistration {
			registration = &in.AllowRegistration
		}
		if in.SetAnonymousAccess {
			anonymous = &in.AnonymousAccess
		}
		var timeout *int32
		if in.SessionTimeout > 0 {
			timeout = &in.SessionTimeout
		}
		return invokeFamilyAdmin(ctx, cl.Auth.UpdateAuthSettings, v1.UpdateAuthSettingsRequest{
			LocalAuthEnabled: local, AllowRegistration: registration,
			AnonymousAccess: anonymous, SessionTimeout: timeout,
		})
	})
	addFamilyAdminTool(s, c, "create_invite", func(ctx context.Context, cl *FamilyAdminClients, in adminInviteCreateArgs) (any, error) {
		if err := requireText("create_invite", "description", in.Description, maxAdminTextLen); err != nil {
			return nil, err
		}
		if err := boundedStringList("create_invite", "roles", in.Roles, maxAdminRoles, maxAdminNameLen); err != nil {
			return nil, err
		}
		if err := requireText("create_invite", "pin", in.Pin, maxAdminSecretLen); err != nil {
			return nil, err
		}
		if in.MaxUses < 0 || in.MaxUses > 10000 {
			return nil, invalidArgument("create_invite", "max_uses", "must be 0 for unlimited or 1 to 10000")
		}
		if in.ExpiresInHours < 0 || in.ExpiresInHours > 8760 {
			return nil, invalidArgument("create_invite", "expires_in_hours", "must be 0 for no expiry or 1 to 8760")
		}
		var pin *string
		if in.Pin != "" {
			pin = &in.Pin
		}
		var expiry *int32
		if in.ExpiresInHours > 0 {
			expiry = &in.ExpiresInHours
		}
		return invokeFamilyAdmin(ctx, cl.Auth.CreateInvite, v1.CreateInviteRequest{
			Description: in.Description, Roles: in.Roles, Pin: pin,
			MaxUses: in.MaxUses, ExpiresInHours: expiry,
		})
	})
	addFamilyAdminTool(s, c, "list_invites", func(ctx context.Context, cl *FamilyAdminClients, _ adminEmptyArgs) (any, error) {
		return invokeFamilyAdmin(ctx, cl.Auth.ListInvites, v1.ListInvitesRequest{})
	})
	addFamilyAdminTool(s, c, "get_invite", func(ctx context.Context, cl *FamilyAdminClients, in adminIDArgs) (any, error) {
		if err := requireID("get_invite", "id", in.ID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Auth.GetInvite, v1.GetInviteRequest{Id: in.ID})
	})
	addFamilyAdminTool(s, c, "delete_invite", func(ctx context.Context, cl *FamilyAdminClients, in adminConfirmIDArgs) (any, error) {
		if err := requireID("delete_invite", "id", in.ID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Auth.DeleteInvite, v1.DeleteInviteRequest{Id: in.ID})
	})
	addFamilyAdminTool(s, c, "list_api_tokens", func(ctx context.Context, cl *FamilyAdminClients, _ adminEmptyArgs) (any, error) {
		return invokeFamilyAdmin(ctx, cl.Auth.ListAPITokens, v1.ListAPITokensRequest{})
	})
	addFamilyAdminTool(s, c, "delete_api_token", func(ctx context.Context, cl *FamilyAdminClients, in adminConfirmIDArgs) (any, error) {
		if err := requireID("delete_api_token", "id", in.ID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Auth.DeleteAPIToken, v1.DeleteAPITokenRequest{Id: in.ID})
	})
	addFamilyAdminTool(s, c, "validate_invite", func(ctx context.Context, cl *FamilyAdminClients, in adminValidateInviteArgs) (any, error) {
		if err := requireText("validate_invite", "code", in.Code, maxAdminTextLen); err != nil {
			return nil, err
		}
		if in.Code == "" {
			return nil, invalidArgument("validate_invite", "code", "an invite code is required")
		}
		return invokeFamilyAdmin(ctx, cl.Auth.ValidateInvite, v1.ValidateInviteRequest{Code: in.Code})
	})
	addFamilyAdminTool(s, c, "create_api_token", func(ctx context.Context, cl *FamilyAdminClients, in adminAPITokenCreateArgs) (any, error) {
		if err := requireID("create_api_token", "name", in.Name); err != nil {
			return nil, err
		}
		if in.ExpiresInDays < 0 || in.ExpiresInDays > 3650 {
			return nil, invalidArgument("create_api_token", "expires_in_days", "must be 0 for no expiry or 1 to 3650")
		}
		var expiry *int32
		if in.ExpiresInDays > 0 {
			expiry = &in.ExpiresInDays
		}
		return invokeFamilyAdmin(ctx, cl.Auth.CreateAPIToken, v1.CreateAPITokenRequest{
			Name: in.Name, ExpiresInDays: expiry,
		})
	})

	addFamilyAdminTool(s, c, "list_users", func(ctx context.Context, cl *FamilyAdminClients, _ adminEmptyArgs) (any, error) {
		return invokeFamilyAdmin(ctx, cl.User.ListUsers, v1.ListUsersRequest{})
	})
	addFamilyAdminTool(s, c, "get_user", func(ctx context.Context, cl *FamilyAdminClients, in adminIDArgs) (any, error) {
		if err := requireID("get_user", "id", in.ID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.User.GetUser, v1.GetUserRequest{Id: in.ID})
	})
	addFamilyAdminTool(s, c, "create_user", func(ctx context.Context, cl *FamilyAdminClients, in adminUserCreateArgs) (any, error) {
		if err := requireID("create_user", "username", in.Username); err != nil {
			return nil, err
		}
		if err := requireText("create_user", "email", in.Email, maxAdminTextLen); err != nil {
			return nil, err
		}
		if err := requireText("create_user", "password", in.Password, maxAdminSecretLen); err != nil {
			return nil, err
		}
		if in.Password == "" {
			return nil, invalidArgument("create_user", "password", "a one-time password is required")
		}
		if err := boundedStringList("create_user", "roles", in.Roles, maxAdminRoles, maxAdminNameLen); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.User.CreateUser, v1.CreateUserRequest{
			Username: in.Username, Email: in.Email, Password: in.Password, Roles: in.Roles,
		})
	})
	addFamilyAdminTool(s, c, "update_user", func(ctx context.Context, cl *FamilyAdminClients, in adminUserUpdateArgs) (any, error) {
		if err := requireID("update_user", "id", in.ID); err != nil {
			return nil, err
		}
		if err := requireText("update_user", "email", in.Email, maxAdminTextLen); err != nil {
			return nil, err
		}
		if err := boundedStringList("update_user", "roles", in.Roles, maxAdminRoles, maxAdminNameLen); err != nil {
			return nil, err
		}
		var email *string
		if in.Email != "" {
			email = &in.Email
		}
		var active *bool
		if in.SetIsActive {
			active = &in.IsActive
		}
		return invokeFamilyAdmin(ctx, cl.User.UpdateUser, v1.UpdateUserRequest{
			Id: in.ID, Email: email, IsActive: active, Roles: in.Roles,
		})
	})
	addFamilyAdminTool(s, c, "delete_user", func(ctx context.Context, cl *FamilyAdminClients, in adminConfirmIDArgs) (any, error) {
		if err := requireID("delete_user", "id", in.ID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.User.DeleteUser, v1.DeleteUserRequest{Id: in.ID})
	})

	addFamilyAdminTool(s, c, "list_roles", func(ctx context.Context, cl *FamilyAdminClients, _ adminEmptyArgs) (any, error) {
		return invokeFamilyAdmin(ctx, cl.Role.ListRoles, v1.ListRolesRequest{})
	})
	addFamilyAdminTool(s, c, "get_role", func(ctx context.Context, cl *FamilyAdminClients, in adminIDArgs) (any, error) {
		if err := requireID("get_role", "id", in.ID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Role.GetRole, v1.GetRoleRequest{Id: in.ID})
	})
	addFamilyAdminTool(s, c, "create_role", func(ctx context.Context, cl *FamilyAdminClients, in adminRoleCreateArgs) (any, error) {
		if err := requireID("create_role", "name", in.Name); err != nil {
			return nil, err
		}
		if err := requireText("create_role", "description", in.Description, maxAdminTextLen); err != nil {
			return nil, err
		}
		perms, err := boundedPermissions("create_role", in.Permissions)
		if err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Role.CreateRole, v1.CreateRoleRequest{
			Name: in.Name, Description: in.Description, IsDefault: in.IsDefault, Permissions: perms,
		})
	})
	addFamilyAdminTool(s, c, "update_role", func(ctx context.Context, cl *FamilyAdminClients, in adminRoleUpdateArgs) (any, error) {
		if err := requireID("update_role", "id", in.ID); err != nil {
			return nil, err
		}
		if err := optionalID("update_role", "name", in.Name); err != nil {
			return nil, err
		}
		if err := requireText("update_role", "description", in.Description, maxAdminTextLen); err != nil {
			return nil, err
		}
		var name *string
		if in.Name != "" {
			name = &in.Name
		}
		var description *string
		if in.Description != "" {
			description = &in.Description
		}
		var isDefault *bool
		if in.SetIsDefault {
			isDefault = &in.IsDefault
		}
		return invokeFamilyAdmin(ctx, cl.Role.UpdateRole, v1.UpdateRoleRequest{
			Id: in.ID, Name: name, Description: description, IsDefault: isDefault,
		})
	})
	addFamilyAdminTool(s, c, "delete_role", func(ctx context.Context, cl *FamilyAdminClients, in adminConfirmIDArgs) (any, error) {
		if err := requireID("delete_role", "id", in.ID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Role.DeleteRole, v1.DeleteRoleRequest{Id: in.ID})
	})
	addFamilyAdminTool(s, c, "get_permission_matrix", func(ctx context.Context, cl *FamilyAdminClients, in adminPermissionMatrixArgs) (any, error) {
		return invokeFamilyAdmin(ctx, cl.Role.GetPermissionMatrix, v1.GetPermissionMatrixRequest{IncludeObjects: in.IncludeObjects})
	})
	addFamilyAdminTool(s, c, "update_role_permissions", func(ctx context.Context, cl *FamilyAdminClients, in adminUpdatePermissionsArgs) (any, error) {
		if err := requireID("update_role_permissions", "role_name", in.RoleName); err != nil {
			return nil, err
		}
		perms, err := boundedPermissions("update_role_permissions", in.Permissions)
		if err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Role.UpdatePermissions, v1.UpdatePermissionsRequest{
			RoleName: in.RoleName, Permissions: perms,
		})
	})
	addFamilyAdminTool(s, c, "assign_role", func(ctx context.Context, cl *FamilyAdminClients, in adminUserRoleArgs) (any, error) {
		if err := requireID("assign_role", "user_id", in.UserID); err != nil {
			return nil, err
		}
		if err := requireID("assign_role", "role_name", in.RoleName); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Role.AssignRole, v1.AssignRoleRequest{UserId: in.UserID, RoleName: in.RoleName})
	})
	addFamilyAdminTool(s, c, "unassign_role", func(ctx context.Context, cl *FamilyAdminClients, in adminUserRoleArgs) (any, error) {
		if err := requireID("unassign_role", "user_id", in.UserID); err != nil {
			return nil, err
		}
		if err := requireID("unassign_role", "role_name", in.RoleName); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Role.UnassignRole, v1.UnassignRoleRequest{UserId: in.UserID, RoleName: in.RoleName})
	})
	addFamilyAdminTool(s, c, "get_user_roles", func(ctx context.Context, cl *FamilyAdminClients, in adminUserIDArgs) (any, error) {
		if err := requireID("get_user_roles", "user_id", in.UserID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Role.GetUserRoles, v1.GetUserRolesRequest{UserId: in.UserID})
	})

	addFamilyAdminTool(s, c, "list_subusers", func(ctx context.Context, cl *FamilyAdminClients, in serverArgs) (any, error) {
		if err := requireID("list_subusers", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Subuser.ListSubusers, v1.ListSubusersRequest{ServerId: in.ServerID})
	})
	addFamilyAdminTool(s, c, "set_subuser", func(ctx context.Context, cl *FamilyAdminClients, in adminSubuserArgs) (any, error) {
		if err := requireID("set_subuser", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("set_subuser", "user_id", in.UserID); err != nil {
			return nil, err
		}
		if err := boundedStringList("set_subuser", "permissions", in.Permissions, 32, maxAdminNameLen); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Subuser.SetSubuser, v1.SetSubuserRequest{
			ServerId: in.ServerID, UserId: in.UserID, Permissions: in.Permissions,
		})
	})
	addFamilyAdminTool(s, c, "remove_subuser", func(ctx context.Context, cl *FamilyAdminClients, in adminSubuserArgs) (any, error) {
		if err := requireID("remove_subuser", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("remove_subuser", "user_id", in.UserID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Subuser.RemoveSubuser, v1.RemoveSubuserRequest{
			ServerId: in.ServerID, UserId: in.UserID,
		})
	})

	addFamilyAdminTool(s, c, "get_proxy_routes", func(ctx context.Context, cl *FamilyAdminClients, _ adminEmptyArgs) (any, error) {
		return invokeFamilyAdmin(ctx, cl.Proxy.GetProxyRoutes, v1.GetProxyRoutesRequest{})
	})
	addFamilyAdminTool(s, c, "get_proxy_status", func(ctx context.Context, cl *FamilyAdminClients, _ adminEmptyArgs) (any, error) {
		return invokeFamilyAdmin(ctx, cl.Proxy.GetProxyStatus, v1.GetProxyStatusRequest{})
	})
	addFamilyAdminTool(s, c, "get_proxy_listeners", func(ctx context.Context, cl *FamilyAdminClients, _ adminEmptyArgs) (any, error) {
		return invokeFamilyAdmin(ctx, cl.Proxy.GetProxyListeners, v1.GetProxyListenersRequest{})
	})
	addFamilyAdminTool(s, c, "get_server_routing", func(ctx context.Context, cl *FamilyAdminClients, in serverArgs) (any, error) {
		if err := requireID("get_server_routing", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Proxy.GetServerRouting, v1.GetServerRoutingRequest{ServerId: in.ServerID})
	})
	addFamilyAdminTool(s, c, "update_proxy_config", func(ctx context.Context, cl *FamilyAdminClients, in adminProxyConfigArgs) (any, error) {
		if err := boundedBaseURL("update_proxy_config", in.BaseURL); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Proxy.UpdateProxyConfig, v1.UpdateProxyConfigRequest{
			Enabled: in.Enabled, BaseUrl: in.BaseURL,
		})
	})
	addFamilyAdminTool(s, c, "create_proxy_listener", func(ctx context.Context, cl *FamilyAdminClients, in adminListenerCreateArgs) (any, error) {
		if err := requireID("create_proxy_listener", "name", in.Name); err != nil {
			return nil, err
		}
		if err := requireText("create_proxy_listener", "description", in.Description, maxAdminTextLen); err != nil {
			return nil, err
		}
		if err := boundedPort("create_proxy_listener", in.Port); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Proxy.CreateProxyListener, v1.CreateProxyListenerRequest{
			Name: in.Name, Description: in.Description, Port: in.Port, Enabled: in.Enabled,
			IsDefault: in.IsDefault, ProxyProtocol: in.ProxyProtocol,
		})
	})
	addFamilyAdminTool(s, c, "update_proxy_listener", func(ctx context.Context, cl *FamilyAdminClients, in adminListenerUpdateArgs) (any, error) {
		if err := requireID("update_proxy_listener", "id", in.ID); err != nil {
			return nil, err
		}
		if err := optionalID("update_proxy_listener", "name", in.Name); err != nil {
			return nil, err
		}
		if err := requireText("update_proxy_listener", "description", in.Description, maxAdminTextLen); err != nil {
			return nil, err
		}
		if in.Port != 0 {
			if err := boundedPort("update_proxy_listener", in.Port); err != nil {
				return nil, err
			}
		}
		var proxyProtocol *bool
		if in.SetProxyProto {
			proxyProtocol = &in.ProxyProtocol
		}
		return invokeFamilyAdmin(ctx, cl.Proxy.UpdateProxyListener, v1.UpdateProxyListenerRequest{
			Id: in.ID, Name: in.Name, Description: in.Description, Port: in.Port,
			Enabled: in.Enabled, IsDefault: in.IsDefault, ProxyProtocol: proxyProtocol,
		})
	})
	addFamilyAdminTool(s, c, "delete_proxy_listener", func(ctx context.Context, cl *FamilyAdminClients, in adminConfirmIDArgs) (any, error) {
		if err := requireID("delete_proxy_listener", "id", in.ID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Proxy.DeleteProxyListener, v1.DeleteProxyListenerRequest{Id: in.ID})
	})
	addFamilyAdminTool(s, c, "update_server_routing", func(ctx context.Context, cl *FamilyAdminClients, in adminRoutingArgs) (any, error) {
		if err := requireID("update_server_routing", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireText("update_server_routing", "proxy_hostname", in.ProxyHostname, maxAdminTextLen); err != nil {
			return nil, err
		}
		if err := optionalID("update_server_routing", "proxy_listener_id", in.ProxyListenerID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Proxy.UpdateServerRouting, v1.UpdateServerRoutingRequest{
			ServerId: in.ServerID, ProxyHostname: in.ProxyHostname, ProxyListenerId: in.ProxyListenerID,
		})
	})

	addFamilyAdminTool(s, c, "get_global_settings", func(ctx context.Context, cl *FamilyAdminClients, _ adminEmptyArgs) (any, error) {
		return invokeFamilyAdmin(ctx, cl.Config.GetGlobalSettings, v1.GetGlobalSettingsRequest{})
	})
	addFamilyAdminTool(s, c, "update_global_settings", func(ctx context.Context, cl *FamilyAdminClients, in adminGlobalSettingsArgs) (any, error) {
		if err := boundedUpdates("update_global_settings", in.Updates); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Config.UpdateGlobalSettings, v1.UpdateGlobalSettingsRequest{Updates: in.Updates})
	})
	addFamilyAdminTool(s, c, "get_server_config", func(ctx context.Context, cl *FamilyAdminClients, in serverArgs) (any, error) {
		if err := requireID("get_server_config", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Config.GetServerConfig, v1.GetServerConfigRequest{ServerId: in.ServerID})
	})
	addFamilyAdminTool(s, c, "update_server_config", func(ctx context.Context, cl *FamilyAdminClients, in adminServerConfigUpdateArgs) (any, error) {
		if err := requireID("update_server_config", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := boundedUpdates("update_server_config", in.Updates); err != nil {
			return nil, err
		}
		window := v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_UNSPECIFIED
		if in.Window != "" {
			n, ok := v1.ConfigApplyWindow_value[strings.ToUpper(in.Window)]
			if !ok {
				return nil, invalidArgument("update_server_config", "window", "unknown ConfigApplyWindow name")
			}
			window = v1.ConfigApplyWindow(n)
		}
		if in.IdleMinutes < 0 || in.IdleMinutes > 10080 {
			return nil, invalidArgument("update_server_config", "idle_minutes", "must be 0 or 1 to 10080")
		}
		if in.ApplyAtUnix < 0 {
			return nil, invalidArgument("update_server_config", "apply_at_unix", "must not be negative")
		}
		return invokeFamilyAdmin(ctx, cl.Config.UpdateServerConfig, v1.UpdateServerConfigRequest{
			ServerId: in.ServerID, Updates: in.Updates, Stage: in.Stage, Window: window,
			ApplyAtUnix: in.ApplyAtUnix, IdleMinutes: in.IdleMinutes,
		})
	})
	addFamilyAdminTool(s, c, "get_update_batch", func(ctx context.Context, cl *FamilyAdminClients, in serverArgs) (any, error) {
		if err := requireID("get_update_batch", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Config.GetUpdateBatch, v1.GetUpdateBatchRequest{ServerId: in.ServerID})
	})
	addFamilyAdminTool(s, c, "discard_update_batch", func(ctx context.Context, cl *FamilyAdminClients, in adminDiscardBatchArgs) (any, error) {
		if err := requireID("discard_update_batch", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := optionalID("discard_update_batch", "item_id", in.ItemID); err != nil {
			return nil, err
		}
		return invokeFamilyAdmin(ctx, cl.Config.DiscardUpdateBatch, v1.DiscardUpdateBatchRequest{
			ServerId: in.ServerID, ItemId: in.ItemID,
		})
	})
}

// addFamilyAdminTool binds a typed tool with an explicit ToolSpec (the shared
// toolSpecs map only knows the first slice). Confirmation is checked before the
// RPC; all failures are converted to the shared structured ToolError shape.
func addFamilyAdminTool[In any](s *sdkmcp.Server, c *FamilyAdminClients, name string,
	run func(context.Context, *FamilyAdminClients, In) (any, error)) {
	if !toolAllowedOn(s, name) {
		return
	}
	spec := familyAdminSpec(name)
	sdkmcp.AddTool(s, &sdkmcp.Tool{Name: spec.Name, Description: spec.Description},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in In) (*sdkmcp.CallToolResult, ToolOutput, error) {
			if spec.Confirm {
				if gate, ok := any(in).(interface{ Confirmation() bool }); !ok || !gate.Confirmation() {
					return failure(spec, &ToolError{
						Kind:      ErrKindConfirmationRequired,
						Tool:      spec.Name,
						Procedure: spec.Procedure,
						Message:   spec.Name + " is a mutation: set confirm=true with the exact target to proceed",
					})
				}
			}
			result, err := run(ctx, c, in)
			if err != nil {
				return failure(spec, err)
			}
			return nil, ToolOutput{Status: "ok", Result: result}, nil
		})
}
