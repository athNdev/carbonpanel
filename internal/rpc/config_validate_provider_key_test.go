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
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc/services"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

type validateKeyFixtureService struct {
	carbonpanelv1connect.UnimplementedConfigServiceHandler
	calls int
}

func (s *validateKeyFixtureService) ValidateProviderKey(context.Context, *connect.Request[v1.ValidateProviderKeyRequest]) (*connect.Response[v1.ValidateProviderKeyResponse], error) {
	s.calls++
	return connect.NewResponse(&v1.ValidateProviderKeyResponse{Valid: true, Message: "fixture"}), nil
}

// TestGeneratedValidateProviderKeyAuthorizationMatchesCompatibilityScope
// maps the new RPC to settings/read on the global object and verifies the
// negative authorization cases before it can reach the validation operation.
func TestGeneratedValidateProviderKeyAuthorizationMatchesCompatibilityScope(t *testing.T) {
	for _, fixture := range []struct {
		name                                           string
		noAuth, anonymous, global, scoped, nilEnforcer bool
		code                                           connect.Code
	}{
		{name: "missing credentials", code: connect.CodeUnauthenticated},
		{name: "missing permission", anonymous: true, code: connect.CodePermissionDenied},
		{name: "resource-scoped grant cannot validate globally", anonymous: true, scoped: true, code: connect.CodePermissionDenied},
		{name: "global settings read", anonymous: true, global: true},
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
					object = "settings-one"
				}
				if err = enforcer.SetPermissionsForRole("anonymous", []rbac.Permission{{Resource: rbac.ResourceSettings, Action: rbac.ActionRead, ObjectID: object}}); err != nil {
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
			fixtureService := &validateKeyFixtureService{}
			mux := http.NewServeMux()
			path, handler := carbonpanelv1connect.NewConfigServiceHandler(fixtureService, connect.WithInterceptors(server.authInterceptor()))
			mux.Handle(path, handler)
			httpServer := httptest.NewServer(mux)
			defer httpServer.Close()
			client := carbonpanelv1connect.NewConfigServiceClient(httpServer.Client(), httpServer.URL)
			response, err := client.ValidateProviderKey(context.Background(), connect.NewRequest(&v1.ValidateProviderKeyRequest{Provider: "unsupported-provider"}))
			if fixture.code != 0 {
				if connect.CodeOf(err) != fixture.code || fixtureService.calls != 0 {
					t.Fatalf("denial=%v calls=%d", err, fixtureService.calls)
				}
				return
			}
			if err != nil || !response.Msg.Valid || response.Msg.Message != "fixture" || fixtureService.calls != 1 {
				t.Fatalf("response=%v error=%v calls=%d", response, err, fixtureService.calls)
			}
		})
	}
}

// TestValidateProviderKeySoftResultRoundTrip uses the real service (no network
// for an unsupported provider) to pin the soft-result contract: a false Valid
// with a message is a normal response, never a connect error.
func TestValidateProviderKeySoftResultRoundTrip(t *testing.T) {
	svc := &services.ConfigService{}
	response, err := svc.ValidateProviderKey(context.Background(), connect.NewRequest(&v1.ValidateProviderKeyRequest{
		Provider: "unsupported-provider",
		ApiKey:   "secret",
	}))
	if err != nil {
		t.Fatalf("soft result returned a hard error: %v", err)
	}
	if response.Msg.Valid || response.Msg.Message != "Unsupported provider: unsupported-provider" {
		t.Fatalf("response=%v", response.Msg)
	}
}
