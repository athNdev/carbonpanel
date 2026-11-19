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

// packwizFixtureService records invocation so denial tests can prove the
// interceptor stopped the call before any pack operation ran.
type packwizFixtureService struct{ calls int }

func (s *packwizFixtureService) ListPacks(_ context.Context, _ *connect.Request[v1.ListPacksRequest]) (*connect.Response[v1.ListPacksResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.ListPacksResponse{Packs: []*v1.PackSummary{{Id: "p1"}}}), nil
}
func (s *packwizFixtureService) GetPack(_ context.Context, _ *connect.Request[v1.GetPackRequest]) (*connect.Response[v1.GetPackResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.GetPackResponse{Pack: &v1.Pack{Id: "p1"}}), nil
}
func (s *packwizFixtureService) ClonePack(_ context.Context, _ *connect.Request[v1.ClonePackRequest]) (*connect.Response[v1.ClonePackResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.ClonePackResponse{Pack: &v1.Pack{Id: "p2"}}), nil
}
func (s *packwizFixtureService) RefreshPack(_ context.Context, _ *connect.Request[v1.RefreshPackRequest]) (*connect.Response[v1.RefreshPackResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.RefreshPackResponse{Success: true}), nil
}
func (s *packwizFixtureService) CheckUpdates(_ context.Context, _ *connect.Request[v1.CheckUpdatesRequest]) (*connect.Response[v1.CheckUpdatesResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.CheckUpdatesResponse{}), nil
}
func (s *packwizFixtureService) DeletePack(_ context.Context, _ *connect.Request[v1.DeletePackRequest]) (*connect.Response[v1.DeletePackResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.DeletePackResponse{Success: true}), nil
}
func (s *packwizFixtureService) AddPackMod(_ context.Context, _ *connect.Request[v1.AddPackModRequest]) (*connect.Response[v1.AddPackModResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.AddPackModResponse{Success: true}), nil
}
func (s *packwizFixtureService) AddUrlMod(_ context.Context, _ *connect.Request[v1.AddUrlModRequest]) (*connect.Response[v1.AddUrlModResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.AddUrlModResponse{Mod: &v1.PackMod{Slug: "url-mod"}}), nil
}
func (s *packwizFixtureService) UpdatePackMod(_ context.Context, _ *connect.Request[v1.UpdatePackModRequest]) (*connect.Response[v1.UpdatePackModResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.UpdatePackModResponse{Success: true}), nil
}
func (s *packwizFixtureService) DeletePackMod(_ context.Context, _ *connect.Request[v1.DeletePackModRequest]) (*connect.Response[v1.DeletePackModResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.DeletePackModResponse{Success: true}), nil
}
func (s *packwizFixtureService) BatchPackMods(_ context.Context, _ *connect.Request[v1.BatchPackModsRequest]) (*connect.Response[v1.BatchPackModsResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.BatchPackModsResponse{Success: true, Count: 1}), nil
}

