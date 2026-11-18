// SPDX-License-Identifier: MPL-2.0
package rpc

import (
	"connectrpc.com/connect"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

// modBrowseFixtureService records invocation so denial tests can prove the
// interceptor stopped the call before any application behavior ran.
type modBrowseFixtureService struct {
	searchCalls, versionsCalls, installCalls int
}

func (s *modBrowseFixtureService) SearchMods(_ context.Context, _ *connect.Request[v1.SearchModsRequest]) (*connect.Response[v1.SearchModsResponse], error) {
	s.searchCalls++
	return connect.NewResponse(&v1.SearchModsResponse{
		Results: []*v1.BrowseModResult{{Id: "mr-1", Slug: "sodium", Platform: "modrinth"}},
		Count:   1,
	}), nil
}

func (s *modBrowseFixtureService) GetModVersions(_ context.Context, _ *connect.Request[v1.GetModVersionsRequest]) (*connect.Response[v1.GetModVersionsResponse], error) {
	s.versionsCalls++
	return connect.NewResponse(&v1.GetModVersionsResponse{
		Versions: []*v1.ModVersion{{Id: "v1", FileName: "sodium.jar", DownloadUrl: "https://fixture/sodium.jar"}},
		Count:    1,
	}), nil
}

func (s *modBrowseFixtureService) InstallMods(_ context.Context, _ *connect.Request[v1.InstallModsRequest]) (*connect.Response[v1.InstallModsResponse], error) {
	s.installCalls++
	return connect.NewResponse(&v1.InstallModsResponse{Success: true, Installed: []string{"sodium.jar"}}), nil
}

// TestGeneratedModBrowseAuthorizationMatchesCompatibilityScope exercises the
// real authInterceptor with a generated client for all three migrated methods.
// The legacy /mods/* adapter enforces mods/read for browse and version lookup
// and mods/create for install against the global "*" object; these cases prove
// the generated contract uses the same scope and fails closed on every denial.
func TestGeneratedModBrowseAuthorizationMatchesCompatibilityScope(t *testing.T) {
	type invocation struct {
		name   string
		action string
		call   func(carbonpanelv1connect.ModBrowseServiceClient) (any, error)
		verify func(*testing.T, any)
		calls  func(*modBrowseFixtureService) int
	}

	invocations := []invocation{
		{
			name:   "SearchMods",
			action: rbac.ActionRead,
			call: func(c carbonpanelv1connect.ModBrowseServiceClient) (any, error) {
				return c.SearchMods(context.Background(), connect.NewRequest(&v1.SearchModsRequest{ServerId: "srv-1", Query: "sodium"}))
			},
			verify: func(t *testing.T, value any) {
				resp := value.(*connect.Response[v1.SearchModsResponse])
				if resp.Msg.Count != 1 || len(resp.Msg.Results) != 1 || resp.Msg.Results[0].Slug != "sodium" {
					t.Fatalf("unexpected SearchMods response: %v", resp.Msg)
				}
			},
			calls: func(s *modBrowseFixtureService) int { return s.searchCalls },
		},
		{
			name:   "GetModVersions",
			action: rbac.ActionRead,
			call: func(c carbonpanelv1connect.ModBrowseServiceClient) (any, error) {
				return c.GetModVersions(context.Background(), connect.NewRequest(&v1.GetModVersionsRequest{ServerId: "srv-1", Project: "sodium"}))
			},
			verify: func(t *testing.T, value any) {
				resp := value.(*connect.Response[v1.GetModVersionsResponse])
				if resp.Msg.Count != 1 || len(resp.Msg.Versions) != 1 || resp.Msg.Versions[0].FileName != "sodium.jar" {
					t.Fatalf("unexpected GetModVersions response: %v", resp.Msg)
				}
			},
			calls: func(s *modBrowseFixtureService) int { return s.versionsCalls },
		},
		{
			name:   "InstallMods",
			action: rbac.ActionCreate,
			call: func(c carbonpanelv1connect.ModBrowseServiceClient) (any, error) {
				return c.InstallMods(context.Background(), connect.NewRequest(&v1.InstallModsRequest{
					ServerId: "srv-1",
					Items:    []*v1.InstallModEntry{{Name: "Sodium", FileName: "sodium.jar", DownloadUrl: "https://fixture/sodium.jar"}},
				}))
			},
			verify: func(t *testing.T, value any) {
				resp := value.(*connect.Response[v1.InstallModsResponse])
				if !resp.Msg.Success || len(resp.Msg.Installed) != 1 || resp.Msg.Installed[0] != "sodium.jar" {
					t.Fatalf("unexpected InstallMods response: %v", resp.Msg)
				}
			},
			calls: func(s *modBrowseFixtureService) int { return s.installCalls },
		},
	}

	for _, inv := range invocations {
		for _, fixture := range []struct {
			name                                           string
			noAuth, anonymous, global, scoped, nilEnforcer bool
			code                                           connect.Code
		}{
			{name: "missing credentials", code: connect.CodeUnauthenticated},
			{name: "anonymous without permission", anonymous: true, code: connect.CodePermissionDenied},
			{name: "resource-scoped grant cannot browse globally", anonymous: true, scoped: true, code: connect.CodePermissionDenied},
			{name: "global grant", anonymous: true, global: true},
			{name: "explicit no-auth admin", noAuth: true},
			{name: "missing enforcer", noAuth: true, nilEnforcer: true, code: connect.CodePermissionDenied},
		} {
			t.Run(inv.name+"/"+fixture.name, func(t *testing.T) {
				store := newTestStore(t)
				enforcer, err := rbac.NewEnforcer(store.DB())
				if err != nil {
					t.Fatal(err)
				}
				if err = enforcer.SeedDefaultPolicies(false); err != nil {
					t.Fatal(err)
				}
				// Replace the anonymous role's defaults so read methods are not
				// accidentally authorized by the seeded mods/read grant.
				object := "*"
				perms := []rbac.Permission{}
				if fixture.global || fixture.scoped {
					if fixture.scoped {
						object = "srv-1"
					}
					perms = []rbac.Permission{{Resource: rbac.ResourceMods, Action: inv.action, ObjectID: object}}
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
				fixtureService := &modBrowseFixtureService{}
				mux := http.NewServeMux()
				path, handler := carbonpanelv1connect.NewModBrowseServiceHandler(fixtureService, connect.WithInterceptors(srv.authInterceptor()))
				mux.Handle(path, handler)
				httpServer := httptest.NewServer(mux)
				defer httpServer.Close()
				client := carbonpanelv1connect.NewModBrowseServiceClient(httpServer.Client(), httpServer.URL)

				value, err := inv.call(client)
				if fixture.code != 0 {
					if connect.CodeOf(err) != fixture.code || inv.calls(fixtureService) != 0 {
						t.Fatalf("denial=%v calls=%d want code=%v", err, inv.calls(fixtureService), fixture.code)
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected denial: %v", err)
				}
				inv.verify(t, value)
				if inv.calls(fixtureService) != 1 {
					t.Fatalf("service calls=%d want 1", inv.calls(fixtureService))
				}
			})
		}
	}
}
