// SPDX-License-Identifier: MPL-2.0

package module

import (
	"context"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// A reserved create/recreate consumes the caller's handle even when its first
// module lookup or ownership check fails. A leaked handle would leave the
// server's module mutation gate held after a rejected request, blocking later
// admission until the process restarts.
func TestReservedModuleWorkReleasesHandleOnEarlyFailure(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	for _, serverID := range []string{"reservation-a", "reservation-b"} {
		if err := store.CreateServer(ctx, &storage.Server{ID: serverID, Name: serverID}); err != nil {
			t.Fatalf("create server %s: %v", serverID, err)
		}
	}
	if err := store.CreateModule(ctx, &storage.Module{
		ID: "reservation-module", Name: "sidecar", ServerID: "reservation-a",
		TemplateID: "custom", Status: storage.ModuleStatusStopped,
	}); err != nil {
		t.Fatalf("create module: %v", err)
	}

	manager := NewManager(store, nil, nil, nil, nil, logger.New())

	tests := []struct {
		name string
		run  func(context.Context, *ModuleTransition) error
	}{
		{
			name: "create missing module",
			run: func(ctx context.Context, reservation *ModuleTransition) error {
				return manager.CreateAndStartModuleReserved(ctx, "missing-module", true, reservation)
			},
		},
		{
			name: "create mismatched server",
			run: func(ctx context.Context, reservation *ModuleTransition) error {
				return manager.CreateAndStartModuleReserved(ctx, "reservation-module", true, reservation)
			},
		},
		{
			name: "recreate missing module",
			run: func(ctx context.Context, reservation *ModuleTransition) error {
				return manager.RecreateModuleReserved(ctx, "missing-module", reservation)
			},
		},
		{
			name: "recreate mismatched server",
			run: func(ctx context.Context, reservation *ModuleTransition) error {
				return manager.RecreateModuleReserved(ctx, "reservation-module", reservation)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Mismatched cases reserve the other server so the ownership check
			// is the early failure; missing-module cases fail at the lookup.
			reserveID := "reservation-a"
			if test.name == "create mismatched server" || test.name == "recreate mismatched server" {
				reserveID = "reservation-b"
			}
			reservation, err := manager.ReserveModuleLifecycle(ctx, reserveID)
			if err != nil {
				t.Fatalf("reserve lifecycle: %v", err)
			}
			if err := test.run(ctx, reservation); err == nil {
				t.Fatal("expected the reserved work to fail")
			}

			// Admission must be available again immediately after the rejected
			// request, on the server whose handle was handed to the worker.
			probe, err := TryServerModuleMutation(reserveID)
			if err != nil {
				t.Fatalf("server admission still held after early failure: %v", err)
			}
			probe.Release()
		})
	}
}
