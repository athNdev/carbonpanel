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
	"github.com/moby/moby/api/types/container"
)

type recreateFunc func(context.Context, string, *db.Server, *db.ServerConfig) (*docker.RecreateContainerResult, error)

func (f recreateFunc) RecreateContainer(ctx context.Context, id string, server *db.Server, cfg *db.ServerConfig) (*docker.RecreateContainerResult, error) {
	return f(ctx, id, server, cfg)
}

// recordOnlyStore has no UpdateServerFields method, so persistence exercises the
// UpdateServer fallback that writes the whole in-memory server row.
type recordOnlyStore struct {
	saved *db.Server
	err   error
}

func (s *recordOnlyStore) UpdateServer(_ context.Context, server *db.Server) error {
	if s.err != nil {
		return s.err
	}
	copied := *server
	s.saved = &copied
	return nil
}

type fixedContainerResolver struct{ id string }

func (r fixedContainerResolver) ResolveContainer(context.Context, string) (*container.Summary, error) {
	return &container.Summary{ID: r.id}, nil
}

func TestReconcileContainerIdentityFallbackPersistsResolvedID(t *testing.T) {
	store := &recordOnlyStore{}
	server := &db.Server{ID: "server", ContainerID: "stale"}
	adopted, err := ReconcileContainerIdentity(context.Background(), store, fixedContainerResolver{id: "resolved"}, server)
	if err != nil {
		t.Fatalf("ReconcileContainerIdentity: %v", err)
	}
	if !adopted {
		t.Fatal("expected the different canonical container to be adopted")
	}
	if server.ContainerID != "resolved" {
		t.Fatalf("in-memory container id = %q, want resolved", server.ContainerID)
	}
	if store.saved == nil || store.saved.ContainerID != "resolved" {
		t.Fatalf("fallback persistence saved %+v, want the resolved container id", store.saved)
	}
}

func TestReconcileContainerIdentityRestoresPriorIDWhenWriteFails(t *testing.T) {
	writeErr := errors.New("store unavailable")
	store := &recordOnlyStore{err: writeErr}
	server := &db.Server{ID: "server", ContainerID: "stale"}
	adopted, err := ReconcileContainerIdentity(context.Background(), store, fixedContainerResolver{id: "resolved"}, server)
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected the write failure, got %v", err)
	}
	if adopted {
		t.Fatal("a failed write must not report adoption")
	}
	if server.ContainerID != "stale" {
		t.Fatalf("in-memory container id = %q after a failed write, want the prior id", server.ContainerID)
	}
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
	// A stale lifecycle projection must not overwrite independently edited
	// settings while persisting the canonical replacement identity.
	if err := store.UpdateServerFields(ctx, server.ID, map[string]any{"name": "Edited server"}); err != nil {
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
	if err != nil || got.Name != "Edited server" || got.ContainerID != "partial" || got.Status != db.StatusError || got.LastStarted == nil || !got.LastStarted.Equal(started) {
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
