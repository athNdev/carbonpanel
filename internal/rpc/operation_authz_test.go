// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// registerOperationListHandler mounts the real OperationService/ListOperations
// procedure behind the server's auth interceptor, so the per-server scoping in
// rbac.ProcedurePermissions is exercised end to end rather than assumed.
func registerOperationListHandler(mux *http.ServeMux, srv *Server) {
	handler := connect.NewUnaryHandler(
		"/carbonpanel.v1.OperationService/ListOperations",
		func(_ context.Context, _ *connect.Request[v1.ListOperationsRequest]) (*connect.Response[v1.ListOperationsResponse], error) {
			return connect.NewResponse(&v1.ListOperationsResponse{}), nil
		},
		connect.WithInterceptors(srv.authInterceptor()),
	)
	mux.Handle("/carbonpanel.v1.OperationService/ListOperations", handler)
}

// seedScopedToken creates an active user with one custom role and an API token,
// returning the raw bearer value.
func seedScopedToken(t *testing.T, store *storage.Store, enforcer *rbac.Enforcer, roleName string, permissions []rbac.Permission) string {
	t.Helper()
	ctx := context.Background()

	if err := enforcer.SetPermissionsForRole(roleName, permissions); err != nil {
		t.Fatalf("SetPermissionsForRole: %v", err)
	}
	if err := store.CreateUser(ctx, &storage.User{ID: "u-scoped", Username: "scoped", AuthProvider: "local", IsActive: true}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := store.AssignRole(ctx, "u-scoped", roleName, "manual"); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}

	raw := "dp_operationjournaltesttoken"
	sum := sha256.Sum256([]byte(raw))
	if err := store.CreateAPIToken(ctx, &storage.APIToken{UserID: "u-scoped", Name: "t", TokenHash: hex.EncodeToString(sum[:])}); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	return raw
}

func callOperationList(t *testing.T, ts *httptest.Server, bearer, serverID string) *connect.Error {
	t.Helper()
	client := connect.NewClient[v1.ListOperationsRequest, v1.ListOperationsResponse](
		http.DefaultClient,
		ts.URL+"/carbonpanel.v1.OperationService/ListOperations",
	)
	req := connect.NewRequest(&v1.ListOperationsRequest{ServerId: serverID})
	req.Header().Set("Authorization", "Bearer "+bearer)
	if _, err := client.CallUnary(context.Background(), req); err != nil {
		connErr, ok := err.(*connect.Error)
		if !ok {
			t.Fatalf("expected a *connect.Error, got %T: %v", err, err)
		}
		return connErr
	}
	return nil
}

// TestOperationJournalIsScopedToTheGrantedServer is the negative authorization
// test for the operation journal (OSS-245).
//
// Every OperationService call carries a server_id, and that field is the RBAC
// object id. A caller granted servers:read on one server must not be able to
// read another server's journal - and, just as important, must not be able to
// omit server_id to widen the query: an empty field becomes "*", which only an
// unscoped grant matches.
func TestOperationJournalIsScopedToTheGrantedServer(t *testing.T) {
	srv, ts := buildHarness(t, false /* anonymousAccess */, false, nil, false)
	mux := ts.Config.Handler.(*http.ServeMux)
	registerOperationListHandler(mux, srv)

	bearer := seedScopedToken(t, srv.store, srv.enforcer, "scoped-journal-reader", []rbac.Permission{
		{Resource: rbac.ResourceServers, Action: rbac.ActionRead, ObjectID: "srv-mine"},
	})

	t.Run("own server is allowed", func(t *testing.T) {
		if err := callOperationList(t, ts, bearer, "srv-mine"); err != nil {
			t.Fatalf("expected the granted server's journal to be readable, got %v", err)
		}
	})

	t.Run("another server is denied", func(t *testing.T) {
		err := callOperationList(t, ts, bearer, "srv-theirs")
		if err == nil {
			t.Fatal("cross-server journal read was allowed")
		}
		if err.Code() != connect.CodePermissionDenied {
			t.Fatalf("expected permission_denied, got %v (%v)", err.Code(), err)
		}
	})

	t.Run("omitting server_id does not widen the query", func(t *testing.T) {
		err := callOperationList(t, ts, bearer, "")
		if err == nil {
			t.Fatal("a scoped caller listed every server's journal by omitting server_id")
		}
		if err.Code() != connect.CodePermissionDenied {
			t.Fatalf("expected permission_denied, got %v (%v)", err.Code(), err)
		}
	})
}

// TestOperationJournalRequiresAuthentication pins the fail-closed default: no
// token and no anonymous access means no journal, not an empty one.
func TestOperationJournalRequiresAuthentication(t *testing.T) {
	srv, ts := buildHarness(t, false /* anonymousAccess */, false, nil, false)
	mux := ts.Config.Handler.(*http.ServeMux)
	registerOperationListHandler(mux, srv)

	err := callOperationList(t, ts, "", "srv-mine")
	if err == nil {
		t.Fatal("unauthenticated journal read was allowed")
	}
	if err.Code() != connect.CodeUnauthenticated {
		t.Fatalf("expected unauthenticated, got %v (%v)", err.Code(), err)
	}
}
