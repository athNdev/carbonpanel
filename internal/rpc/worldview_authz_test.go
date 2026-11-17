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
	"github.com/athNdev/carbon-panel/internal/worldview"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

var worldViewProcedures = []string{
	"/carbonpanel.v1.WorldViewService/ListWorldViewTargets",
	"/carbonpanel.v1.WorldViewService/GetWorldViewStatus",
	"/carbonpanel.v1.WorldViewService/GetWorldViewSession",
	"/carbonpanel.v1.WorldViewService/ListCapabilities",
	"/carbonpanel.v1.WorldViewService/ListMapOverlays",
	"/carbonpanel.v1.WorldViewService/SetMapOverlay",
	"/carbonpanel.v1.WorldViewService/SetPlayerVisibility",
}

func registerWorldViewEcho[Req, Resp any](mux *http.ServeMux, srv *Server, procedure string) {
	handler := connect.NewUnaryHandler(
		procedure,
		func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error) {
			return connect.NewResponse(new(Resp)), nil
		},
		connect.WithInterceptors(srv.authInterceptor()),
	)
	mux.Handle(procedure, handler)
}

func registerAllWorldViewEchoes(mux *http.ServeMux, srv *Server) {
	registerWorldViewEcho[v1.ListWorldViewTargetsRequest, v1.ListWorldViewTargetsResponse](mux, srv, worldViewProcedures[0])
	registerWorldViewEcho[v1.GetWorldViewStatusRequest, v1.GetWorldViewStatusResponse](mux, srv, worldViewProcedures[1])
	registerWorldViewEcho[v1.GetWorldViewSessionRequest, v1.GetWorldViewSessionResponse](mux, srv, worldViewProcedures[2])
	registerWorldViewEcho[v1.ListCapabilitiesRequest, v1.ListCapabilitiesResponse](mux, srv, worldViewProcedures[3])
	registerWorldViewEcho[v1.ListMapOverlaysRequest, v1.ListMapOverlaysResponse](mux, srv, worldViewProcedures[4])
	registerWorldViewEcho[v1.SetMapOverlayRequest, v1.SetMapOverlayResponse](mux, srv, worldViewProcedures[5])
	registerWorldViewEcho[v1.SetPlayerVisibilityRequest, v1.SetPlayerVisibilityResponse](mux, srv, worldViewProcedures[6])
	// Deliberately unmapped: it must still be denied by the interceptor.
	registerWorldViewEcho[v1.ListWorldViewTargetsRequest, v1.ListWorldViewTargetsResponse](mux, srv, "/carbonpanel.v1.WorldViewService/UnmappedProcedure")
}

