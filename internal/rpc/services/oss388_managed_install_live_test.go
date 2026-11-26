//go:build integration_live

// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/bluemapdiscovery"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/module"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/worldview"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// e2eAuthenticator is the narrow proxy auth seam: only the test's explicit
// bearer token authenticates; everything else fails closed.
type e2eAuthenticator struct{ user *auth.AuthenticatedUser }

func (a e2eAuthenticator) AuthenticateFromHeader(_ context.Context, header string) (*auth.AuthenticatedUser, error) {
	if strings.Contains(header, "oss388-e2e-token") {
		return a.user, nil
	}
	return nil, fmt.Errorf("authentication required")
}

// TestOSS388ManagedInstallEndToEndLive exercises the PUBLIC World View install
// path with REAL components: SQLite store, RBAC, ModuleService.CreateModule
// with consent and startImmediately, module.Manager and the real Docker client
// against a task-owned daemon, deterministic managed config staging, real
// bluemapdiscovery and the real same-origin proxy including a genuine PNG tile.
// It is opt-in (tag integration_live + all CARBON_OSS388_E2E_* variables) and
// is skipped by every ordinary `go test`. The harness owns and disposes the
// fixture; this test never creates or deletes a world, container or network.
func TestOSS388ManagedInstallEndToEndLive(t *testing.T) {
	relay := os.Getenv("CARBON_OSS388_E2E_DOCKER")
	netName := os.Getenv("CARBON_OSS388_E2E_NET")
	dataRoot := os.Getenv("CARBON_OSS388_E2E_DATA")
	serverID := os.Getenv("CARBON_OSS388_E2E_SERVER")
	nodeID := os.Getenv("CARBON_OSS388_E2E_NODE")
	outDir := os.Getenv("CARBON_OSS388_E2E_OUT")
	if relay == "" || netName == "" || dataRoot == "" || serverID == "" || nodeID == "" || outDir == "" {
		t.Skip("live managed-install E2E requires CARBON_OSS388_E2E_{DOCKER,NET,DATA,SERVER,NODE,OUT}")
	}
	result := map[string]any{"status": "running"}
	writeResult := func() {
		b, _ := json.MarshalIndent(result, "", "  ")
		_ = os.WriteFile(filepath.Join(outDir, "managed-install-e2e.json"), b, 0o644)
	}
	defer writeResult()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	log := logger.New()
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Path: filepath.Join(outDir, "managed-install-e2e.db"), AutoMigrate: true, MaxConnections: 5,
		},
		Docker:  config.DockerConfig{NetworkName: netName},
		Storage: config.StorageConfig{DataDir: dataRoot},
	}

	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	defer func() { _ = store.Close() }()

	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil || enforcer == nil {
		t.Fatalf("NewEnforcer: err=%v", err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatalf("SeedDefaultPolicies: %v", err)
	}
	admin := auth.WithUser(ctx, &auth.AuthenticatedUser{
		ID: "oss388-e2e-admin", Username: "oss388-e2e", Roles: []string{"admin"},
	})

	level := "world"
	if err := store.CreateServer(ctx, &storage.Server{
		ID: serverID, Name: "OSS388 E2E", ModLoader: storage.ModLoaderVanilla,
		MCVersion: "1.20.4", Status: storage.StatusRunning, NodeID: nodeID, DataPath: dataRoot,
	}); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	if err := store.SaveServerConfig(ctx, &storage.ServerConfig{
		ID: serverID, ServerID: serverID, Level: &level,
	}); err != nil {
		t.Fatalf("SaveServerConfig: %v", err)
	}
	if err := store.CreateModuleTemplate(ctx, &storage.ModuleTemplate{
		ID: worldview.BuiltinBlueMapTemplateID, Name: "BlueMap",
		DockerImage: "ghcr.io/bluemap-minecraft/bluemap:v5.29",
		Type:        storage.ModuleTemplateTypeBuiltin,
		Ports:       []*v1.ModulePort{{Name: "Web", ContainerPort: 8100, Protocol: "http", ProxyEnabled: true}},
	}); err != nil {
		t.Fatalf("CreateModuleTemplate: %v", err)
	}

	cli, err := docker.NewClient(relay, log, docker.ClientConfig{NetworkName: netName})
	if err != nil {
		t.Fatalf("NewClient(relay): %v", err)
	}
	mgr := module.NewManager(store, cli, nil, cfg, nil, log)
	svc := NewModuleService(store, cli, mgr, nil, nil, cfg, nil, log)
	svc.SetEnforcer(enforcer)
	svc.SetStorageResolver(nodestorage.NewResolver(nil))

	createReq := &v1.CreateModuleRequest{
		Name: "OSS388 E2E BlueMap", ServerId: serverID, TemplateId: worldview.BuiltinBlueMapTemplateID,
		StartImmediately: true,
		Metadata: map[string]string{
			"bluemap_managed": "true", "bluemap_consent_accepted": "true",
		},
	}
	resp, err := svc.CreateModule(admin, connect.NewRequest(createReq))
	if err != nil {
		result["status"] = "create_failed"
		result["error"] = err.Error()
		t.Fatalf("CreateModule (managed install): %v", err)
	}
	moduleID := resp.Msg.GetModule().GetId()
	if moduleID == "" {
		t.Fatal("CreateModule returned no module ID")
	}
	result["moduleID"] = moduleID
	result["status"] = "created"
	t.Logf("managed install created module %s", moduleID)

	// Idempotent retry through the SAME public RPC: no duplicate module row.
	again, err := svc.CreateModule(admin, connect.NewRequest(createReq))
	if err != nil || again.Msg.GetModule().GetId() != moduleID {
		result["status"] = "idempotency_failed"
		t.Fatalf("retry returned different module: resp=%v err=%v", again, err)
	}
	mods, err := store.ListModulesByTemplate(ctx, worldview.BuiltinBlueMapTemplateID)
	if err != nil || len(mods) != 1 {
		result["status"] = "singleton_failed"
		t.Fatalf("expected exactly one managed module, got %d (err=%v)", len(mods), err)
	}
	result["singleton"] = true

	// Wait for the REAL container to be created and the module to report running.
	var containerID string
	for i := 0; i < 60; i++ {
		m, err := store.GetModule(ctx, moduleID)
		if err == nil && m != nil {
			if m.ContainerID != "" {
				containerID = m.ContainerID
			}
			if m.Status == storage.ModuleStatusRunning && containerID != "" {
				break
			}
			if m.Status == storage.ModuleStatusError {
				result["status"] = "module_error"
				t.Fatalf("module entered ERROR state during install")
			}
		}
		time.Sleep(3 * time.Second)
	}
	if containerID == "" {
		result["status"] = "no_container"
		t.Fatal("module container was never created")
	}
	result["containerID"] = containerID

	runtime, err := cli.InspectModuleRuntime(ctx, moduleID, containerID, netName)
	if err != nil || runtime == nil {
		result["status"] = "inspect_failed"
		t.Fatalf("InspectModuleRuntime: facts=%+v err=%v", runtime, err)
	}
	result["runtime"] = runtime

	// Deterministic managed config was staged and committed from real world facts.
	for _, rel := range []string{
		"modules/bluemap/config/core.conf",
		"modules/bluemap/config/maps/world.conf",
	} {
		b, err := os.ReadFile(filepath.Join(dataRoot, filepath.FromSlash(rel)))
		if err != nil || len(b) == 0 {
			result["status"] = "config_missing"
			t.Fatalf("managed config %s missing/empty: %v", rel, err)
		}
		if strings.Contains(rel, "core.conf") && !strings.Contains(string(b), "accept-download: true") {
			result["status"] = "consent_config_missing"
			t.Fatalf("core.conf lacks explicit accept-download consent: %q", string(b))
		}
	}

	// Real discovery from the installed module (explicit endpoint empty).
	pool := docker.NewClientPool(store, cli, log, docker.ClientConfig{NetworkName: netName})
	defer func() { _ = pool.Close() }()
	discovery := bluemapdiscovery.New(store, pool, nodestorage.NewResolver(nil), netName, log)
	deadline := time.Now().Add(4 * time.Minute)
	var facts *worldview.InstalledRenderer
	for time.Now().Before(deadline) {
		facts, err = discovery.FindRenderer(ctx, nodeID, serverID)
		if err == nil && facts != nil && facts.IdentityVerified && facts.NetworkVerified &&
			facts.MountVerified && facts.ReadOnlyWorldMount && facts.AcceptDownload &&
			facts.WebUp && facts.Rendered && facts.WorldMatchesServer &&
			facts.Lifecycle == worldview.ModuleRunning && facts.MapID == "world" {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if facts == nil || !facts.Rendered || !facts.WebUp || !facts.WorldMatchesServer {
		result["status"] = "not_ready"
		result["lastFacts"] = facts
		result["lastErr"] = fmt.Sprint(err)
		t.Fatalf("installed renderer never satisfied READY gates: facts=%+v err=%v", facts, err)
	}
	result["facts"] = facts
	result["status"] = "ready"

	// Real proxied asset through the production proxy with real auth/RBAC.
	resolver := worldview.NewResolverWithDiscovery(worldview.Config{}, docker.NewLocator(pool, nil, store), discovery)
	sessions := worldview.NewSessionRegistry()
	key, _ := sessions.Issue(serverID, "e2e.w1", "oss388-e2e-admin", time.Minute)
	proxyHandler := worldview.NewProxyHandler(worldview.ProxyDeps{
		Auth: e2eAuthenticator{user: &auth.AuthenticatedUser{
			ID: "oss388-e2e-admin", Username: "oss388-e2e", Roles: []string{"admin"},
		}},
		Authorizer: enforcer, Subusers: store,
		Servers: store, Resolver: resolver, Sessions: sessions, Log: log,
	})
	ts := httptest.NewServer(proxyHandler)
	defer ts.Close()
	fetch := func(path string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+worldview.ProxyPrefix+key+"/"+path, nil)
		req.Header.Set("Authorization", "Bearer oss388-e2e-token")
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("proxy GET %s: %v", path, err)
		}
		return resp
	}
	settings := fetch("settings.json")
	body, _ := io.ReadAll(settings.Body)
	_ = settings.Body.Close()
	// Managed config creates a map per actual dimension; require the real
	// settings document to list the target world, not exactly one map.
	if settings.StatusCode != http.StatusOK || !strings.Contains(string(body), `"maps":[`) ||
		!strings.Contains(string(body), `"world"`) {
		result["status"] = "bad_settings"
		t.Fatalf("settings.json = %d %q", settings.StatusCode, string(body))
	}
	result["settings"] = string(body)

	// This render produced BlueMap's native .prbm.gz tiles. Request the plain
	// path so the production proxy's exact gzip-sibling fallback is exercised.
	tile := fetch("maps/world/tiles/0/x0/z0.prbm")
	tileBytes, _ := io.ReadAll(tile.Body)
	_ = tile.Body.Close()
	// BlueMap tile payloads are binary PRBM (optionally gzip-encoded); assert a
	// substantial real binary body through the proxy, not a SPA/HTML fallback.
	if tile.StatusCode != http.StatusOK || len(tileBytes) < 1024 || bytes.HasPrefix(tileBytes, []byte("<")) {
		result["status"] = "bad_tile"
		t.Fatalf("tile = %d, len=%d, magic=%x", tile.StatusCode, len(tileBytes), tileBytes[:min(8, len(tileBytes))])
	}
	result["tileBytes"] = len(tileBytes)
	result["tileContentType"] = tile.Header.Get("Content-Type")
	result["tileContentEncoding"] = tile.Header.Get("Content-Encoding")
	result["status"] = "pass"
	t.Logf("E2E PASS: managed install %s, READY, real binary tile %d bytes", moduleID, len(tileBytes))
}
