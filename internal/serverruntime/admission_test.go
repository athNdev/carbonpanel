// SPDX-License-Identifier: MPL-2.0
package serverruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
)

func TestStartAdmissionUsesPersistedActiveMemoryWithoutMutation(t *testing.T) {
	for _, fixture := range []struct {
		name          string
		status        db.ServerStatus
		memory, other int
		limit         int64
		refused       bool
	}{
		{"stopped overcommit", db.StatusStopped, 2048, 3072, 0, true},
		{"running counts once", db.StatusRunning, 2048, 2048, 0, false},
		{"starting counts once", db.StatusStarting, 2048, 2048, 0, false},
		{"paused needs admission", db.StatusPaused, 2048, 3072, 0, true},
		{"operator cap", db.StatusStopped, 1024, 1536, 2048, true},
		{"admitted start", db.StatusStopped, 1024, 1024, 0, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := db.NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{Path: ":memory:", MaxConnections: 1}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if err := store.Migrate(); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateNode(ctx, &db.Node{ID: "node", Name: "Node", MaxMemoryMB: fixture.limit}); err != nil {
				t.Fatal(err)
			}
			server := &db.Server{ID: "target", Name: "Target", NodeID: "node", Memory: fixture.memory, Status: fixture.status}
			if err := store.CreateServer(ctx, server); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateServer(ctx, &db.Server{ID: "other", Name: "Other", NodeID: "node", Memory: fixture.other, Status: db.StatusRunning}); err != nil {
				t.Fatal(err)
			}
			err = CheckStartCapacity(ctx, store, server, func(context.Context, string) (int64, error) { return 4096, nil })
			if fixture.refused {
				var refusal *AdmissionError
				if !errors.As(err, &refusal) || refusal.Kind != CapacityExceeded {
					t.Fatalf("expected capacity refusal, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			got, err := store.GetServer(ctx, server.ID)
			if err != nil || got.Status != fixture.status || got.LastStarted != nil {
				t.Fatalf("admission mutated state: %+v, %v", got, err)
			}
		})
	}
}
