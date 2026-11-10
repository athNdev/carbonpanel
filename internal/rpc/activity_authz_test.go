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

func registerActivityListHandler(mux *http.ServeMux, srv *Server) {
	handler := connect.NewUnaryHandler(
		"/carbonpanel.v1.ActivityService/ListActivityLogs",
		func(_ context.Context, _ *connect.Request[v1.ListActivityLogsRequest]) (*connect.Response[v1.ListActivityLogsResponse], error) {
			return connect.NewResponse(&v1.ListActivityLogsResponse{}), nil
		},
		connect.WithInterceptors(srv.authInterceptor()),
	)
	mux.Handle("/carbonpanel.v1.ActivityService/ListActivityLogs", handler)
}

func callActivityList(t *testing.T, ts *httptest.Server, bearer, serverID string) *connect.Error {
	t.Helper()
	client := connect.NewClient[v1.ListActivityLogsRequest, v1.ListActivityLogsResponse](
		http.DefaultClient,
		ts.URL+"/carbonpanel.v1.ActivityService/ListActivityLogs",
	)
	req := connect.NewRequest(&v1.ListActivityLogsRequest{ServerId: serverID})
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

// TestActivityLogsAreScopedToTheGrantedServer is the negative authorization
// test for activity request ids (OSS-245). A caller granted activity:read on
// one server must not list another server's feed — including the request ids
// that join that feed to the operation journal — and must not omit server_id
// to widen the query.
func TestActivityLogsAreScopedToTheGrantedServer(t *testing.T) {
	srv, ts := buildHarness(t, false /* anonymousAccess */, false, nil, false)
	mux := ts.Config.Handler.(*http.ServeMux)
	registerActivityListHandler(mux, srv)

	bearer := seedScopedToken(t, srv.store, srv.enforcer, "scoped-activity-reader", []rbac.Permission{
		{Resource: rbac.ResourceActivity, Action: rbac.ActionRead, ObjectID: "srv-mine"},
	})

	t.Run("own server is allowed", func(t *testing.T) {
		if err := callActivityList(t, ts, bearer, "srv-mine"); err != nil {
			t.Fatalf("expected the granted server's activity to be readable, got %v", err)
		}
	})

	t.Run("another server is denied", func(t *testing.T) {
		err := callActivityList(t, ts, bearer, "srv-theirs")
		if err == nil {
			t.Fatal("cross-server activity read was allowed")
		}
		if err.Code() != connect.CodePermissionDenied {
			t.Fatalf("expected permission_denied, got %v (%v)", err.Code(), err)
		}
	})

	t.Run("omitting server_id does not widen the query", func(t *testing.T) {
		err := callActivityList(t, ts, bearer, "")
		if err == nil {
			t.Fatal("a scoped caller listed every server's activity by omitting server_id")
		}
		if err.Code() != connect.CodePermissionDenied {
			t.Fatalf("expected permission_denied, got %v (%v)", err.Code(), err)
		}
	})
}

func TestActivityLogsRequireAuthentication(t *testing.T) {
	srv, ts := buildHarness(t, false /* anonymousAccess */, false, nil, false)
	mux := ts.Config.Handler.(*http.ServeMux)
	registerActivityListHandler(mux, srv)

	err := callActivityList(t, ts, "", "srv-mine")
	if err == nil {
		t.Fatal("unauthenticated activity read was allowed")
	}
	if err.Code() != connect.CodeUnauthenticated {
		t.Fatalf("expected unauthenticated, got %v (%v)", err.Code(), err)
	}
}
