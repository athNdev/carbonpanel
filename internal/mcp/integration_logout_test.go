// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc"
	"github.com/athNdev/carbon-panel/internal/rpc/services"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

// TestIntegrationLogoutAPITokenBehavior mounts the REAL controller AuthService
// behind the production auth interceptor and calls auth_service_logout with
// the dp_ token the gateway presents. It observes what actually happens rather
// than trusting the tool description: the controller attempts to delete a
// session keyed by the bearer token, swallows a missing-session error, and
// returns "logged out". The API token remains valid.
func TestIntegrationLogoutAPITokenBehavior(t *testing.T) {
	store := newTestStore(t)
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatalf("SeedDefaultPolicies: %v", err)
	}
	authManager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
		Local: config.LocalConfig{Enabled: true},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	oidc, err := auth.NewOIDCHandler(authManager, store, &config.OIDCConfig{}, logger.New())
	if err != nil {
		t.Fatalf("NewOIDCHandler: %v", err)
	}
	interceptor := rpc.NewAuthInterceptor(authManager, enforcer, store, logger.New())
	token := seedToken(t, store, enforcer, "u-logout", "admin", nil)

	realAuth := services.NewAuthService(store, authManager, enforcer, oidc, logger.New())
	mux := http.NewServeMux()
	path, handler := carbonpanelv1connect.NewAuthServiceHandler(realAuth, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	clients, err := NewClients(ts.Client(), ts.URL, token)
	if err != nil {
		t.Fatalf("NewClients: %v", err)
	}
	session := connectStdioSession(t, NewServer(clients), context.Background())

	before := callTool(t, session, "get_current_user", map[string]any{})
	assertSuccess(t, before, "get_current_user")

	logout := callTool(t, session, "auth_service_logout", map[string]any{"confirm": true})
	assertSuccess(t, logout, "auth_service_logout")
	message, _ := logout.Result.(map[string]any)["message"].(string)
	if message != "logged out" {
		t.Fatalf("real Logout response message: got %q want %q (%+v)", message, "logged out", logout)
	}

	after := callTool(t, session, "get_current_user", map[string]any{})
	assertSuccess(t, after, "get_current_user")
	t.Logf("observed real API-token Logout: message=%q; token still authenticates afterwards", message)

	// Pin the honest description so a future edit cannot reintroduce a
	// revocation claim that the real controller does not implement.
	spec, ok := toolSpecForProcedure("/carbonpanel.v1.AuthService/Logout")
	if !ok {
		t.Fatal("logout tool is not in the combined catalog")
	}
	if !containsFold(spec.Description, "does not revoke") {
		t.Errorf("logout description must state observed no-revocation behavior, got %q", spec.Description)
	}
}

func containsFold(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) && strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}