func callWorldView[Req, Resp any](t *testing.T, ts *httptest.Server, procedure, bearer string, req *connect.Request[Req]) *connect.Error {
	t.Helper()
	client := connect.NewClient[Req, Resp](http.DefaultClient, ts.URL+procedure)
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

// seedWorldViewToken creates an isolated user/role/API token so multiple
// distinct grants can coexist in one harness.
func seedWorldViewToken(t *testing.T, srv *Server, id, role string, permissions []rbac.Permission) string {
	t.Helper()
	ctx := context.Background()
	if err := srv.enforcer.SetPermissionsForRole(role, permissions); err != nil {
		t.Fatalf("SetPermissionsForRole: %v", err)
	}
	if err := srv.store.CreateUser(ctx, &storage.User{
		ID: id, Username: id, AuthProvider: "local", IsActive: true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := srv.store.AssignRole(ctx, id, role, "manual"); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	raw := "dp_worldview_" + id
	sum := sha256.Sum256([]byte(raw))
	if err := srv.store.CreateAPIToken(ctx, &storage.APIToken{
		UserID: id, Name: "worldview-test", TokenHash: hex.EncodeToString(sum[:]),
	}); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	return raw
}

// TestWorldViewProcedurePermissionEnforcement checks every generated procedure
// through the real authInterceptor: scoped reads, privileged mutations,
// anonymous, nil enforcer and unmapped procedures all fail closed (OSS-354).
func TestWorldViewProcedurePermissionEnforcement(t *testing.T) {
	srv, ts := buildHarness(t, false /* anonymousAccess */, false, nil, false)
	mux := ts.Config.Handler.(*http.ServeMux)
	registerAllWorldViewEchoes(mux, srv)

	globalRead := seedWorldViewToken(t, srv, "wv-global", "wv-global-role", []rbac.Permission{
		{Resource: rbac.ResourceWorldView, Action: rbac.ActionRead, ObjectID: "*"},
	})
	scopedRead := seedWorldViewToken(t, srv, "wv-scoped", "wv-scoped-role", []rbac.Permission{
		{Resource: rbac.ResourceWorldView, Action: rbac.ActionRead, ObjectID: "srv-a"},
	})
	noGrant := seedWorldViewToken(t, srv, "wv-none", "wv-no-grant-role", nil)

	t.Run("global read permits read procedures but not mutations", func(t *testing.T) {
		for _, proc := range worldViewProcedures[:5] {
			req := &v1.ListWorldViewTargetsRequest{}
			if proc != worldViewProcedures[0] {
				// The typed bodies differ but the interceptor only reads headers,
				// so an empty proto of the same wire kind is sufficient here.
				req = &v1.ListWorldViewTargetsRequest{}
			}
			if err := callWorldView[v1.ListWorldViewTargetsRequest, v1.ListWorldViewTargetsResponse](t, ts, proc, globalRead, connect.NewRequest(req)); err != nil {
				t.Errorf("%s unexpectedly denied: %v", proc, err)
			}
		}
		for _, proc := range []string{worldViewProcedures[5], worldViewProcedures[6]} {
			err := callWorldView[v1.SetMapOverlayRequest, v1.SetMapOverlayResponse](t, ts, proc, globalRead, connect.NewRequest(&v1.SetMapOverlayRequest{ServerId: "srv-a"}))
			if err == nil || err.Code() != connect.CodePermissionDenied {
				t.Errorf("%s with read-only grant error = %v, want permission_denied", proc, err)
			}
		}
	})

	t.Run("server-scoped read cannot list globally or cross servers", func(t *testing.T) {
		if err := callWorldView[v1.GetWorldViewStatusRequest, v1.GetWorldViewStatusResponse](t, ts, worldViewProcedures[1], scopedRead,
			connect.NewRequest(&v1.GetWorldViewStatusRequest{ServerId: "srv-a"})); err != nil {
			t.Errorf("own server read denied: %v", err)
		}
		if err := callWorldView[v1.GetWorldViewStatusRequest, v1.GetWorldViewStatusResponse](t, ts, worldViewProcedures[1], scopedRead,
			connect.NewRequest(&v1.GetWorldViewStatusRequest{ServerId: "srv-b"})); err == nil || err.Code() != connect.CodePermissionDenied {
			t.Errorf("cross-server read error = %v, want permission_denied", err)
		}
		if err := callWorldView[v1.ListWorldViewTargetsRequest, v1.ListWorldViewTargetsResponse](t, ts, worldViewProcedures[0], scopedRead,
			connect.NewRequest(&v1.ListWorldViewTargetsRequest{})); err == nil || err.Code() != connect.CodePermissionDenied {
			t.Errorf("scoped caller listed all targets: %v", err)
		}
	})

	t.Run("no grant is denied", func(t *testing.T) {
		err := callWorldView[v1.GetWorldViewStatusRequest, v1.GetWorldViewStatusResponse](t, ts, worldViewProcedures[1], noGrant,
			connect.NewRequest(&v1.GetWorldViewStatusRequest{ServerId: "srv-a"}))
		if err == nil || err.Code() != connect.CodePermissionDenied {
			t.Fatalf("no-grant error = %v, want permission_denied", err)
		}
	})

	t.Run("unmapped procedure is denied", func(t *testing.T) {
		err := callWorldView[v1.ListWorldViewTargetsRequest, v1.ListWorldViewTargetsResponse](t, ts,
			"/carbonpanel.v1.WorldViewService/UnmappedProcedure", globalRead, connect.NewRequest(&v1.ListWorldViewTargetsRequest{}))
		if err == nil || err.Code() != connect.CodePermissionDenied {
			t.Fatalf("unmapped procedure error = %v, want permission_denied", err)
		}
	})
}

// TestWorldViewAnonymousAndNilEnforcerDenied pins the fail-closed paths shared
// by every new procedure: an anonymous role has no map grant and a missing
// enforcer never bypasses the check.
func TestWorldViewAnonymousAndNilEnforcerDenied(t *testing.T) {
	anonSrv, anonTS := buildHarness(t, true /* anonymousAccess */, false, nil, false)
	registerAllWorldViewEchoes(anonTS.Config.Handler.(*http.ServeMux), anonSrv)
	if err := callWorldView[v1.ListWorldViewTargetsRequest, v1.ListWorldViewTargetsResponse](t, anonTS,
		worldViewProcedures[0], "", connect.NewRequest(&v1.ListWorldViewTargetsRequest{})); err == nil || err.Code() != connect.CodePermissionDenied {
		t.Fatalf("anonymous map read error = %v, want permission_denied", err)
	}

	nilSrv, nilTS := buildHarness(t, false, true /* noAuth */, nil, true /* useOverride: nil enforcer */)
	registerAllWorldViewEchoes(nilTS.Config.Handler.(*http.ServeMux), nilSrv)
	if err := callWorldView[v1.ListWorldViewTargetsRequest, v1.ListWorldViewTargetsResponse](t, nilTS,
		worldViewProcedures[0], "", connect.NewRequest(&v1.ListWorldViewTargetsRequest{})); err == nil || err.Code() != connect.CodePermissionDenied {
		t.Fatalf("nil-enforcer map read error = %v, want permission_denied", err)
	}
}

// TestGeneratedWorldViewHandlerIsFailClosed registers the real generated
// handler (not an echo) and confirms a read-only caller cannot reach a
// privileged procedure through it.
func TestGeneratedWorldViewHandlerIsFailClosed(t *testing.T) {
	srv, ts := buildHarness(t, false, false, nil, false)
	reader := seedWorldViewToken(t, srv, "wv-gen-reader", "wv-gen-role", []rbac.Permission{
		{Resource: rbac.ResourceWorldView, Action: rbac.ActionRead, ObjectID: "*"},
	})
	service := worldview.NewService(worldview.ServiceDeps{Store: srv.store, Authorizer: srv.enforcer})
	path, handler := carbonpanelv1connect.NewWorldViewServiceHandler(service, connect.WithInterceptors(srv.authInterceptor()))
	ts.Config.Handler.(*http.ServeMux).Handle(path, handler)

	err := callWorldView[v1.SetMapOverlayRequest, v1.SetMapOverlayResponse](t, ts, worldViewProcedures[5], reader,
		connect.NewRequest(&v1.SetMapOverlayRequest{ServerId: "srv-a"}))
	if err == nil || err.Code() != connect.CodePermissionDenied {
		t.Fatalf("generated SetMapOverlay with read-only grant error = %v, want permission_denied", err)
	}
}