// TestGeneratedPackwizAuthorizationMatchesCompatibilityScope exercises the real
// authInterceptor with a generated client for every migrated method. The legacy
// adapter checks modpacks/read for GET and modpacks/create otherwise, against
// the global "*" object; these cases prove the same scope and fail-closed
// behavior on every denial.
func TestGeneratedPackwizAuthorizationMatchesCompatibilityScope(t *testing.T) {
	type invocation struct {
		name   string
		action string
		call   func(carbonpanelv1connect.PackwizServiceClient) error
	}
	invocations := []invocation{
		{"ListPacks", rbac.ActionRead, func(c carbonpanelv1connect.PackwizServiceClient) error { _, err := c.ListPacks(context.Background(), connect.NewRequest(&v1.ListPacksRequest{})); return err }},
		{"GetPack", rbac.ActionRead, func(c carbonpanelv1connect.PackwizServiceClient) error { _, err := c.GetPack(context.Background(), connect.NewRequest(&v1.GetPackRequest{PackId: "p1"})); return err }},
		{"CheckUpdates", rbac.ActionRead, func(c carbonpanelv1connect.PackwizServiceClient) error { _, err := c.CheckUpdates(context.Background(), connect.NewRequest(&v1.CheckUpdatesRequest{PackId: "p1"})); return err }},
		{"ClonePack", rbac.ActionCreate, func(c carbonpanelv1connect.PackwizServiceClient) error { _, err := c.ClonePack(context.Background(), connect.NewRequest(&v1.ClonePackRequest{PackId: "p1"})); return err }},
		{"RefreshPack", rbac.ActionCreate, func(c carbonpanelv1connect.PackwizServiceClient) error { _, err := c.RefreshPack(context.Background(), connect.NewRequest(&v1.RefreshPackRequest{PackId: "p1"})); return err }},
		{"DeletePack", rbac.ActionCreate, func(c carbonpanelv1connect.PackwizServiceClient) error { _, err := c.DeletePack(context.Background(), connect.NewRequest(&v1.DeletePackRequest{PackId: "p1"})); return err }},
		{"AddPackMod", rbac.ActionCreate, func(c carbonpanelv1connect.PackwizServiceClient) error { _, err := c.AddPackMod(context.Background(), connect.NewRequest(&v1.AddPackModRequest{PackId: "p1", Mod: &v1.PackMod{Slug: "m"}})); return err }},
		{"AddUrlMod", rbac.ActionCreate, func(c carbonpanelv1connect.PackwizServiceClient) error { _, err := c.AddUrlMod(context.Background(), connect.NewRequest(&v1.AddUrlModRequest{PackId: "p1"})); return err }},
		{"UpdatePackMod", rbac.ActionCreate, func(c carbonpanelv1connect.PackwizServiceClient) error { _, err := c.UpdatePackMod(context.Background(), connect.NewRequest(&v1.UpdatePackModRequest{PackId: "p1", Slug: "m"})); return err }},
		{"DeletePackMod", rbac.ActionCreate, func(c carbonpanelv1connect.PackwizServiceClient) error { _, err := c.DeletePackMod(context.Background(), connect.NewRequest(&v1.DeletePackModRequest{PackId: "p1", Slug: "m"})); return err }},
		{"BatchPackMods", rbac.ActionCreate, func(c carbonpanelv1connect.PackwizServiceClient) error { _, err := c.BatchPackMods(context.Background(), connect.NewRequest(&v1.BatchPackModsRequest{PackId: "p1", Action: "pin", Slugs: []string{"m"}})); return err }},
	}
	type fixtureCase struct {
		name        string
		global      bool
		scoped      bool
		anonymous   bool
		noAuth      bool
		nilEnforcer bool
		wantCode    connect.Code
	}
	fixtures := []fixtureCase{
		{name: "missing credentials", wantCode: connect.CodeUnauthenticated},
		{name: "anonymous without grant", anonymous: true, wantCode: connect.CodePermissionDenied},
		{name: "server-scoped grant only", scoped: true, anonymous: true, wantCode: connect.CodePermissionDenied},
		{name: "nil enforcer", anonymous: true, nilEnforcer: true, wantCode: connect.CodePermissionDenied},
		{name: "global grant", global: true, anonymous: true},
		{name: "explicit no-auth admin", noAuth: true},
	}
	for _, inv := range invocations {
		for _, fixture := range fixtures {
			t.Run(inv.name+"/"+fixture.name, func(t *testing.T) {
				store := newTestStore(t)
				enforcer, err := rbac.NewEnforcer(store.DB())
				if err != nil {
					t.Fatal(err)
				}
				if err = enforcer.SeedDefaultPolicies(false); err != nil {
					t.Fatal(err)
				}
				perms := []rbac.Permission{}
				if fixture.global || fixture.scoped {
					object := "*"
					if fixture.scoped {
						object = "srv-1"
					}
					perms = []rbac.Permission{{Resource: rbac.ResourceModpacks, Action: inv.action, ObjectID: object}}
				}
				if err = enforcer.SetPermissionsForRole("anonymous", perms); err != nil {
					t.Fatal(err)
				}
				manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
					Local:           config.LocalConfig{Enabled: !fixture.noAuth},
					AnonymousAccess: fixture.anonymous,
					AllowNoAuth:     fixture.noAuth,
				})
				if err != nil {
					t.Fatal(err)
				}
				if fixture.nilEnforcer {
					enforcer = nil
				}
				srv := &Server{store: store, authManager: manager, enforcer: enforcer, log: logger.New()}
				fixtureService := &packwizFixtureService{}
				mux := http.NewServeMux()
				path, handler := carbonpanelv1connect.NewPackwizServiceHandler(fixtureService, connect.WithInterceptors(srv.authInterceptor()))
				mux.Handle(path, handler)
				httpServer := httptest.NewServer(mux)
				defer httpServer.Close()
				client := carbonpanelv1connect.NewPackwizServiceClient(httpServer.Client(), httpServer.URL)

				err = inv.call(client)
				if fixture.wantCode != 0 {
					if connect.CodeOf(err) != fixture.wantCode || fixtureService.calls != 0 {
						t.Fatalf("denial=%v calls=%d want code=%v", err, fixtureService.calls, fixture.wantCode)
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected denial: %v", err)
				}
				if fixtureService.calls != 1 {
					t.Fatalf("service calls=%d want 1", fixtureService.calls)
				}
			})
		}
	}
}
