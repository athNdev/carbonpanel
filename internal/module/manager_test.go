// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package module

import (
	"context"
	"fmt"
	"net"
	"testing"

	"net/netip"

	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/proxy"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

type mockDockerClient struct {
	proxy.DockerInspector
	inspectFunc func(ctx context.Context, containerID string) (container.InspectResponse, error)
}

func (m *mockDockerClient) ContainerInspect(ctx context.Context, containerID string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	if m.inspectFunc != nil {
		resp, err := m.inspectFunc(ctx, containerID)
		return client.ContainerInspectResult{Container: resp}, err
	}
	return client.ContainerInspectResult{}, nil
}

func setupTestStore(t *testing.T) *storage.Store {
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Path:           ":memory:",
			AutoMigrate:    true,
			MaxConnections: 1,
		},
	}
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}
	return store
}

func TestModuleManager_RestoreProxyRoutesOnStart(t *testing.T) {
	mockCli := &mockDockerClient{
		inspectFunc: func(ctx context.Context, containerID string) (container.InspectResponse, error) {
			return container.InspectResponse{
				NetworkSettings: &container.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						"test-net": {
							IPAddress: netip.MustParseAddr("172.28.0.25"),
						},
					},
				},
			}, nil
		},
	}

	store := setupTestStore(t)
	defer store.Close()

	cfg := &config.Config{
		Module: config.ModuleConfig{
			Enabled: true,
		},
		Proxy: config.ProxyConfig{
			Enabled:  true,
			BindHost: "127.0.0.1",
		},
		Docker: config.DockerConfig{
			NetworkName: "test-net",
		},
	}
	log := logger.New()

	proxyMgr := proxy.NewManagerWithListeners(store, cfg, log, proxy.ListenerFactory{
		TCP: func(network, address string) (net.Listener, error) {
			host, _, err := net.SplitHostPort(address)
			if err != nil || !net.ParseIP(host).IsLoopback() {
				return nil, fmt.Errorf("test refuses non-loopback TCP bind %q", address)
			}
			return net.Listen(network, address)
		},
		UDP: func(network string, address *net.UDPAddr) (*net.UDPConn, error) {
			if address == nil || !address.IP.IsLoopback() {
				return nil, fmt.Errorf("test refuses non-loopback UDP bind %v", address)
			}
			return net.ListenUDP(network, address)
		},
	}, mockCli)
	defer proxyMgr.Stop()

	// Create server
	server := &storage.Server{
		ID:            uuid.New().String(),
		Name:          "survival",
		ProxyHostname: "play.example.com",
		ContainerID:   "srv-container",
	}
	if err := store.CreateServer(context.Background(), server); err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	portProbe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	hostPort := portProbe.LocalAddr().(*net.UDPAddr).Port
	if err := portProbe.Close(); err != nil {
		t.Fatal(err)
	}

	// Create running module with proxy-enabled port (e.g. Geyser UDP)
	runningMod := &storage.Module{
		ID:          uuid.New().String(),
		ServerID:    server.ID,
		Name:        "geyser",
		ContainerID: "geyser-container",
		Status:      storage.ModuleStatusRunning,
		Ports: []*v1.ModulePort{
			{
				Name:          "bedrock",
				HostPort:      int32(hostPort),
				ContainerPort: 19132,
				Protocol:      "udp",
				ProxyEnabled:  true,
			},
		},
	}
	if err := store.CreateModule(context.Background(), runningMod); err != nil {
		t.Fatalf("failed to create running module: %v", err)
	}

	// Create stopped module with proxy-enabled port
	stoppedMod := &storage.Module{
		ID:          uuid.New().String(),
		ServerID:    server.ID,
		Name:        "bluemap",
		ContainerID: "bluemap-container",
		Status:      storage.ModuleStatusStopped,
		Ports: []*v1.ModulePort{
			{
				Name:          "web",
				HostPort:      8100,
				ContainerPort: 8100,
				Protocol:      "tcp",
				ProxyEnabled:  true,
			},
		},
	}
	if err := store.CreateModule(context.Background(), stoppedMod); err != nil {
		t.Fatalf("failed to create stopped module: %v", err)
	}

	// Initialize and start module manager
	modMgr := NewManager(store, nil, nil, cfg, proxyMgr, log)
	if err := modMgr.Start(); err != nil {
		t.Fatalf("failed to start module manager: %v", err)
	}

	// Verify running module's route was restored
	routes := proxyMgr.GetRoutes()
	route, found := routes["udp"]
	if !found {
		t.Fatalf("expected running module's UDP route to be restored on startup, got routes: %+v", routes)
	}
	if route.BackendHost != "172.28.0.25" {
		t.Errorf("expected BackendHost 172.28.0.25, got %s", route.BackendHost)
	}
	if route.BackendPort != 19132 {
		t.Errorf("expected BackendPort 19132, got %d", route.BackendPort)
	}

	// Verify stopped module's route was NOT added
	for key, r := range routes {
		if r.BackendPort == 8100 {
			t.Errorf("expected stopped module route (port 8100) not to be restored, found under key %s", key)
		}
	}
}
