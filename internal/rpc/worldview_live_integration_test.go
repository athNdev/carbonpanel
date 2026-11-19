//go:build integration_live

// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/bluemapdiscovery"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/worldview"
)

// TestOSS388LiveDiscoveredRendererThroughRealProxy is a packaged, opt-in
// controller integration for the ORIGINALLY REPORTED path: an installed
// builtin-bluemap module is present, its private web server is up, and World
// View used to report ABSENT. It deliberately uses the REAL discovery code,
// REAL Docker metadata from a task-owned disposable container and the REAL
// same-origin Go proxy with REAL auth/RBAC/session access checks. It does NOT
// use an explicit world_view.endpoint, a synthetic renderer or a mocked proxy.
//
// The fixture (container, volumes, network, real Java world, real rendered
// tiles) is created and disposed by the task's harness, never by this test.
// Enable only with all of the CARBON_OSS388_LIVE_* variables below; every
// normal `go test` invocation skips it.
func TestOSS388LiveDiscoveredRendererThroughRealProxy(t *testing.T) {
	relay := os.Getenv("CARBON_OSS388_LIVE_DOCKER")
	netName := os.Getenv("CARBON_OSS388_LIVE_NET")
	moduleID := os.Getenv("CARBON_OSS388_LIVE_MODULE")
	serverID := os.Getenv("CARBON_OSS388_LIVE_SERVER")
	containerID := os.Getenv("CARBON_OSS388_LIVE_CONTAINER")
	dataPath := os.Getenv("CARBON_OSS388_LIVE_DATA")
	wantIP := os.Getenv("CARBON_OSS388_LIVE_IP")
	if relay == "" || netName == "" || moduleID == "" || serverID == "" ||
		containerID == "" || dataPath == "" || wantIP == "" {
		t.Skip("live controller integration requires CARBON_OSS388_LIVE_{DOCKER,NET,MODULE,SERVER,CONTAINER,DATA,IP}")
	}

	ctx := context.Background()
	srv, ts := buildHarness(t, false, false, nil, false)
	defer ts.Close()

	level := "world"
	if err := srv.store.CreateServer(ctx, &storage.Server{
		ID: serverID, Name: "OSS388 live integration", ModLoader: storage.ModLoaderVanilla,
		MCVersion: "1.20.4", Status: storage.StatusRunning, NodeID: "default",
		DataPath: dataPath,
	}); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	if err := srv.store.SaveServerConfig(ctx, &storage.ServerConfig{
		ID: serverID, ServerID: serverID, Level: &level,
	}); err != nil {
		t.Fatalf("SaveServerConfig: %v", err)
	}
	if err := srv.store.CreateModule(ctx, &storage.Module{
		ID: moduleID, Name: "OSS388 Integration BlueMap", ServerID: serverID,
		NodeID: "default", TemplateID: worldview.BuiltinBlueMapTemplateID,
		ContainerID: containerID, Status: storage.ModuleStatusRunning,
		Metadata: map[string]string{"bluemap_version": "5.29"},
	}); err != nil {
		t.Fatalf("CreateModule: %v", err)
	}

	cfg := docker.ClientConfig{NetworkName: netName}
	cli, err := docker.NewClient(relay, srv.log, cfg)
	if err != nil {
		t.Fatalf("NewClient(relay): %v", err)
	}
	pool := docker.NewClientPool(srv.store, cli, srv.log, cfg)
	defer func() { _ = pool.Close() }()

	// No explicit endpoint: discovery must locate the installed module from
	// panel state + inspected container metadata alone.
	discovery := bluemapdiscovery.New(srv.store, pool, nodestorage.NewResolver(nil), netName, srv.log)
	facts, err := discovery.FindRenderer(ctx, "default", serverID)
	if err != nil || facts == nil {
		t.Fatalf("FindRenderer: facts=%+v err=%v", facts, err)
	}
	t.Logf("OSS388 live discovery facts: %+v", facts)
	for name, ok := range map[string]bool{
		"identity": facts.IdentityVerified, "network": facts.NetworkVerified,
		"mount": facts.MountVerified, "readonly": facts.ReadOnlyWorldMount,
		"web": facts.WebUp, "rendered": facts.Rendered,
		"accept-download": facts.AcceptDownload,
		"world-matches-server": facts.WorldMatchesServer,
	} {
		if !ok {
			t.Errorf("discovery fact %q is false; facts = %+v", name, facts)
		}
	}
	if facts.MapID != "world" || facts.WorldName != "world" {
		t.Errorf("target identity = map %q / world %q, want world/world", facts.MapID, facts.WorldName)
	}
	if facts.Lifecycle != worldview.ModuleRunning {
		t.Errorf("lifecycle = %v, want running", facts.Lifecycle)
	}
	if facts.BaseURL != "http://"+wantIP+":8100" {
		t.Errorf("private BaseURL = %q, want http://%s:8100 (inspected owned-network IP, no DNS/host port)", facts.BaseURL, wantIP)
	}

	resolver := worldview.NewResolverWithDiscovery(worldview.Config{}, docker.NewLocator(pool, nil, srv.store), discovery)
	sessions := worldview.NewSessionRegistry()
	// A real issued key bound to the granted user and server. The browser never
	// sees the renderer origin; the map key travels only in the URL path.
	key, expires := sessions.Issue(serverID, "w1.live", "oss388-live-reader", time.Minute)
	if expires.Before(time.Now()) {
		t.Fatal("session already expired")
	}

	mux := ts.Config.Handler.(*http.ServeMux)
	mux.Handle(worldview.ProxyPrefix, worldview.NewProxyHandler(worldview.ProxyDeps{
		Auth: srv.authManager, Authorizer: srv.enforcer, Subusers: srv.store,
		Servers: srv.store, Resolver: resolver, Sessions: sessions, Log: srv.log,
	}))

	granted := seedWorldViewToken(t, srv, "oss388-live-reader", "oss388-live-role", []rbac.Permission{
		{Resource: rbac.ResourceWorldView, Action: rbac.ActionRead, ObjectID: serverID},
	})
	wrongScope := seedWorldViewToken(t, srv, "oss388-live-wrong", "oss388-live-wrong-role", []rbac.Permission{
		{Resource: rbac.ResourceWorldView, Action: rbac.ActionRead, ObjectID: "srv-other"},
	})

	call := func(token, asset string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, ts.URL+worldview.ProxyPrefix+key+"/"+asset, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("proxy request: %v", err)
		}
		return resp
	}

	// REAL /settings.json through the REAL proxy under REAL access checks.
	resp := call(granted, "settings.json")
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"maps":["world"]`) {
		t.Fatalf("settings.json status/body = %d/%q", resp.StatusCode, string(body))
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "private, no-store, no-cache, must-revalidate, max-age=0" {
		t.Errorf("Cache-Control = %q, want panel policy", cc)
	}
	if strings.Contains(string(body), wantIP) || strings.Contains(resp.Header.Get("Location"), wantIP) {
		t.Error("private renderer address leaked to the browser")
	}
	t.Logf("REAL /settings.json through Go proxy: %s", string(body))

	// REAL rendered tile through the same proxy.
	tileResp := call(granted, "maps/world/tiles/3/x0/z0.png")
	tile, err := io.ReadAll(tileResp.Body)
	_ = tileResp.Body.Close()
	if err != nil {
		t.Fatalf("read tile: %v", err)
	}
	if tileResp.StatusCode != http.StatusOK || !bytes.HasPrefix(tile, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) {
		t.Fatalf("tile status = %d, magic = %x, len = %d", tileResp.StatusCode, tile[:min(8, len(tile))], len(tile))
	}
	t.Logf("REAL PNG tile through Go proxy: %d bytes, PNG magic OK", len(tile))

	// Fail-closed: wrong scope and no token are denied before any upstream hit.
	for _, tc := range []struct {
		name, token string
		want        int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"wrong server scope", wrongScope, http.StatusForbidden},
	} {
		denied := call(tc.token, "maps/world/tiles/3/x0/z0.png")
		_ = denied.Body.Close()
		if denied.StatusCode != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, denied.StatusCode, tc.want)
		}
	}
}
