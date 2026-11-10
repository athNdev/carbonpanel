// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/rbac"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func registerGetServerHandler(mux *http.ServeMux, srv *Server) {
	handler := connect.NewUnaryHandler(
		"/carbonpanel.v1.ServerService/GetServer",
		func(_ context.Context, _ *connect.Request[v1.GetServerRequest]) (*connect.Response[v1.GetServerResponse], error) {
			return connect.NewResponse(&v1.GetServerResponse{}), nil
		},
		connect.WithInterceptors(srv.authInterceptor()),
	)
	mux.Handle("/carbonpanel.v1.ServerService/GetServer", handler)
}

func callGetServer(t *testing.T, ts *httptest.Server, bearer, serverID string) *connect.Error {
	t.Helper()
	client := connect.NewClient[v1.GetServerRequest, v1.GetServerResponse](
		http.DefaultClient,
		ts.URL+"/carbonpanel.v1.ServerService/GetServer",
	)
	req := connect.NewRequest(&v1.GetServerRequest{Id: serverID})
	if bearer != "" {
		req.Header().Set("Authorization", "Bearer "+bearer)
	}
	if _, err := client.CallUnary(context.Background(), req); err != nil {
		connErr, ok := err.(*connect.Error)
		if !ok {
			t.Fatalf("expected a *connect.Error, got %T: %v", err, err)
		}
		return connErr
	}
	return nil
}

// TestGetServerIsScopedToTheGrantedServer is the negative authorization test
// for live SLP/player fields on GetServer (OSS-245). A caller granted
// servers:read on one server must not read another server's join evidence.
func TestGetServerIsScopedToTheGrantedServer(t *testing.T) {
	srv, ts := buildHarness(t, false /* anonymousAccess */, false, nil, false)
	mux := ts.Config.Handler.(*http.ServeMux)
	registerGetServerHandler(mux, srv)

	bearer := seedScopedToken(t, srv.store, srv.enforcer, "scoped-server-reader", []rbac.Permission{
		{Resource: rbac.ResourceServers, Action: rbac.ActionRead, ObjectID: "srv-mine"},
	})

	t.Run("own server is allowed", func(t *testing.T) {
		if err := callGetServer(t, ts, bearer, "srv-mine"); err != nil {
			t.Fatalf("expected the granted server to be readable, got %v", err)
		}
	})

	t.Run("another server is denied", func(t *testing.T) {
		err := callGetServer(t, ts, bearer, "srv-theirs")
		if err == nil {
			t.Fatal("cross-server GetServer was allowed")
		}
		if err.Code() != connect.CodePermissionDenied {
			t.Fatalf("expected permission_denied, got %v (%v)", err.Code(), err)
		}
	})
}

func TestGetServerRequiresAuthentication(t *testing.T) {
	srv, ts := buildHarness(t, false /* anonymousAccess */, false, nil, false)
	mux := ts.Config.Handler.(*http.ServeMux)
	registerGetServerHandler(mux, srv)

	err := callGetServer(t, ts, "", "srv-mine")
	if err == nil {
		t.Fatal("unauthenticated GetServer was allowed")
	}
	if err.Code() != connect.CodeUnauthenticated {
		t.Fatalf("expected unauthenticated, got %v (%v)", err.Code(), err)
	}
}
