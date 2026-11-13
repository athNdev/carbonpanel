// SPDX-License-Identifier: MPL-2.0
package serverruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
)

type recreateFunc func(context.Context, string, *db.Server, *db.ServerConfig) (*docker.RecreateContainerResult, error)

func (f recreateFunc) RecreateContainer(ctx context.Context, id string, server *db.Server, cfg *db.ServerConfig) (*docker.RecreateContainerResult, error) {
	return f(ctx, id, server, cfg)
}

func TestRecreationPersistsObservedIdentityAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := db.NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{Path: ":memory:", MaxConnections: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Hour).Truncate(time.Second)
	server := &db.Server{ID: "server", Name: "Server", NodeID: "node", ContainerID: "old", Status: db.StatusRunning, LastStarted: &started}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	client := recreateFunc(func(context.Context, string, *db.Server, *db.ServerConfig) (*docker.RecreateContainerResult, error) {
		cancel()
		return &docker.RecreateContainerResult{NewContainerID: "partial"}, context.Canceled
	})
	_, err = Recreate(ctx, store, client, server, &db.ServerConfig{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	got, err := store.GetServer(context.Background(), server.ID)
	if err != nil || got.ContainerID != "partial" || got.Status != db.StatusError || got.LastStarted == nil || !got.LastStarted.Equal(started) {
		t.Fatalf("recovery state = %+v, error %v", got, err)
	}
}

func TestRecreationReturnsBothDockerAndPersistenceFailures(t *testing.T) {
	store, err := db.NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{Path: ":memory:", MaxConnections: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	dockerErr := errors.New("replacement failed")
	client := recreateFunc(func(context.Context, string, *db.Server, *db.ServerConfig) (*docker.RecreateContainerResult, error) {
		return &docker.RecreateContainerResult{NewContainerID: "partial"}, dockerErr
	})
	_, err = Recreate(context.Background(), store, client, &db.Server{ID: "server"}, &db.ServerConfig{})
	if !errors.Is(err, dockerErr) || err == dockerErr {
		t.Fatalf("expected joined Docker and persistence errors, got %v", err)
	}
}
