// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/worldview"
)

// TestWorldViewProxyMountedBeforeCatchAll verifies the HTTP handler wired in
// setupHandler order wins over a frontend catch-all and fails closed for
// unauthenticated/unauthorized requests before contacting the fixture renderer.
func TestWorldViewProxyMountedBeforeCatchAll(t *testing.T) {
	srv, ts := buildHarness(t, false /* anonymousAccess */, false, nil, false)
	ctx := context.Background()
	if err := srv.store.CreateServer(ctx, &storage.Server{
		ID: "srv-proxy", Name: "proxy fixture", ModLoader: storage.ModLoaderVanilla,
		MCVersion: "1.21", Status: storage.StatusRunning, NodeID: "default", DataPath: "/fixture",
	}); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	var upstreamHits atomic.Int32
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("tile-bytes"))
	}))
	defer renderer.Close()

	parsed, err := url.Parse(renderer.URL)
	if err != nil {
		t.Fatalf("parse renderer URL: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse renderer port: %v", err)
	}
	resolver := worldview.NewResolver(worldview.Config{Endpoints: []worldview.Endpoint{{
		NodeID: "default", Kind: worldview.RendererBlueMap, Host: "127.0.0.1", Port: port,
	}}}, docker.NewLocator(nil, nil, srv.store))

	registry := worldview.NewSessionRegistry()
	key, _ := registry.Issue("srv-proxy", "fixture-world", "wv-proxy-reader", time.Minute)
	otherKey, _ := registry.Issue("srv-proxy", "fixture-world", "someone-else", time.Minute)

	mux := ts.Config.Handler.(*http.ServeMux)
	mux.Handle(worldview.ProxyPrefix, worldview.NewProxyHandler(worldview.ProxyDeps{
		Auth: srv.authManager, Authorizer: srv.enforcer, Subusers: srv.store, Servers: srv.store,
		Resolver: resolver, Sessions: registry, Log: srv.log,
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("SPA-CATCHALL"))
	})

	reader := seedWorldViewToken(t, srv, "wv-proxy-reader", "wv-proxy-role", []rbac.Permission{
		{Resource: rbac.ResourceWorldView, Action: rbac.ActionRead, ObjectID: "srv-proxy"},
	})
	denied := seedWorldViewToken(t, srv, "wv-proxy-denied", "wv-proxy-denied-role", []rbac.Permission{
		{Resource: rbac.ResourceServers, Action: rbac.ActionRead, ObjectID: "*"},
	})

	call := func(t *testing.T, token, sessionKey string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, worldview.ProxyPrefix+sessionKey+"/tiles/0.png", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		ts.Config.Handler.ServeHTTP(rec, req)
		return rec
	}

	t.Run("unauthenticated is denied before upstream", func(t *testing.T) {
		before := upstreamHits.Load()
		rec := call(t, "", key)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
		}
		if upstreamHits.Load() != before || rec.Body.String() == "SPA-CATCHALL" {
			t.Fatal("proxy did not win over the catch-all or contacted the renderer")
		}
	})

	t.Run("granted user reaches the renderer", func(t *testing.T) {
		rec := call(t, reader, key)
		if rec.Code != http.StatusOK || rec.Body.String() != "tile-bytes" {
			t.Fatalf("status/body = %d/%q", rec.Code, rec.Body.String())
		}
		if upstreamHits.Load() == 0 {
			t.Fatal("fixture renderer was not used")
		}
	})

	t.Run("ungranted user and foreign key are denied", func(t *testing.T) {
		for _, tc := range []struct {
			name, token, sessionKey string
			want                    int
		}{
			{"no world_view grant", denied, key, http.StatusForbidden},
			{"key issued to another user", reader, otherKey, http.StatusForbidden},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := upstreamHits.Load()
				rec := call(t, tc.token, tc.sessionKey)
				if rec.Code != tc.want {
					t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
				}
				if upstreamHits.Load() != before {
					t.Fatal("denied request contacted the renderer")
				}
			})
		}
	})
}
