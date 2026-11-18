// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type nodeControlCall struct {
	name string
	call func(context.Context, carbonpanelv1connect.NodeControlServiceClient) error
}

// nodeControlCalls enumerates every RPC in the new contract. Each must be
// denied by the shared interceptor before reaching the (unimplemented) handler.
var nodeControlCalls = []nodeControlCall{
	{"CreateEnrollment", func(ctx context.Context, c carbonpanelv1connect.NodeControlServiceClient) error {
		_, err := c.CreateEnrollment(ctx, connect.NewRequest(&v1.CreateEnrollmentRequest{}))
		return err
	}},
	{"BeginEnrollmentChallenge", func(ctx context.Context, c carbonpanelv1connect.NodeControlServiceClient) error {
		_, err := c.BeginEnrollmentChallenge(ctx, connect.NewRequest(&v1.BeginEnrollmentChallengeRequest{}))
		return err
	}},
	{"ClaimEnrollment", func(ctx context.Context, c carbonpanelv1connect.NodeControlServiceClient) error {
		_, err := c.ClaimEnrollment(ctx, connect.NewRequest(&v1.ClaimEnrollmentRequest{}))
		return err
	}},
	{"RevokeCredential", func(ctx context.Context, c carbonpanelv1connect.NodeControlServiceClient) error {
		_, err := c.RevokeCredential(ctx, connect.NewRequest(&v1.RevokeCredentialRequest{}))
		return err
	}},
	{"NegotiateVersion", func(ctx context.Context, c carbonpanelv1connect.NodeControlServiceClient) error {
		_, err := c.NegotiateVersion(ctx, connect.NewRequest(&v1.NegotiateVersionRequest{}))
		return err
	}},
	{"OpenControlSession", func(ctx context.Context, c carbonpanelv1connect.NodeControlServiceClient) error {
		_, err := c.OpenControlSession(ctx, connect.NewRequest(&v1.OpenControlSessionRequest{}))
		return err
	}},
	{"ReportPreflight", func(ctx context.Context, c carbonpanelv1connect.NodeControlServiceClient) error {
		_, err := c.ReportPreflight(ctx, connect.NewRequest(&v1.ReportPreflightRequest{}))
		return err
	}},
	{"ReportHealth", func(ctx context.Context, c carbonpanelv1connect.NodeControlServiceClient) error {
		_, err := c.ReportHealth(ctx, connect.NewRequest(&v1.ReportHealthRequest{}))
		return err
	}},
	{"AckOperation", func(ctx context.Context, c carbonpanelv1connect.NodeControlServiceClient) error {
		_, err := c.AckOperation(ctx, connect.NewRequest(&v1.AckOperationRequest{}))
		return err
	}},
	{"DispatchOperation", func(ctx context.Context, c carbonpanelv1connect.NodeControlServiceClient) error {
		_, err := c.DispatchOperation(ctx, connect.NewRequest(&v1.DispatchOperationRequest{}))
		return err
	}},
}

