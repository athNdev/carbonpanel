// SPDX-License-Identifier: MPL-2.0
package rpc

import (
	"connectrpc.com/connect"
	"context"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"net/http"
	"net/http/httptest"
	"testing"
)

type scanFixtureService struct {
	carbonpanelv1connect.UnimplementedNodeServiceHandler
	calls int
}

func (s *scanFixtureService) ScanNodes(context.Context, *connect.Request[v1.ScanNodesRequest]) (*connect.Response[v1.ScanNodesResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.ScanNodesResponse{Candidates: []*v1.DockerCandidate{{Host: "fixture", LatencyMs: 42, Reachable: true}}, KnownNodes: 2}), nil
}

func TestGeneratedNodeScanAuthorizationMatchesCompatibilityScope(t *testing.T) {
	for _, fixture := range []struct {
		name                                           string
		noAuth, anonymous, global, scoped, nilEnforcer bool
		code                                           connect.Code
	}{
		{name: "missing credentials", code: connect.CodeUnauthenticated},
		{name: "missing permission", anonymous: true, code: connect.CodePermissionDenied},
		{name: "resource-scoped grant cannot scan globally", anonymous: true, scoped: true, code: connect.CodePermissionDenied},
		{name: "global read", anonymous: true, global: true},
		{name: "explicit no-auth admin", noAuth: true},
		{name: "missing enforcer", noAuth: true, nilEnforcer: true, code: connect.CodePermissionDenied},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			store := newTestStore(t)
			enforcer, err := rbac.NewEnforcer(store.DB())
			if err != nil {
				t.Fatal(err)
			}
			if err = enforcer.SeedDefaultPolicies(false); err != nil {
				t.Fatal(err)
			}
			if fixture.global || fixture.scoped {
				object := "*"
				if fixture.scoped {
					object = "node-one"
				}
				if err = enforcer.SetPermissionsForRole("anonymous", []rbac.Permission{{Resource: rbac.ResourceNodes, Action: rbac.ActionRead, ObjectID: object}}); err != nil {
					t.Fatal(err)
				}
			}
			manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{Local: config.LocalConfig{Enabled: !fixture.noAuth}, AnonymousAccess: fixture.anonymous, AllowNoAuth: fixture.noAuth})
			if err != nil {
				t.Fatal(err)
			}
			if fixture.nilEnforcer {
				enforcer = nil
			}
			server := &Server{store: store, authManager: manager, enforcer: enforcer, log: logger.New()}
			fixtureService := &scanFixtureService{}
			mux := http.NewServeMux()
			path, handler := carbonpanelv1connect.NewNodeServiceHandler(fixtureService, connect.WithInterceptors(server.authInterceptor()))
			mux.Handle(path, handler)
			httpServer := httptest.NewServer(mux)
			defer httpServer.Close()
			client := carbonpanelv1connect.NewNodeServiceClient(httpServer.Client(), httpServer.URL)
			response, err := client.ScanNodes(context.Background(), connect.NewRequest(&v1.ScanNodesRequest{}))
			if fixture.code != 0 {
				if connect.CodeOf(err) != fixture.code || fixtureService.calls != 0 {
					t.Fatalf("denial=%v calls=%d", err, fixtureService.calls)
				}
				return
			}
			if err != nil || response.Msg.KnownNodes != 2 || response.Msg.Candidates[0].LatencyMs != 42 || fixtureService.calls != 1 {
				t.Fatalf("generated response=%v error=%v", response, err)
			}
		})
	}
}
