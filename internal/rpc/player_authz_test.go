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

type playerFixtureService struct {
	carbonpanelv1connect.UnimplementedPlayerServiceHandler
	calls int
}

func (s *playerFixtureService) ListPlayers(context.Context, *connect.Request[v1.ListPlayersRequest]) (*connect.Response[v1.ListPlayersResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.ListPlayersResponse{}), nil
}

func (s *playerFixtureService) KickPlayer(context.Context, *connect.Request[v1.KickPlayerRequest]) (*connect.Response[v1.KickPlayerResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.KickPlayerResponse{}), nil
}

func (s *playerFixtureService) SetOperator(context.Context, *connect.Request[v1.SetOperatorRequest]) (*connect.Response[v1.SetOperatorResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.SetOperatorResponse{}), nil
}

func (s *playerFixtureService) GetPlayerSkin(context.Context, *connect.Request[v1.GetPlayerSkinRequest]) (*connect.Response[v1.GetPlayerSkinResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.GetPlayerSkinResponse{}), nil
}

// TestPlayerProceduresFailClosed exercises the real interceptor for the new
// resource. Every case asserts the handler ran zero times on denial, so a UI
// that merely hides a button cannot accidentally authorize the RPC.
func TestPlayerProceduresFailClosed(t *testing.T) {
	for _, fixture := range []struct {
		name         string
		noAuth, anon bool
		grant        []rbac.Permission
		nilEnforcer  bool
		code         connect.Code
	}{
		{name: "missing credentials", code: connect.CodeUnauthenticated},
		{name: "anonymous without players grant", anon: true, code: connect.CodePermissionDenied},
		{name: "read grant cannot moderate", anon: true, grant: []rbac.Permission{{Resource: rbac.ResourcePlayers, Action: rbac.ActionRead, ObjectID: "srv-1"}}, code: connect.CodePermissionDenied},
		{name: "moderate grant on another server", anon: true, grant: []rbac.Permission{{Resource: rbac.ResourcePlayers, Action: rbac.ActionModerate, ObjectID: "srv-2"}}, code: connect.CodePermissionDenied},
		{name: "moderate grant on owning server", anon: true, grant: []rbac.Permission{{Resource: rbac.ResourcePlayers, Action: rbac.ActionModerate, ObjectID: "srv-1"}}},
		{name: "nil enforcer denies", noAuth: true, nilEnforcer: true, code: connect.CodePermissionDenied},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			store := newTestStore(t)
			enforcer, err := rbac.NewEnforcer(store.DB())
			if err != nil {
				t.Fatal(err)
			}
			if err := enforcer.SeedDefaultPolicies(false); err != nil {
				t.Fatal(err)
			}
			if len(fixture.grant) > 0 {
				if err := enforcer.SetPermissionsForRole("anonymous", fixture.grant); err != nil {
					t.Fatal(err)
				}
			}
			manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
				Local:           config.LocalConfig{Enabled: !fixture.noAuth},
				AnonymousAccess: fixture.anon,
				AllowNoAuth:     fixture.noAuth,
			})
			if err != nil {
				t.Fatal(err)
			}
			if fixture.nilEnforcer {
				enforcer = nil
			}
			server := &Server{store: store, authManager: manager, enforcer: enforcer, log: logger.New()}
			fixtureService := &playerFixtureService{}
			mux := http.NewServeMux()
			path, handler := carbonpanelv1connect.NewPlayerServiceHandler(fixtureService, connect.WithInterceptors(server.authInterceptor()))
			mux.Handle(path, handler)
			httpServer := httptest.NewServer(mux)
			defer httpServer.Close()
			client := carbonpanelv1connect.NewPlayerServiceClient(httpServer.Client(), httpServer.URL)
			_, err = client.KickPlayer(context.Background(), connect.NewRequest(&v1.KickPlayerRequest{ServerId: "srv-1", IdentityKey: "pl_x"}))
			if fixture.code != 0 {
				if connect.CodeOf(err) != fixture.code || fixtureService.calls != 0 {
					t.Fatalf("denial=%v handler calls=%d, want code=%v calls=0", err, fixtureService.calls, fixture.code)
				}
				return
			}
			if err != nil || fixtureService.calls != 1 {
				t.Fatalf("allowed call error=%v handler calls=%d", err, fixtureService.calls)
			}
		})
	}
}

// TestPlayerElevationRequiresDistinctScope proves the op scope is not implied
// by the general moderation grant.
func TestPlayerElevationRequiresDistinctScope(t *testing.T) {
	for _, tc := range []struct {
		name       string
		action     string
		wantDenied bool
	}{
		{name: "moderate cannot op", action: rbac.ActionModerate, wantDenied: true},
		{name: "moderate:op can op", action: rbac.ActionModerateOp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			enforcer, err := rbac.NewEnforcer(store.DB())
			if err != nil {
				t.Fatal(err)
			}
			if err := enforcer.SeedDefaultPolicies(false); err != nil {
				t.Fatal(err)
			}
			allowed, err := enforcer.Enforce([]string{"anonymous"}, rbac.ResourcePlayers, tc.action, "srv-1")
			if err != nil {
				t.Fatal(err)
			}
			if !allowed {
				// SeedDefaultPolicies does not grant players; add only the tested grant.
				if err := enforcer.SetPermissionsForRole("anonymous", []rbac.Permission{{Resource: rbac.ResourcePlayers, Action: tc.action, ObjectID: "srv-1"}}); err != nil {
					t.Fatal(err)
				}
				allowed, err = enforcer.Enforce([]string{"anonymous"}, rbac.ResourcePlayers, tc.action, "srv-1")
				if err != nil {
					t.Fatal(err)
				}
				if !allowed {
					t.Fatalf("seeded %s grant did not take effect", tc.action)
				}
			}
			opAllowed, err := enforcer.Enforce([]string{"anonymous"}, rbac.ResourcePlayers, rbac.ActionModerateOp, "srv-1")
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantDenied && opAllowed {
				t.Fatal("general moderation grant unexpectedly allowed operator elevation")
			}
			if !tc.wantDenied && !opAllowed {
				t.Fatal("explicit operator grant did not allow operator elevation")
			}
		})
	}
}
