// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// legacyAuthInterceptor is the exact pre-extraction body of Server.authInterceptor.
// The parity test drives it and NewAuthInterceptor through identical requests
// and compares the Connect code and message byte for byte.
func legacyAuthInterceptor(authManager *auth.Manager, enforcer *rbac.Enforcer, store *storage.Store, log *logger.Logger) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			procedure := req.Spec().Procedure

			if rbac.PublicProcedures[procedure] {
				return next(ctx, req)
			}

			user, err := authManager.AuthenticateFromHeader(ctx, req.Header().Get("Authorization"))
			if err != nil {
				log.Debug("Auth: Token validation failed for %s: %v", procedure, err)
				return nil, connect.NewError(connect.CodeUnauthenticated, err)
			}

			ctx = auth.WithUser(ctx, user)

			if rbac.AuthenticatedOnlyProcedures[procedure] {
				return next(ctx, req)
			}

			perm, ok := rbac.ProcedurePermissions[procedure]
			if !ok {
				log.Warn("RBAC: denying request for unmapped procedure %s (no public/authenticated-only/permission entry)", procedure)
				return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("procedure %s has no permission mapping and is denied by default", procedure))
			}

			if enforcer == nil {
				log.Error("RBAC: denying request for %s because the RBAC enforcer is unavailable", procedure)
				return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("rbac enforcer unavailable"))
			}

			objectID := "*"
			if perm.ObjectIDField != "" {
				objectID = extractObjectID(req, perm.ObjectIDField)
			}
			allowed, err := enforcer.Enforce(user.Roles, perm.Resource, perm.Action, objectID)
			if err != nil {
				log.Error("RBAC enforcement error: %v", err)
				return nil, connect.NewError(connect.CodeInternal, err)
			}
			if !allowed {
				if perm.Resource != rbac.ResourceSubusers && perm.ObjectIDField == "server_id" && store != nil {
					want := perm.Resource + "." + perm.Action
					for _, p := range store.SubuserPermissions(ctx, objectID, user.ID) {
						if p == want {
							allowed = true
							break
						}
					}
				}
			}
			if !allowed {
				return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("insufficient permissions for %s/%s", perm.Resource, perm.Action))
			}

			return next(ctx, req)
		}
	}
}

func parityEcho[Req, Res any](procedure string, ic connect.UnaryInterceptorFunc) http.Handler {
	return connect.NewUnaryHandler(procedure,
		func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error) {
			var res Res
			return connect.NewResponse(&res), nil
		}, connect.WithInterceptors(ic))
}

