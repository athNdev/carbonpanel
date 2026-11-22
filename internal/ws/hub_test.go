// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ws

import (
	"context"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func TestHubMigrateServerContainerUpdatesSubscriptions(t *testing.T) {
	h := &Hub{clients: make(map[*Client]bool)}
	c := &Client{hub: h, subscriptions: map[string]*subscription{}}
	h.clients[c] = true

	ch := make(chan *v1.LogEntry, 1)
	c.subscriptions["s1"] = &subscription{ch: ch, containerID: "old-container"}

	h.MigrateServerContainer("s1", "new-container")

	if got := c.subscriptions["s1"].containerID; got != "new-container" {
		t.Fatalf("subscription containerID = %q, want %q", got, "new-container")
	}
}

func TestHubMigrateServerContainerLeavesOtherServersAlone(t *testing.T) {
	h := &Hub{clients: make(map[*Client]bool)}
	c := &Client{hub: h, subscriptions: map[string]*subscription{}}
	h.clients[c] = true

	c.subscriptions["s2"] = &subscription{ch: make(chan *v1.LogEntry, 1), containerID: "s2-container"}

	h.MigrateServerContainer("s1", "new-container")

	if got := c.subscriptions["s2"].containerID; got != "s2-container" {
		t.Fatalf("unrelated subscription containerID = %q, want unchanged", got)
	}
}

func TestHubMigrateServerContainerIsSafeWithoutSubscription(t *testing.T) {
	h := &Hub{clients: make(map[*Client]bool)}
	c := &Client{hub: h, subscriptions: map[string]*subscription{}}
	h.clients[c] = true

	// Must be a no-op rather than a panic for a client with no subscription.
	h.MigrateServerContainer("s1", "new-container")
	h.MigrateServerContainer("", "new-container")
	h.MigrateServerContainer("s1", "")
}

func TestHubDockerFor_OnlyUsesLocalClientForLocalAliases(t *testing.T) {
	defaultCli := &docker.Client{}
	h := &Hub{docker: defaultCli}

	if got, err := h.dockerFor("node-1"); err == nil || got != nil {
		t.Fatalf("explicit node without pool = (%v, %v), want (nil, error)", got, err)
	}

	for _, nodeID := range []string{"", "local", "default"} {
		got, err := h.dockerFor(nodeID)
		if err != nil || got != defaultCli {
			t.Fatalf("local alias %q = (%v, %v), want configured default client", nodeID, got, err)
		}
	}
}

func TestHubDockerForRejectsUnresolvedPooledNode(t *testing.T) {
	defaultCli := &docker.Client{}
	h := &Hub{docker: defaultCli}
	h.SetPool(docker.NewClientPool(nil, defaultCli, nil))

	if got, err := h.dockerFor("missing-remote-node"); err == nil || got != nil {
		t.Fatalf("unresolved remote node = (%v, %v), want (nil, error)", got, err)
	}
}

func TestHandleCommandDoesNotInspectLocalDockerForUnavailableRemote(t *testing.T) {
	var inspectRequests atomic.Int32
	dockerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			inspectRequests.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ApiVersion":"1.41","Version":"test"}`))
	}))
	defer dockerServer.Close()

	cfg := &config.Config{Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "ws-test.db"), AutoMigrate: true}}
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.CreateServer(context.Background(), &storage.Server{
		ID: "remote-server", Name: "Remote", ModLoader: storage.ModLoaderVanilla,
		MCVersion: "1.21", NodeID: "unavailable-remote", ContainerID: "same-container-id",
		DataPath: t.TempDir(),
	}); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatalf("SeedDefaultPolicies: %v", err)
	}
	localDocker, err := docker.NewClient(dockerServer.URL, logger.New())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = localDocker.Close() }()
	// Ignore the Docker API version negotiation performed during construction.
	inspectRequests.Store(0)

	h := &Hub{
		store: store, enforcer: enforcer, docker: localDocker,
		pool: docker.NewClientPool(nil, localDocker, logger.New()),
	}
	c := &Client{
		hub: h, authenticated: true, user: &auth.AuthenticatedUser{Roles: []string{"admin"}},
		send: make(chan []byte, 4), subscriptions: make(map[string]*subscription),
	}
	c.handleCommand(&v1.CommandMessage{ServerId: "remote-server", Command: "list"})

	if got := inspectRequests.Load(); got != 0 {
		t.Fatalf("local Docker inspect requests = %d, want 0", got)
	}
	msgs := drainClient(t, c)
	if len(msgs) != 1 || msgs[0].GetCommandResult().GetSuccess() || msgs[0].GetCommandResult().GetError() != "node unavailable" {
		t.Fatalf("command result = %v, want node unavailable", msgs)
	}
}

