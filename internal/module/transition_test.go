// SPDX-License-Identifier: MPL-2.0

package module

import (
	"context"
	"errors"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
)

func TestModuleArchiveFenceBlocksMutations(t *testing.T) {
	ctx := context.Background()
	fence, err := TryBeginServerModuleArchive("server-fence")
	if err != nil {
		t.Fatalf("begin archive fence: %v", err)
	}
	defer fence.Release()

	deadline, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if _, err := beginServerModuleMutation(deadline, "server-fence"); !errors.Is(err, ErrModuleArchiveExport) {
		t.Fatalf("mutation during archive = %v, want ErrModuleArchiveExport", err)
	}
}

func TestModuleMutationBlocksArchiveFence(t *testing.T) {
	ctx := context.Background()
	mutation, err := beginServerModuleMutation(ctx, "server-busy")
	if err != nil {
		t.Fatalf("begin mutation: %v", err)
	}
	defer mutation.Release()

	if _, err := TryBeginServerModuleArchive("server-busy"); !errors.Is(err, ErrModuleLifecycleInFlight) {
		t.Fatalf("archive during mutation = %v, want ErrModuleLifecycleInFlight", err)
	}
}

func TestModuleMutationsSerialize(t *testing.T) {
	ctx := context.Background()
	first, err := beginServerModuleMutation(ctx, "server-serial")
	if err != nil {
		t.Fatalf("first mutation: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		second, err := beginServerModuleMutation(ctx, "server-serial")
		if err == nil {
			close(acquired)
			second.Release()
		}
	}()

	select {
	case <-acquired:
		t.Fatal("second mutation acquired while the first was held")
	case <-time.After(50 * time.Millisecond):
	}
	first.Release()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("waiting mutation was not woken by release")
	}
}

func TestManagerStopModuleHonorsArchiveFence(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()
	ctx := context.Background()
	server := &storage.Server{ID: "server-manager-fence", Name: "fixture"}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatalf("create server: %v", err)
	}
	if err := store.CreateModule(ctx, &storage.Module{
		ID: "module-manager-fence", Name: "sidecar", ServerID: server.ID,
		TemplateID: "custom", Status: storage.ModuleStatusRunning, ContainerID: "container-1",
	}); err != nil {
		t.Fatalf("create module: %v", err)
	}

	manager := NewManager(store, nil, nil, nil, nil, nil)
	fence, err := TryBeginServerModuleArchive(server.ID)
	if err != nil {
		t.Fatalf("begin archive fence: %v", err)
	}
	defer fence.Release()

	if err := manager.StopModule(ctx, "module-manager-fence"); !errors.Is(err, ErrModuleArchiveExport) {
		t.Fatalf("StopModule during archive = %v, want ErrModuleArchiveExport", err)
	}
}
