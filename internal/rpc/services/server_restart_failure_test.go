// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestartServer_SetsStatusErrorOnFailure(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	log := logger.New()

	// Host port 2375 may belong to a running Docker daemon. Wire every
	// lookup path to one private loopback endpoint that refuses mutations.
	var attempts atomic.Int32
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			w.Header().Set("Api-Version", "1.45")
			w.Header().Set("Ostype", "linux")
			return
		}
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"message":"injected Docker failure"}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(daemon.Close)
	host := "tcp://" + strings.TrimPrefix(daemon.URL, "http://")
	t.Setenv("DOCKER_HOST", host)
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	cfg := &config.Config{
		Storage: config.StorageConfig{DataDir: t.TempDir()},
		Docker:  config.DockerConfig{Host: host},
	}
	ctx := context.Background()
	require.NoError(t, store.CreateNode(ctx, &db.Node{
		ID:      "test-node",
		Name:    "Test Node",
		Host:    host,
		Enabled: true,
		Status:  db.NodeStatusOnline,
	}))

	dockerCli, err := docker.NewClient(host, log)
	require.NoError(t, err)
	pool := docker.NewClientPool(store, dockerCli, log)
	pool.RegisterClient("test-node", dockerCli)
	t.Cleanup(func() { _ = pool.Close() })
	svc := NewServerService(store, dockerCli, nil, cfg, nil, nil, nil, nil, nil, log, nil, nil, nil)
	svc.pool = pool
	svc.hostMemoryMB = func(context.Context, string) (int64, error) { return 16 * 1024, nil }

	server := &db.Server{
		ID:          "srv-restart-fail",
		Name:        "Restart Fail Server",
		NodeID:      "test-node",
		Port:        25565,
		Memory:      1024,
		Status:      db.StatusRunning,
		ContainerID: "deadbeef-fake",
		DataPath:    t.TempDir(),
	}
	require.NoError(t, store.CreateServer(ctx, server))

	// Attempt RestartServer
	_, err = svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID}))
	require.Error(t, err)
	require.Positive(t, attempts.Load(), "restart must reach the isolated Docker endpoint")

	// Verify server status was updated to Error in the database
	updated, err := store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	assert.Equal(t, db.StatusError, updated.Status)
}