// newCommandTestHub builds a hub wired to a scripted Docker daemon so
// handleCommand's status checks can be exercised without a real daemon.
func newCommandTestHub(t *testing.T, inspectState string) (*Hub, *Client, *storage.Store) {
	t.Helper()

	dockerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ApiVersion":"1.41","Version":"test","Id":"c1","State":{"Status":"` + inspectState + `","Running":` + `false` + `,"Paused":true}}`))
	}))
	t.Cleanup(dockerServer.Close)

	cfg := &config.Config{Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "ws-cmd-test.db"), AutoMigrate: true}}
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatalf("SeedDefaultPolicies: %v", err)
	}
	localDocker, err := docker.NewClient(dockerServer.URL, logger.New())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = localDocker.Close() })

	h := &Hub{
		store: store, enforcer: enforcer, docker: localDocker,
		pool: docker.NewClientPool(nil, localDocker, logger.New()),
	}
	c := &Client{
		hub: h, authenticated: true, user: &auth.AuthenticatedUser{Roles: []string{"admin"}},
		send: make(chan []byte, 4), subscriptions: make(map[string]*subscription),
	}
	return h, c, store
}

func TestHandleCommand_StatusAwareRejections(t *testing.T) {
	cases := []struct {
		name       string
		dbStatus   storage.ServerStatus
		inspect    string
		wantErrHas string
	}{
		{
			name:       "frozen server names the resume action",
			dbStatus:   storage.StatusPaused,
			inspect:    "running",
			wantErrHas: "server is frozen (paused); resume the server before sending commands",
		},
		{
			name:       "deep sleep server names the wake action",
			dbStatus:   storage.StatusDeepSleep,
			inspect:    "exited",
			wantErrHas: "server is in deep sleep; it wakes when a player connects - wake it first to run commands",
		},
		{
			name:       "a container paused outside the panel is frozen the same way",
			dbStatus:   storage.StatusRunning,
			inspect:    "paused",
			wantErrHas: "server is frozen (paused); resume the server before sending commands",
		},
		{
			name:       "a plainly stopped server keeps the generic message",
			dbStatus:   storage.StatusRunning,
			inspect:    "exited",
			wantErrHas: "server is not running",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, c, store := newCommandTestHub(t, tc.inspect)
			if err := store.CreateServer(context.Background(), &storage.Server{
				ID: "s1", Name: "S", ModLoader: storage.ModLoaderVanilla,
				MCVersion: "1.21", ContainerID: "c1", DataPath: t.TempDir(),
			}); err != nil {
				t.Fatalf("CreateServer: %v", err)
			}
			if err := store.DB().Model(&storage.Server{}).Where("id = ?", "s1").
				Update("status", tc.dbStatus).Error; err != nil {
				t.Fatalf("set status: %v", err)
			}

			c.handleCommand(&v1.CommandMessage{ServerId: "s1", Command: "list"})
			msgs := drainClient(t, c)
			if len(msgs) != 1 {
				t.Fatalf("messages = %v, want one command result", msgs)
			}
			result := msgs[0].GetCommandResult()
			if result == nil || result.GetSuccess() {
				t.Fatalf("command result = %v, want a failure", msgs)
			}
			if !strings.Contains(result.GetError(), tc.wantErrHas) {
				t.Fatalf("error = %q, want it to contain %q", result.GetError(), tc.wantErrHas)
			}
		})
	}
}
