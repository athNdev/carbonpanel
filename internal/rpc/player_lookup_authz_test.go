// SPDX-License-Identifier: MPL-2.0
package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

// playerRPCFixture builds a real PlayerService handler behind the production
// auth interceptor with an exact set of anonymous-role grants.
func playerRPCFixture(t *testing.T, grants []rbac.Permission) (*playerFixtureService, carbonpanelv1connect.PlayerServiceClient, func()) {
	t.Helper()
	store := newTestStore(t)
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatal(err)
	}
	if len(grants) > 0 {
		if err := enforcer.SetPermissionsForRole("anonymous", grants); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
		Local: config.LocalConfig{Enabled: true}, AnonymousAccess: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: store, authManager: manager, enforcer: enforcer, log: logger.New()}
	fixture := &playerFixtureService{}
	mux := http.NewServeMux()
	path, handler := carbonpanelv1connect.NewPlayerServiceHandler(fixture, connect.WithInterceptors(server.authInterceptor()))
	mux.Handle(path, handler)
	httpServer := httptest.NewServer(mux)
	client := carbonpanelv1connect.NewPlayerServiceClient(httpServer.Client(), httpServer.URL)
	return fixture, client, httpServer.Close
}

// TestLookupPlayerRequiresGlobalPlayersRead proves the new RPC is fail-closed:
// no grant and a server-scoped grant are both denied; only a global players
// read on "*" reaches the handler.
func TestLookupPlayerRequiresGlobalPlayersRead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		grant []rbac.Permission
		code  connect.Code
	}{
		{name: "no players grant", code: connect.CodePermissionDenied},
		{name: "server-scoped read is not global", grant: []rbac.Permission{{Resource: rbac.ResourcePlayers, Action: rbac.ActionRead, ObjectID: "srv-1"}}, code: connect.CodePermissionDenied},
		{name: "global players read", grant: []rbac.Permission{{Resource: rbac.ResourcePlayers, Action: rbac.ActionRead, ObjectID: "*"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, client, closeFn := playerRPCFixture(t, tc.grant)
			defer closeFn()
			_, err := client.LookupPlayer(context.Background(), connect.NewRequest(&v1.LookupPlayerRequest{Query: "SomeName", IncludeSkin: true}))
			if tc.code != 0 {
				if connect.CodeOf(err) != tc.code || fixture.calls != 0 {
					t.Fatalf("denial=%v calls=%d, want code=%v calls=0", err, fixture.calls, tc.code)
				}
				return
			}
			if err != nil || fixture.calls != 1 {
				t.Fatalf("allowed call error=%v calls=%d", err, fixture.calls)
			}
		})
	}
}

// TestManualWhitelistAddDeniedOnOtherServer proves the existing server_id RBAC
// scope also applies when a manual name/UUID is used instead of an identity
// key: a grant for srv-1 cannot add to srv-2.
func TestManualWhitelistAddDeniedOnOtherServer(t *testing.T) {
	fixture, client, closeFn := playerRPCFixture(t, []rbac.Permission{
		{Resource: rbac.ResourcePlayers, Action: rbac.ActionAccessWrite, ObjectID: "srv-1"},
	})
	defer closeFn()
	_, err := client.SetWhitelistEntry(context.Background(), connect.NewRequest(&v1.SetWhitelistEntryRequest{
		ServerId: "srv-2", PlayerQuery: "CrossServerName", RequestId: "cross-server-1",
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied || fixture.calls != 0 {
		t.Fatalf("cross-server denial=%v calls=%d, want denied calls=0", err, fixture.calls)
	}
}

// TestManualWhitelistAddAllowedOnGrantedServer confirms the same raw-name path
// is reachable when the actor holds the matching server-scoped grant.
func TestManualWhitelistAddAllowedOnGrantedServer(t *testing.T) {
	fixture, client, closeFn := playerRPCFixture(t, []rbac.Permission{
		{Resource: rbac.ResourcePlayers, Action: rbac.ActionAccessWrite, ObjectID: "srv-1"},
	})
	defer closeFn()
	_, err := client.SetWhitelistEntry(context.Background(), connect.NewRequest(&v1.SetWhitelistEntryRequest{
		ServerId: "srv-1", PlayerQuery: "FreshName", RequestId: "granted-server-1",
	}))
	if err != nil || fixture.calls != 1 {
		t.Fatalf("granted call error=%v calls=%d", err, fixture.calls)
	}
}
