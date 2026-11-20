// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/bluemapdiscovery"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/hostmount"
	"github.com/athNdev/carbon-panel/internal/module"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc/services"
	"github.com/athNdev/carbon-panel/internal/worldview"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

func TestManagedBlueMapRegisteredNodeConnect(t *testing.T) {
	const (
		localID    = "f7fcd1c6-d3a5-44e2-bf59-c1677c5147b4"
		remoteID   = "6edcc39c-9b6f-4047-9677-70aa3ae10ce1"
		disabledID = "7e42c02f-d267-4eb7-a185-f76b8ae94e23"
		unknownID  = "906c6e80-1deb-4e13-8dc3-3c431922a8ec"
		level      = "registered_world"
	)
	ctx := context.Background()
	srv, ts := buildHarness(t, false, false, nil, false)
	for _, node := range []*storage.Node{
		{ID: localID, Name: "Registered local", Host: "unix:///var/run/docker.sock", IsLocal: true, Enabled: true},
		{ID: remoteID, Name: "Remote", Host: "tcp://docker.example.invalid:2376", Enabled: true},
		{ID: disabledID, Name: "Disabled local", Host: "unix:///var/run/docker.sock", IsLocal: true, Enabled: true},
	} {
		if err := srv.store.CreateNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	disabled, err := srv.store.GetNode(ctx, disabledID)
	if err != nil {
		t.Fatal(err)
	}
	disabled.Enabled = false
	if err := srv.store.UpdateNode(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateModuleTemplate(ctx, &storage.ModuleTemplate{
		ID: worldview.BuiltinBlueMapTemplateID, Name: "BlueMap",
		DockerImage: "ghcr.io/bluemap-minecraft/bluemap:v5.29", Type: storage.ModuleTemplateTypeBuiltin,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	manager := module.NewManager(srv.store, nil, nil, cfg, nil, srv.log)
	controller, err := docker.NewClient("tcp://127.0.0.1:1", srv.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	mountService := func(prefix string, controller *docker.Client) carbonpanelv1connect.ModuleServiceClient {
		service := services.NewModuleService(srv.store, controller, manager, srv.proxyManager, srv.authManager, cfg, nil, srv.log)
		service.SetEnforcer(srv.enforcer)
		service.SetStorageResolver(nodestorage.NewResolver(nil))
		path, handler := carbonpanelv1connect.NewModuleServiceHandler(service, connect.WithInterceptors(srv.authInterceptor()))
		ts.Config.Handler.(*http.ServeMux).Handle(prefix+path, http.StripPrefix(prefix, handler))
		return carbonpanelv1connect.NewModuleServiceClient(ts.Client(), ts.URL+prefix)
	}
	api := mountService("", controller)
	apiWithoutController := mountService("/controller-only", nil)

	for _, tc := range []struct {
		name, nodeID, existingNode string
		readable                   bool
		consent                    bool
		wantCode                   connect.Code
		wantReason                 string
	}{
		{"legacy", "default", "", true, true, 0, ""},
		{"registered-local", localID, "", true, true, 0, ""},
		{"transition-in-progress", localID, "", true, true, connect.CodeAborted, "transition"},
		{"registered-without-controller", localID, "", true, true, connect.CodeFailedPrecondition, "no configured controller Docker client"},
		{"registered-local-without-consent", localID, "", true, false, connect.CodeInvalidArgument, "explicit acceptance"},
		{"registered-local-without-world", localID, "", false, true, connect.CodeFailedPrecondition, "readable Minecraft Java Edition world"},
		{"registered-remote", remoteID, "", true, true, connect.CodeFailedPrecondition, "remote node"},
		{"registered-disabled", disabledID, "", true, true, connect.CodeFailedPrecondition, "local Docker node is disabled"},
		{"unknown", unknownID, "", true, true, connect.CodeFailedPrecondition, "ownership could not be verified"},
		{"previous-assignment", localID, remoteID, true, true, connect.CodeFailedPrecondition, "previous node assignment"},
		{"previous-assignment-legacy", "default", remoteID, true, true, connect.CodeFailedPrecondition, "previous node assignment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			world := filepath.Join(root, level)
			if tc.readable {
				if err := os.Mkdir(world, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(world, "level.dat"), []byte("existing world fixture"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			server := &storage.Server{ID: tc.name, Name: tc.name, NodeID: tc.nodeID, DataPath: root, Status: storage.StatusStopped}
			if err := srv.store.CreateServer(ctx, server); err != nil {
				t.Fatal(err)
			}
			levelName := level
			if err := srv.store.SaveServerConfig(ctx, &storage.ServerConfig{ID: server.ID, ServerID: server.ID, Level: &levelName}); err != nil {
				t.Fatal(err)
			}
			var existing []string
			if tc.existingNode != "" {
				mod := &storage.Module{ID: tc.name + "-existing", ServerID: server.ID, NodeID: tc.existingNode, TemplateID: worldview.BuiltinBlueMapTemplateID}
				if err := srv.store.CreateModule(ctx, mod); err != nil {
					t.Fatal(err)
				}
				existing = append(existing, mod.ID)
			}
			token := seedWorldViewToken(t, srv, tc.name, tc.name+"-installer", []rbac.Permission{
				{Resource: rbac.ResourceModules, Action: rbac.ActionCreate, ObjectID: server.ID},
				{Resource: rbac.ResourceModules, Action: rbac.ActionStart, ObjectID: server.ID},
			})
			metadata := map[string]string{"bluemap_managed": "true"}
			if tc.consent {
				metadata["bluemap_consent_accepted"] = "true"
			}
			req := connect.NewRequest(&v1.CreateModuleRequest{
				Name: "BlueMap", ServerId: server.ID, TemplateId: worldview.BuiltinBlueMapTemplateID,
				Metadata: metadata, StartImmediately: true,
			})
			req.Header().Set("Authorization", "Bearer "+token)
			if tc.name == "transition-in-progress" {
				transition, err := docker.TryBeginServerTransition(server.ID)
				if err != nil {
					t.Fatal(err)
				}
				defer transition.Release()
			}
			client := api
			if tc.name == "registered-without-controller" {
				client = apiWithoutController
			}
			resp, err := client.CreateModule(ctx, req)
			if tc.wantCode != 0 {
				if connect.CodeOf(err) != tc.wantCode || !strings.Contains(err.Error(), tc.wantReason) {
					t.Fatalf("CreateModule = %v, want %v containing %q", err, tc.wantCode, tc.wantReason)
				}
				modules, listErr := srv.store.ListServerModules(ctx, server.ID)
				if listErr != nil || len(modules) != len(existing) || (len(existing) > 0 && modules[0].ID != existing[0]) {
					t.Fatalf("refusal changed module rows: %v, %v", modules, listErr)
				}
				if _, statErr := os.Stat(filepath.Join(root, "modules")); !os.IsNotExist(statErr) {
					t.Fatalf("refusal created module storage: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := srv.store.GetModule(ctx, resp.Msg.Module.Id)
			if err != nil || persisted.NodeID != server.NodeID || persisted.Metadata["bluemap_consent_by"] != tc.name {
				t.Fatalf("wrong module/node/consent: %+v, %v", persisted, err)
			}
			if len(persisted.Ports) != 1 || persisted.Ports[0].HostPort >= 0 || len(persisted.AccessUrls) != 0 {
				t.Fatalf("expected private renderer port and no access URLs: %+v, %v", persisted.Ports, persisted.AccessUrls)
			}
			mapFile := filepath.Join(root, "modules", "bluemap", "config", "maps", level+".conf")
			b, err := os.ReadFile(mapFile)
			if err != nil || !strings.Contains(string(b), "world: \"/app/world\"") {
				t.Fatalf("map config not committed for actual level: %s, %v", b, err)
			}
			retained := string(b) + "# operator change\n"
			if err := os.WriteFile(mapFile, []byte(retained), 0o644); err != nil {
				t.Fatal(err)
			}
			retry := func(token string) (*connect.Response[v1.CreateModuleResponse], error) {
				req := connect.NewRequest(&v1.CreateModuleRequest{
					Name: "BlueMap", ServerId: server.ID, TemplateId: worldview.BuiltinBlueMapTemplateID,
					Metadata: map[string]string{"bluemap_managed": "true"},
				})
				req.Header().Set("Authorization", "Bearer "+token)
				return api.CreateModule(ctx, req)
			}
			if tc.name == "legacy" {
				server.NodeID = "local"
				if err := srv.store.UpdateServer(ctx, server); err != nil {
					t.Fatal(err)
				}
			}
			repeated, err := retry(token)
			if err != nil || repeated.Msg.GetModule().GetId() != persisted.ID {
				t.Fatalf("repeated install failed to adopt exact module %s: %v, %v", persisted.ID, repeated, err)
			}
			other := seedWorldViewToken(t, srv, tc.name+"-other", tc.name+"-other-installer", []rbac.Permission{
				{Resource: rbac.ResourceModules, Action: rbac.ActionCreate, ObjectID: "different-server"},
			})
			if response, err := retry(other); connect.CodeOf(err) != connect.CodePermissionDenied || response != nil {
				t.Fatalf("wrong-scope retry returned existing module: %v, %v", response, err)
			}
			modules, err := srv.store.ListServerModules(ctx, server.ID)
			if err != nil || len(modules) != 1 || modules[0].ID != persisted.ID {
				t.Fatalf("retries changed the module rows: %v, %v", modules, err)
			}
			b, err = os.ReadFile(mapFile)
			if err != nil || string(b) != retained {
				t.Fatalf("retries rewrote the operator's map config: %s, %v", b, err)
			}
			b, err = os.ReadFile(filepath.Join(world, "level.dat"))
			if err != nil || string(b) != "existing world fixture" {
				t.Fatalf("original world changed: %s, %v", b, err)
			}
			stale := &storage.Module{
				ID: tc.name + "-old", ServerID: server.ID, NodeID: remoteID, TemplateID: worldview.BuiltinBlueMapTemplateID,
			}
			if err := srv.store.CreateModule(ctx, stale); err != nil {
				t.Fatal(err)
			}
			repeated, err = retry(token)
			if err != nil || repeated.Msg.GetModule().GetId() != persisted.ID {
				t.Fatalf("prior assignment shadowed the current renderer: %v, %v", repeated, err)
			}
		})
	}
}

// The Docker endpoint only supplies inspect metadata. No live daemon, renderer,
// Minecraft process or world is used by the standard test suite.
func TestPrivateBlueMapRegisteredNodeConnect(t *testing.T) {
	const (
		localID     = "1745d369-cc9b-4180-8bb7-d62f8f6864d4"
		remoteID    = "eaa198b6-a911-4b98-8060-d05579ac2b91"
		unknownID   = "71c97a6c-c066-4ffa-ae45-79165da55851"
		serverID    = "registered-map-server"
		moduleID    = "registered-map-module"
		containerID = "renderer-container"
		level       = "registered_world"
		network     = "fixture-panel-network"
	)
	ctx := context.Background()
	srv, ts := buildHarness(t, false, false, nil, false)
	for _, node := range []*storage.Node{
		{ID: localID, Name: "Registered local", Host: "unix:///var/run/docker.sock", IsLocal: true, Enabled: true},
		// Persisted locality must win even on a legacy row that the general
		// Docker locator would infer is local from its endpoint string.
		{ID: remoteID, Name: "Registered remote", Host: "unix://" + filepath.ToSlash(filepath.Join(t.TempDir(), "fixture-docker.sock")), Enabled: true},
	} {
		if err := srv.store.CreateNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	for name, content := range map[string]string{
		"modules/bluemap/config/core.conf":                              "accept-download: true",
		"modules/bluemap/config/maps/" + level + ".conf":                "id: " + level + "\nworld: /app/world",
		"modules/bluemap/web/maps/" + level + "/tiles/rendered.fixture": "tile fixture",
	} {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	server := &storage.Server{ID: serverID, Name: "Registered map", DataPath: root, NodeID: localID}
	if err := srv.store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	levelName := level
	if err := srv.store.SaveServerConfig(ctx, &storage.ServerConfig{ID: server.ID, ServerID: server.ID, Level: &levelName}); err != nil {
		t.Fatal(err)
	}
	mod := &storage.Module{
		ID: moduleID, ServerID: serverID, NodeID: localID, Name: "BlueMap",
		TemplateID: worldview.BuiltinBlueMapTemplateID, ContainerID: containerID,
		Metadata: map[string]string{"bluemap_version": "v5.29"},
	}
	if err := srv.store.CreateModule(ctx, mod); err != nil {
		t.Fatal(err)
	}
	var dockerCalls, inspectCalls atomic.Int32
	var running atomic.Bool
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dockerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/_ping":
			w.Header().Set("Api-Version", "1.45")
			w.Header().Set("Ostype", "linux")
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte(`[{"Id":"renderer-container","State":"running"}]`))
		case strings.HasSuffix(r.URL.Path, "/containers/"+containerID+"/json"):
			inspectCalls.Add(1)
			state := "exited"
			endpoint := map[string]any{"IPAddress": "127.0.0.2"}
			if running.Load() {
				// Omit the address to test storage facts without dialing a listener.
				state = "running"
				endpoint = map[string]any{}
			}
			if err := json.NewEncoder(w).Encode(map[string]any{
				"Id":    containerID,
				"State": map[string]any{"Status": state},
				"Config": map[string]any{
					"Image":        "ghcr.io/bluemap-minecraft/bluemap:v5.29",
					"ExposedPorts": map[string]any{"8100/tcp": map[string]any{}},
					"Labels": map[string]string{
						"carbon-panel.module.id": moduleID, "carbon-panel.module.server_id": serverID,
						"carbon-panel.node.id": localID, "carbon-panel.module.template_id": worldview.BuiltinBlueMapTemplateID,
					},
				},
				"HostConfig":      map[string]any{},
				"NetworkSettings": map[string]any{"Networks": map[string]any{network: endpoint}},
				"Mounts":          []map[string]any{{"Type": "bind", "Source": hostmount.Translate(filepath.Join(root, level)), "Destination": "/app/world", "RW": false}},
			}); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer daemon.Close()
	dockerConfig := docker.ClientConfig{NetworkName: network}
	dockerClient, err := docker.NewClient("tcp://"+strings.TrimPrefix(daemon.URL, "http://"), srv.log, dockerConfig)
	if err != nil {
		t.Fatal(err)
	}
	pool := docker.NewClientPool(srv.store, dockerClient, srv.log, dockerConfig)
	defer func() { _ = pool.Close() }()
	discovery := bluemapdiscovery.New(srv.store, pool, nodestorage.NewResolver(nil), network, srv.log)
	resolver := worldview.NewResolverWithDiscovery(worldview.Config{}, docker.NewLocator(pool, dockerClient, srv.store), discovery)
	service := worldview.NewService(worldview.ServiceDeps{Store: srv.store, Authorizer: srv.enforcer, Resolver: resolver})
	path, handler := carbonpanelv1connect.NewWorldViewServiceHandler(service, connect.WithInterceptors(srv.authInterceptor()))
	ts.Config.Handler.(*http.ServeMux).Handle(path, handler)
	api := carbonpanelv1connect.NewWorldViewServiceClient(ts.Client(), ts.URL)
	token := seedWorldViewToken(t, srv, "map-reader", "map-role", []rbac.Permission{
		{Resource: rbac.ResourceWorldView, Action: rbac.ActionRead, ObjectID: serverID},
	})
	getStatus := func() *v1.WorldViewTarget {
		t.Helper()
		req := connect.NewRequest(&v1.GetWorldViewStatusRequest{ServerId: serverID})
		req.Header().Set("Authorization", "Bearer "+token)
		resp, err := api.GetWorldViewStatus(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.Target
	}

	got := getStatus()
	if got.NodeId != localID || got.RendererType != v1.RendererType_RENDERER_TYPE_BLUEMAP ||
		got.RendererState != v1.RendererState_RENDERER_STATE_STOPPED {
		t.Fatalf("expected discovered BlueMap and stopped lifecycle, got %+v", got)
	}
	running.Store(true)
	facts, err := discovery.FindRenderer(ctx, localID, serverID)
	if err != nil || facts == nil || !facts.IdentityVerified || !facts.NetworkVerified || !facts.MountVerified ||
		!facts.ReadOnlyWorldMount || !facts.AcceptDownload || !facts.WorldMatchesServer ||
		!facts.Rendered || facts.MapID != level || facts.WorldName != level || facts.BaseURL != "" {
		t.Fatalf("registered-node discovery failed to read exact config and tiles: %+v, %v", facts, err)
	}

	// An edited endpoint may still carry the persisted local marker. Without
	// a default controller client, that endpoint must never receive a probe.
	stored, err := srv.store.GetNode(ctx, localID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Host = "tcp://" + strings.TrimPrefix(daemon.URL, "http://")
	if err := srv.store.UpdateNode(ctx, stored); err != nil {
		t.Fatal(err)
	}
	poolWithoutController := docker.NewClientPool(srv.store, nil, srv.log, dockerConfig)
	defer func() { _ = poolWithoutController.Close() }()
	withoutController := bluemapdiscovery.New(srv.store, poolWithoutController, nodestorage.NewResolver(nil), network, srv.log)
	before := dockerCalls.Load()
	facts, err = withoutController.FindRenderer(ctx, localID, serverID)
	if err != nil || facts == nil || facts.Lifecycle != worldview.ModuleFailed ||
		facts.BaseURL != "" || dockerCalls.Load() != before {
		t.Fatalf("discovery used an edited node endpoint without the controller client: %+v, %v", facts, err)
	}

	tests := []struct{ name, serverNode, moduleNode string }{
		{"remote", remoteID, remoteID},
		{"missing node record", unknownID, unknownID},
		{"module from previous assignment", localID, remoteID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server.NodeID = tc.serverNode
			mod.NodeID = tc.moduleNode
			if err := srv.store.UpdateServer(ctx, server); err != nil {
				t.Fatal(err)
			}
			if err := srv.store.UpdateModule(ctx, mod); err != nil {
				t.Fatal(err)
			}
			before := inspectCalls.Load()
			got := getStatus()
			if got.RendererState != v1.RendererState_RENDERER_STATE_ABSENT || inspectCalls.Load() != before {
				t.Fatalf("unverified scope inspected a Docker container or exposed a renderer: %+v", got)
			}
		})
	}
}
