// SPDX-License-Identifier: MPL-2.0
package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/rbac"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

const worldProcedurePrefix = "/carbonpanel.v1.WorldService/"

func worldProcedures() []string {
	var paths []string
	for procedure := range rbac.ProcedurePermissions {
		if strings.HasPrefix(procedure, worldProcedurePrefix) {
			paths = append(paths, procedure)
		}
	}
	sort.Strings(paths)
	return paths
}

func registerWorldBindHandler(mux *http.ServeMux, srv *Server) {
	const procedure = "/carbonpanel.v1.WorldService/BindWorld"
	handler := connect.NewUnaryHandler(
		procedure,
		func(_ context.Context, _ *connect.Request[v1.BindWorldRequest]) (*connect.Response[v1.BindWorldResponse], error) {
			return connect.NewResponse(&v1.BindWorldResponse{}), nil
		},
		connect.WithInterceptors(srv.authInterceptor()),
	)
	mux.Handle(procedure, handler)
}

func callWorldBind(t *testing.T, ts *httptest.Server, bearer, serverID string) *connect.Error {
	t.Helper()
	client := connect.NewClient[v1.BindWorldRequest, v1.BindWorldResponse](
		http.DefaultClient, ts.URL+"/carbonpanel.v1.WorldService/BindWorld")
	req := connect.NewRequest(&v1.BindWorldRequest{ServerId: serverID})
	if bearer != "" {
		req.Header().Set("Authorization", "Bearer "+bearer)
	}
	if _, err := client.CallUnary(context.Background(), req); err != nil {
		connErr, ok := err.(*connect.Error)
		if !ok {
			t.Fatalf("expected *connect.Error, got %T: %v", err, err)
		}
		return connErr
	}
	return nil
}

// TestEveryWorldProcedureIsFailClosed enumerates every generated WorldService
// procedure from the RBAC map. A reader with only a server-scoped grant cannot
// call any of them: unscoped procedures fall back to "*" and mutations require
// a different action. This is the negative coverage for all 18 RPCs.
func TestEveryWorldProcedureIsFailClosed(t *testing.T) {
	paths := worldProcedures()
	if len(paths) != 18 {
		t.Fatalf("WorldService procedure count = %d, want 18: %v", len(paths), paths)
	}
	srv, ts := buildHarness(t, false, false, nil, false)
	mux := ts.Config.Handler.(*http.ServeMux)
	for _, procedure := range paths {
		registerEcho(mux, srv, procedure)
	}
	bearer := seedScopedToken(t, srv.store, srv.enforcer, "scoped-world-reader", []rbac.Permission{
		{Resource: rbac.ResourceWorlds, Action: rbac.ActionRead, ObjectID: "srv-mine"},
	})
	for _, procedure := range paths {
		err := callEcho(t, ts, procedure, bearer)
		if err == nil || err.Code() != connect.CodePermissionDenied {
			t.Errorf("%s was not denied for a narrowly scoped reader: %v", procedure, err)
		}
	}
}

// TestWorldReadProceduresAllowUnscopedReadGrant pins the positive path: a
// worlds:read grant on "*" permits reads, but never a mutation.
func TestWorldReadProceduresAllowUnscopedReadGrant(t *testing.T) {
	paths := worldProcedures()
	srv, ts := buildHarness(t, false, false, nil, false)
	mux := ts.Config.Handler.(*http.ServeMux)
	for _, procedure := range paths {
		registerEcho(mux, srv, procedure)
	}
	bearer := seedScopedToken(t, srv.store, srv.enforcer, "unscoped-world-reader", []rbac.Permission{
		{Resource: rbac.ResourceWorlds, Action: rbac.ActionRead, ObjectID: "*"},
	})
	for _, procedure := range paths {
		err := callEcho(t, ts, procedure, bearer)
		wantDenied := rbac.ProcedurePermissions[procedure].Action != rbac.ActionRead
		if wantDenied {
			if err == nil || err.Code() != connect.CodePermissionDenied {
				t.Errorf("mutation %s was allowed by a read-only grant", procedure)
			}
			continue
		}
		if err != nil {
			t.Errorf("read procedure %s was denied by a read grant: %v", procedure, err)
		}
	}
}

// TestWorldBindIsFailClosed pins the new worlds resource: a subuser grant for
// one server cannot bind another, and omitting server_id is not a wildcard.
func TestWorldBindIsFailClosed(t *testing.T) {
	srv, ts := buildHarness(t, false, false, nil, false)
	registerWorldBindHandler(ts.Config.Handler.(*http.ServeMux), srv)

	bearer := seedScopedToken(t, srv.store, srv.enforcer, "scoped-world-binder", []rbac.Permission{
		{Resource: rbac.ResourceWorlds, Action: rbac.ActionUpdate, ObjectID: "srv-mine"},
	})

	t.Run("granted server allowed", func(t *testing.T) {
		if err := callWorldBind(t, ts, bearer, "srv-mine"); err != nil {
			t.Fatalf("granted server denied: %v", err)
		}
	})
	t.Run("other server denied", func(t *testing.T) {
		err := callWorldBind(t, ts, bearer, "srv-theirs")
		if err == nil || err.Code() != connect.CodePermissionDenied {
			t.Fatalf("expected permission_denied, got %v", err)
		}
	})
	t.Run("empty server_id is not a wildcard", func(t *testing.T) {
		err := callWorldBind(t, ts, bearer, "")
		if err == nil || err.Code() != connect.CodePermissionDenied {
			t.Fatalf("expected permission_denied for empty scope, got %v", err)
		}
	})
	t.Run("unauthenticated denied", func(t *testing.T) {
		err := callWorldBind(t, ts, "", "srv-mine")
		if err == nil || err.Code() != connect.CodeUnauthenticated {
			t.Fatalf("expected unauthenticated, got %v", err)
		}
	})
}