// TestNodeControlRPCsDeniedByDefault proves every new node-control RPC is
// denied without an explicit permission mapping. No handler is implemented, so
// CodeUnimplemented would mean the interceptor allowed the call; that is a
// failure. The fixture uses only harmless in-memory data and fake identities.
func TestNodeControlRPCsDeniedByDefault(t *testing.T) {
	scenarios := []struct {
		name        string
		noAuth      bool
		anonymous   bool
		grantNodes  bool
		nilEnforcer bool
		code        connect.Code
	}{
		{name: "missing credentials", code: connect.CodeUnauthenticated},
		{name: "anonymous without grant", anonymous: true, code: connect.CodePermissionDenied},
		{name: "anonymous with broad nodes grant", anonymous: true, grantNodes: true, code: connect.CodePermissionDenied},
		{name: "explicit no-auth admin", noAuth: true, code: connect.CodePermissionDenied},
		{name: "missing enforcer", noAuth: true, nilEnforcer: true, code: connect.CodePermissionDenied},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			store := newTestStore(t)
			defer func() { _ = store.Close() }()

			enforcer, err := rbac.NewEnforcer(store.DB())
			if err != nil {
				t.Fatal(err)
			}
			if err := enforcer.SeedDefaultPolicies(false); err != nil {
				t.Fatal(err)
			}
			if scenario.grantNodes {
				if err := enforcer.SetPermissionsForRole("anonymous", []rbac.Permission{
					{Resource: rbac.ResourceNodes, Action: rbac.ActionRead, ObjectID: "*"},
					{Resource: rbac.ResourceNodes, Action: rbac.ActionCreate, ObjectID: "*"},
					{Resource: rbac.ResourceNodes, Action: rbac.ActionUpdate, ObjectID: "*"},
					{Resource: rbac.ResourceNodes, Action: rbac.ActionDelete, ObjectID: "*"},
				}); err != nil {
					t.Fatal(err)
				}
			}
			manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
				Local:           config.LocalConfig{Enabled: !scenario.noAuth},
				AnonymousAccess: scenario.anonymous,
				AllowNoAuth:     scenario.noAuth,
			})
			if err != nil {
				t.Fatal(err)
			}
			if scenario.nilEnforcer {
				enforcer = nil
			}
			server := &Server{store: store, authManager: manager, enforcer: enforcer, log: logger.New()}

			mux := http.NewServeMux()
			path, handler := carbonpanelv1connect.NewNodeControlServiceHandler(
				carbonpanelv1connect.UnimplementedNodeControlServiceHandler{},
				connect.WithInterceptors(server.authInterceptor()),
			)
			mux.Handle(path, handler)
			httpServer := httptest.NewServer(mux)
			defer httpServer.Close()
			client := carbonpanelv1connect.NewNodeControlServiceClient(httpServer.Client(), httpServer.URL)

			for _, call := range nodeControlCalls {
				err := call.call(context.Background(), client)
				if connect.CodeOf(err) != scenario.code {
					t.Errorf("%s: got code %v (%v), want %v", call.name, connect.CodeOf(err), err, scenario.code)
				}
				if connect.CodeOf(err) == connect.CodeUnimplemented {
					t.Errorf("%s: reached the unimplemented handler; interceptor allowed it", call.name)
				}
			}
		})
	}
}

// TestNodeControlResponseMessagesCarryNoSecrets checks the generated descriptor
// rather than trusting hand-written DTOs: no response message may declare a
// field that holds a token, key, secret or password. Requests may carry a
// one-time token; responses carry only fingerprints and generation counters.
func TestNodeControlResponseMessagesCarryNoSecrets(t *testing.T) {
	forbidden := map[string]bool{
		"token": true, "enrollment_token": true, "enrollment_secret": true,
		"tls_key": true, "private_key": true, "key": true, "secret": true,
		"password": true, "credential": true,
	}

	file := v1.File_carbonpanel_v1_node_control_proto
	messages := file.Messages()
	for i := 0; i < messages.Len(); i++ {
		message := messages.Get(i)
		if !strings.HasSuffix(string(message.Name()), "Response") {
			continue
		}
		fields := message.Fields()
		for j := 0; j < fields.Len(); j++ {
			field := fields.Get(j)
			if forbidden[string(field.Name())] {
				t.Errorf("%s.%s is a secret field in a response", message.Name(), field.Name())
			}
			checkNestedNodeControlFields(t, field.Message(), forbidden)
		}
	}
}

func checkNestedNodeControlFields(t *testing.T, message protoreflect.MessageDescriptor, forbidden map[string]bool) {
	t.Helper()
	if message == nil {
		return
	}
	fields := message.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if forbidden[string(field.Name())] {
			t.Errorf("%s.%s is a secret field nested in a response", message.Name(), field.Name())
		}
		checkNestedNodeControlFields(t, field.Message(), forbidden)
	}
}
