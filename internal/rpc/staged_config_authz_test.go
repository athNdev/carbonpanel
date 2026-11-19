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

// stagedConfigFixtureService records invocation so denial tests can prove the
// interceptor stopped the call before any application behavior ran.
type stagedConfigFixtureService struct{ calls int }

func (s *stagedConfigFixtureService) ListStagedChanges(_ context.Context, _ *connect.Request[v1.ListStagedChangesRequest]) (*connect.Response[v1.ListStagedChangesResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.ListStagedChangesResponse{Count: 1, Changes: []*v1.StagedConfigChange{{Id: "sc-1", ServerId: "srv-1"}}}), nil
}
func (s *stagedConfigFixtureService) StageConfigChange(_ context.Context, _ *connect.Request[v1.StageConfigChangeRequest]) (*connect.Response[v1.StageConfigChangeResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.StageConfigChangeResponse{Change: &v1.StagedConfigChange{Id: "sc-1", ServerId: "srv-1"}}), nil
}
func (s *stagedConfigFixtureService) ApplyStagedChange(_ context.Context, _ *connect.Request[v1.ApplyStagedChangeRequest]) (*connect.Response[v1.ApplyStagedChangeResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.ApplyStagedChangeResponse{Success: true}), nil
}
func (s *stagedConfigFixtureService) RollbackStagedChange(_ context.Context, _ *connect.Request[v1.RollbackStagedChangeRequest]) (*connect.Response[v1.RollbackStagedChangeResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.RollbackStagedChangeResponse{Success: true}), nil
}
func (s *stagedConfigFixtureService) DiscardStagedChange(_ context.Context, _ *connect.Request[v1.DiscardStagedChangeRequest]) (*connect.Response[v1.DiscardStagedChangeResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.DiscardStagedChangeResponse{Success: true}), nil
}

// TestGeneratedStagedConfigAuthorizationMatchesCompatibilityScope exercises the
// real authInterceptor with a generated client for every migrated method. The
// legacy /api/v1/staged-config adapter enforces server config read for list and
// create for mutations against the server in the request; these cases prove the
// generated contract uses the same scope and fails closed on every denial.
func TestGeneratedStagedConfigAuthorizationMatchesCompatibilityScope(t *testing.T) {
	type invocation struct {
		name   string
		action string
		call   func(carbonpanelv1connect.StagedConfigServiceClient) error
	}
	invocations := []invocation{
		{"ListStagedChanges", rbac.ActionRead, func(c carbonpanelv1connect.StagedConfigServiceClient) error {
			_, err := c.ListStagedChanges(context.Background(), connect.NewRequest(&v1.ListStagedChangesRequest{ServerId: "srv-1"}))
			return err
		}},
		{"StageConfigChange", rbac.ActionCreate, func(c carbonpanelv1connect.StagedConfigServiceClient) error {
			_, err := c.StageConfigChange(context.Background(), connect.NewRequest(&v1.StageConfigChangeRequest{ServerId: "srv-1", Changes: `{"x":1}`}))
			return err
		}},
		{"ApplyStagedChange", rbac.ActionCreate, func(c carbonpanelv1connect.StagedConfigServiceClient) error {
			_, err := c.ApplyStagedChange(context.Background(), connect.NewRequest(&v1.ApplyStagedChangeRequest{ServerId: "srv-1", StagedId: "sc-1"}))
			return err
		}},
		{"RollbackStagedChange", rbac.ActionCreate, func(c carbonpanelv1connect.StagedConfigServiceClient) error {
			_, err := c.RollbackStagedChange(context.Background(), connect.NewRequest(&v1.RollbackStagedChangeRequest{ServerId: "srv-1", StagedId: "sc-1"}))
			return err
		}},
		{"DiscardStagedChange", rbac.ActionCreate, func(c carbonpanelv1connect.StagedConfigServiceClient) error {
			_, err := c.DiscardStagedChange(context.Background(), connect.NewRequest(&v1.DiscardStagedChangeRequest{ServerId: "srv-1", StagedId: "sc-1"}))
			return err
		}},
	}
	type fixtureCase struct {
		name        string
		global      bool
		scoped      bool
		objectID    string
		anonymous   bool
		noAuth      bool
		nilEnforcer bool
		wantCode    connect.Code
	}
	fixtures := []fixtureCase{
		{name: "missing credentials", wantCode: connect.CodeUnauthenticated},
		{name: "anonymous without grant", anonymous: true, wantCode: connect.CodePermissionDenied},
		{name: "server-scoped grant for the request server", scoped: true, objectID: "srv-1", anonymous: true},
		{name: "server-scoped grant for another server", scoped: true, objectID: "srv-other", anonymous: true, wantCode: connect.CodePermissionDenied},
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
				if fixture.global {
					perms = []rbac.Permission{{Resource: rbac.ResourceServerConfig, Action: inv.action, ObjectID: "*"}}
				} else if fixture.scoped {
					object := fixture.objectID
					if object == "" {
						object = "srv-1"
					}
					perms = []rbac.Permission{{Resource: rbac.ResourceServerConfig, Action: inv.action, ObjectID: object}}
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
				fixtureService := &stagedConfigFixtureService{}
				mux := http.NewServeMux()
				path, handler := carbonpanelv1connect.NewStagedConfigServiceHandler(fixtureService, connect.WithInterceptors(srv.authInterceptor()))
				mux.Handle(path, handler)
				httpServer := httptest.NewServer(mux)
				defer httpServer.Close()
				client := carbonpanelv1connect.NewStagedConfigServiceClient(httpServer.Client(), httpServer.URL)

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