func parityServer(t *testing.T, ic connect.UnaryInterceptorFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/carbonpanel.v1.AuthService/GetAuthStatus", parityEcho[v1.GetAuthStatusRequest, v1.GetAuthStatusResponse]("/carbonpanel.v1.AuthService/GetAuthStatus", ic))
	mux.Handle("/carbonpanel.v1.AuthService/GetCurrentUser", parityEcho[v1.GetCurrentUserRequest, v1.GetCurrentUserResponse]("/carbonpanel.v1.AuthService/GetCurrentUser", ic))
	mux.Handle("/carbonpanel.v1.ServerService/GetServer", parityEcho[v1.GetServerRequest, v1.GetServerResponse]("/carbonpanel.v1.ServerService/GetServer", ic))
	mux.Handle(testUnmappedProcedure, parityEcho[v1.GetAuthStatusRequest, v1.GetAuthStatusResponse](testUnmappedProcedure, ic))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func callParity[Req, Res any](t *testing.T, ts *httptest.Server, procedure string, req Req, bearer string) (string, string) {
	t.Helper()
	client := connect.NewClient[Req, Res](ts.Client(), ts.URL+procedure)
	r := connect.NewRequest(&req)
	if bearer != "" {
		r.Header().Set("Authorization", "Bearer "+bearer)
	}
	_, err := client.CallUnary(context.Background(), r)
	if err == nil {
		return "ok", ""
	}
	ce, ok := err.(*connect.Error)
	if !ok {
		t.Fatalf("expected *connect.Error, got %T: %v", err, err)
	}
	return ce.Code().String(), ce.Message()
}

func TestAuthInterceptorExtractionParity(t *testing.T) {
	store := newTestStore(t)
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatal(err)
	}
	authCfg := &config.AuthConfig{
		Local:           config.LocalConfig{Enabled: true},
		OIDC:            config.OIDCConfig{Enabled: false},
		AnonymousAccess: false,
		AllowNoAuth:     false,
	}
	authManager, err := auth.NewManager(store, enforcer, authCfg)
	if err != nil {
		t.Fatal(err)
	}
	noEnforcerManager, err := auth.NewManager(store, nil, authCfg)
	if err != nil {
		t.Fatal(err)
	}
	token := seedScopedToken(t, store, enforcer, "parity-role", []rbac.Permission{
		{Resource: rbac.ResourceServers, Action: rbac.ActionRead, ObjectID: "srv-a"},
	})
	log := logger.New()

	legacy := parityServer(t, legacyAuthInterceptor(authManager, enforcer, store, log))
	extracted := parityServer(t, NewAuthInterceptor(authManager, enforcer, store, log))
	nilLegacy := parityServer(t, legacyAuthInterceptor(noEnforcerManager, nil, store, log))
	nilExtracted := parityServer(t, NewAuthInterceptor(noEnforcerManager, nil, store, log))

	const (
		public     = "/carbonpanel.v1.AuthService/GetAuthStatus"
		authOnly   = "/carbonpanel.v1.AuthService/GetCurrentUser"
		permission = "/carbonpanel.v1.ServerService/GetServer"
	)
	tests := []struct {
		name    string
		legacy  *httptest.Server
		current *httptest.Server
		call    func(*testing.T, *httptest.Server) (string, string)
	}{
		{"public without credentials", legacy, extracted, func(t *testing.T, ts *httptest.Server) (string, string) {
			return callParity[v1.GetAuthStatusRequest, v1.GetAuthStatusResponse](t, ts, public, v1.GetAuthStatusRequest{}, "")
		}},
		{"authenticated-only with token", legacy, extracted, func(t *testing.T, ts *httptest.Server) (string, string) {
			return callParity[v1.GetCurrentUserRequest, v1.GetCurrentUserResponse](t, ts, authOnly, v1.GetCurrentUserRequest{}, token)
		}},
		{"unauthenticated denial", legacy, extracted, func(t *testing.T, ts *httptest.Server) (string, string) {
			return callParity[v1.GetServerRequest, v1.GetServerResponse](t, ts, permission, v1.GetServerRequest{Id: "srv-a"}, "")
		}},
		{"scoped object allowed", legacy, extracted, func(t *testing.T, ts *httptest.Server) (string, string) {
			return callParity[v1.GetServerRequest, v1.GetServerResponse](t, ts, permission, v1.GetServerRequest{Id: "srv-a"}, token)
		}},
		{"scoped object denied", legacy, extracted, func(t *testing.T, ts *httptest.Server) (string, string) {
			return callParity[v1.GetServerRequest, v1.GetServerResponse](t, ts, permission, v1.GetServerRequest{Id: "srv-b"}, token)
		}},
		{"unmapped procedure denied", legacy, extracted, func(t *testing.T, ts *httptest.Server) (string, string) {
			return callParity[v1.GetAuthStatusRequest, v1.GetAuthStatusResponse](t, ts, testUnmappedProcedure, v1.GetAuthStatusRequest{}, token)
		}},
		{"nil enforcer denied", nilLegacy, nilExtracted, func(t *testing.T, ts *httptest.Server) (string, string) {
			return callParity[v1.GetServerRequest, v1.GetServerResponse](t, ts, permission, v1.GetServerRequest{Id: "srv-a"}, token)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldCode, oldMsg := tc.call(t, tc.legacy)
			newCode, newMsg := tc.call(t, tc.current)
			if oldCode != newCode || oldMsg != newMsg {
				t.Fatalf("parity break: legacy=(%q,%q) extracted=(%q,%q)", oldCode, oldMsg, newCode, newMsg)
			}
		})
	}
}
