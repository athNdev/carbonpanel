// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc/services"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

// TestNodeReadResponseShapeForNodesReader exercises the real generated
// NodeService through the auth interceptor with a principal holding only the
// global nodes/read grant. This is the negative fixture for the confirmed
// dbNodeToProto leak: no TLS secret may reach the caller in GetNode/ListNodes or
// in the raw HTTP response body. Fake sentinels in documentation address space
// only; no real credential is used.
func TestNodeReadResponseShapeForNodesReader(t *testing.T) {
	store := newTestStore(t)
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	const (
		caSentinel   = "FAKE-CA-SENTINEL-DO-NOT-USE"
		certSentinel = "FAKE-CERT-SENTINEL-DO-NOT-USE"
		keySentinel  = "FAKE-KEY-SENTINEL-DO-NOT-USE"
	)

	node := &storage.Node{
		Name:         "tls-node-reader-fixture",
		Host:         "tcp://192.0.2.20:2376",
		AdvertisedIP: "192.0.2.20",
		TLSCACert:    caSentinel,
		TLSCert:      certSentinel,
		TLSKey:       keySentinel,
		TLSEnabled:   true,
		Status:       storage.NodeStatusOffline,
		Enabled:      false,
	}
	if err := store.CreateNode(ctx, node); err != nil {
		t.Fatalf("CreateNode fixture failed: %v", err)
	}

	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatal(err)
	}
	if err := enforcer.SetPermissionsForRole("anonymous", []rbac.Permission{
		{Resource: rbac.ResourceNodes, Action: rbac.ActionRead, ObjectID: "*"},
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
		Local:           config.LocalConfig{Enabled: true},
		AnonymousAccess: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	nodeService := services.NewNodeService(store, nil, logger.New())
	server := &Server{store: store, authManager: manager, enforcer: enforcer, log: logger.New()}
	mux := http.NewServeMux()
	path, handler := carbonpanelv1connect.NewNodeServiceHandler(nodeService, connect.WithInterceptors(server.authInterceptor()))
	mux.Handle(path, handler)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	client := carbonpanelv1connect.NewNodeServiceClient(httpServer.Client(), httpServer.URL)
	getRes, err := client.GetNode(ctx, connect.NewRequest(&v1.GetNodeRequest{Id: node.ID}))
	if err != nil {
		t.Fatalf("GetNode failed for nodes/read principal: %v", err)
	}
	assertNoNodeSecrets(t, "GetNode", getRes.Msg.Node, caSentinel, certSentinel, keySentinel)

	listRes, err := client.ListNodes(ctx, connect.NewRequest(&v1.ListNodesRequest{}))
	if err != nil {
		t.Fatalf("ListNodes failed for nodes/read principal: %v", err)
	}
	if len(listRes.Msg.Nodes) != 1 {
		t.Fatalf("expected one node, got %d", len(listRes.Msg.Nodes))
	}
	assertNoNodeSecrets(t, "ListNodes", listRes.Msg.Nodes[0], caSentinel, certSentinel, keySentinel)

	// Raw HTTP body check: the JSON the browser actually receives must not
	// contain any of the secret sentinels.
	for _, tc := range []struct{ label, path, body string }{
		{label: "GetNode wire", path: "/carbonpanel.v1.NodeService/GetNode", body: `{"id":"` + node.ID + `"}`},
		{label: "ListNodes wire", path: "/carbonpanel.v1.NodeService/ListNodes", body: `{}`},
	} {
		req, err := http.NewRequest(http.MethodPost, httpServer.URL+tc.path, strings.NewReader(tc.body))
		if err != nil {
			t.Fatalf("%s: request build failed: %v", tc.label, err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpServer.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: request failed: %v", tc.label, err)
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("%s: body read failed: %v", tc.label, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: unexpected status %d: %s", tc.label, resp.StatusCode, raw)
		}
		for _, sentinel := range []string{caSentinel, certSentinel, keySentinel} {
			if strings.Contains(string(raw), sentinel) {
				t.Errorf("%s: TLS secret sentinel appeared in the wire body", tc.label)
			}
		}
		if !strings.Contains(string(raw), node.ID) {
			t.Errorf("%s: non-secret node identity missing from wire body", tc.label)
		}
	}
}

func assertNoNodeSecrets(t *testing.T, label string, node *v1.Node, sentinels ...string) {
	t.Helper()
	if node == nil {
		t.Fatalf("%s: expected a node", label)
	}
	if node.TlsKey != "" || node.TlsCaCert != "" || node.TlsCert != "" {
		t.Errorf("%s: TLS secret fields were not empty: %+v", label, node)
	}
	if node.Id == "" || node.Name == "" || node.Host == "" {
		t.Errorf("%s: non-secret node fields were lost: %+v", label, node)
	}
	for _, sentinel := range sentinels {
		if strings.Contains(fmt.Sprint(node), sentinel) {
			t.Errorf("%s: TLS secret sentinel appeared in the response", label)
		}
	}
}
