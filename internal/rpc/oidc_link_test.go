// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc/services"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"github.com/stretchr/testify/require"
)

func TestBeginOIDCLinkFromAuthenticatedConnectSession(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{
		Path: filepath.Join(t.TempDir(), "panel.db"), AutoMigrate: true,
	}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.Migrate())
	enforcer, err := rbac.NewEnforcer(store.DB())
	require.NoError(t, err)
	require.NoError(t, enforcer.SeedDefaultPolicies(false))
	manager, err := auth.NewManager(store, nil, &config.AuthConfig{
		SessionTimeout: 3600, JWTSecret: "isolated-oidc-link-connect-fixture",
		Local: config.LocalConfig{Enabled: true},
	})
	require.NoError(t, err)
	const password = "ExplicitLink!2345"
	admin, err := manager.CreateLocalUser(ctx, "link-admin", "", password)
	require.NoError(t, err)
	require.NoError(t, store.AssignRole(ctx, admin.ID, "admin", "local"))
	plainUser, err := manager.CreateLocalUser(ctx, "link-plain", "", password)
	require.NoError(t, err)

	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := "http://" + r.Host
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
			"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
		})
	}))
	t.Cleanup(idp.Close)
	mux := http.NewServeMux()
	panel := httptest.NewServer(mux)
	t.Cleanup(panel.Close)
	log := logger.New()
	handler, err := auth.NewOIDCHandler(manager, store, &config.OIDCConfig{
		Enabled: true, IssuerURI: idp.URL, ClientID: "panel",
		RedirectURL: panel.URL + "/api/v1/auth/oidc/callback",
	}, log)
	require.NoError(t, err)
	authService := services.NewAuthService(store, manager, enforcer, handler, log)
	userService := services.NewUserService(store, manager, log)
	userService.SetOIDCHandler(handler)
	userService.SetEnforcer(enforcer)
	opts := connect.WithInterceptors(NewAuthInterceptor(manager, enforcer, store, log))
	mux.Handle(carbonpanelv1connect.NewAuthServiceHandler(authService, opts))
	mux.Handle(carbonpanelv1connect.NewUserServiceHandler(userService, opts))
	mux.Handle(carbonpanelv1connect.NewRoleServiceHandler(services.NewRoleService(store, enforcer, log), opts))
	mux.HandleFunc("/api/v1/auth/oidc/login", handler.HandleLogin)

	client := panel.Client()
	client.Jar, err = cookiejar.New(nil)
	require.NoError(t, err)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	authClient := carbonpanelv1connect.NewAuthServiceClient(client, panel.URL)
	userClient := carbonpanelv1connect.NewUserServiceClient(client, panel.URL)
	roleClient := carbonpanelv1connect.NewRoleServiceClient(client, panel.URL)
	login := func(name string) string {
		res, err := authClient.Login(ctx, connect.NewRequest(&v1.LoginRequest{Username: name, Password: password}))
		require.NoError(t, err)
		return res.Msg.Token
	}
	begin := func(bearer string) (*connect.Response[v1.BeginOIDCLinkResponse], error) {
		req := connect.NewRequest(&v1.BeginOIDCLinkRequest{})
		if bearer != "" {
			req.Header().Set("Authorization", "Bearer "+bearer)
		}
		return userClient.BeginOIDCLink(ctx, req)
	}
	eligible := func(bearer string) bool {
		req := connect.NewRequest(&v1.GetCurrentUserRequest{})
		req.Header().Set("Authorization", "Bearer "+bearer)
		current, err := authClient.GetCurrentUser(ctx, req)
		require.NoError(t, err)
		return current.Msg.GetCanLinkOidc()
	}

	_, err = begin("")
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	plain := login("link-plain")
	require.False(t, eligible(plain))
	_, err = begin(plain)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	apiToken, apiTokenRow, err := manager.GenerateAPIToken(ctx, admin.ID, "fixture", nil)
	require.NoError(t, err)
	require.False(t, eligible(apiToken))
	_, err = begin(apiToken)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	adminToken := login(admin.Username)
	require.True(t, eligible(adminToken))
	intent, err := begin(adminToken)
	require.NoError(t, err)
	require.NotEmpty(t, intent.Msg.GetLinkToken())
	require.Contains(t, intent.Header().Get("Set-Cookie"), "HttpOnly")
	require.Equal(t, "no-store", intent.Header().Get("Cache-Control"))
	start, err := client.Get(panel.URL + "/api/v1/auth/oidc/login?link_token=" + url.QueryEscape(intent.Msg.GetLinkToken()))
	require.NoError(t, err)
	defer func() { _ = start.Body.Close() }()
	require.Equal(t, http.StatusFound, start.StatusCode)
	require.Contains(t, start.Header.Get("Location"), idp.URL+"/authorize?")

	require.NoError(t, store.AssignRole(ctx, plainUser.ID, "viewer", "oidc"))
	require.NoError(t, store.AssignRole(ctx, plainUser.ID, "operator", "local"))
	updated, err := userClient.UpdateUser(ctx, authenticatedRequest(adminToken, &v1.UpdateUserRequest{
		Id: plainUser.ID, Roles: []string{"viewer", "operator"},
	}))
	require.NoError(t, err)
	require.Equal(t, []string{"operator"}, updated.Msg.User.LocalRoles)
	require.Equal(t, []string{"viewer"}, updated.Msg.User.OidcRoles)
	updated, err = userClient.UpdateUser(ctx, authenticatedRequest(adminToken, &v1.UpdateUserRequest{
		Id: plainUser.ID, LocalRolesUpdate: &v1.LocalRoleSelection{Roles: []string{"viewer"}},
	}))
	require.NoError(t, err)
	require.Equal(t, []string{"viewer"}, updated.Msg.User.LocalRoles)
	require.Equal(t, []string{"viewer"}, updated.Msg.User.OidcRoles)
	_, err = roleClient.UnassignRole(ctx, authenticatedRequest(adminToken, &v1.UnassignRoleRequest{
		UserId: plainUser.ID, RoleName: "viewer",
	}))
	require.NoError(t, err)
	current, err := userClient.GetUser(ctx, authenticatedRequest(adminToken, &v1.GetUserRequest{Id: plainUser.ID}))
	require.NoError(t, err)
	require.Empty(t, current.Msg.User.LocalRoles)
	require.Equal(t, []string{"viewer"}, current.Msg.User.OidcRoles)
	require.Equal(t, []string{"viewer"}, current.Msg.User.Roles)

	require.NoError(t, store.AssignRole(ctx, plainUser.ID, "viewer", "local"))
	updated, err = userClient.UpdateUser(ctx, authenticatedRequest(adminToken, &v1.UpdateUserRequest{
		Id: plainUser.ID, LocalRolesUpdate: &v1.LocalRoleSelection{},
	}))
	require.NoError(t, err)
	require.Empty(t, updated.Msg.User.LocalRoles)
	require.Equal(t, []string{"viewer"}, updated.Msg.User.OidcRoles)

	require.NoError(t, enforcer.SetPermissionsForRole("editor", []rbac.Permission{{
		Resource: rbac.ResourceUsers, Action: rbac.ActionUpdate, ObjectID: "*",
	}}))
	require.NoError(t, store.AssignRole(ctx, plainUser.ID, "editor", "local"))
	newEmail := "should-not-persist@example.test"
	_, err = userClient.UpdateUser(ctx, authenticatedRequest(plain, &v1.UpdateUserRequest{
		Id: plainUser.ID, Email: &newEmail, LocalRolesUpdate: &v1.LocalRoleSelection{Roles: []string{"admin"}},
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	current, err = userClient.GetUser(ctx, authenticatedRequest(adminToken, &v1.GetUserRequest{Id: plainUser.ID}))
	require.NoError(t, err)
	require.Empty(t, current.Msg.User.GetEmail())
	require.Equal(t, []string{"editor"}, current.Msg.User.LocalRoles)

	adminSession, err := store.GetSession(ctx, adminToken)
	require.NoError(t, err)
	_, _, _, err = store.LinkOIDCIdentity(ctx, admin.ID, idp.URL, "linked-subject", adminSession.ID, nil, nil)
	require.NoError(t, err)
	// Use an OIDC-origin fixture session on the same user to exercise the
	// generated API handlers through their production interceptor.
	oidc := login(admin.Username)
	oidcSession, err := store.GetSession(ctx, oidc)
	require.NoError(t, err)
	require.NoError(t, store.DB().Model(&db.Session{}).Where("id = ?", oidcSession.ID).Update("auth_provider", "oidc").Error)
	_, err = authClient.DeleteAPIToken(ctx, authenticatedRequest(oidc, &v1.DeleteAPITokenRequest{Id: apiTokenRow.ID}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = authClient.ListAPITokens(ctx, authenticatedRequest(oidc, &v1.ListAPITokensRequest{}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = manager.ValidateAPIToken(ctx, apiToken)
	require.NoError(t, err)
}

func authenticatedRequest[T any](token string, msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+token)
	return req
}
