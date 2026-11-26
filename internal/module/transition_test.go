// SPDX-License-Identifier: MPL-2.0

package module

import (
	"context"
	"errors"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
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

func TestDirectDataWriteBlocksArchiveFence(t *testing.T) {
	write, err := TryBeginServerDataWrite("server-write")
	if err != nil {
		t.Fatalf("begin data write: %v", err)
	}
	if _, err := TryBeginServerModuleArchive("server-write"); !errors.Is(err, ErrServerDataWrite) {
		t.Fatalf("archive during data write = %v, want ErrServerDataWrite", err)
	}
	write.Release()
	fence, err := TryBeginServerModuleArchive("server-write")
	if err != nil {
		t.Fatalf("archive after data write: %v", err)
	}
	fence.Release()
}

func TestArchiveFenceBlocksDirectDataWrite(t *testing.T) {
	fence, err := TryBeginServerModuleArchive("server-write-fence")
	if err != nil {
		t.Fatalf("begin archive: %v", err)
	}
	defer fence.Release()
	if _, err := TryBeginServerDataWrite("server-write-fence"); !errors.Is(err, ErrModuleArchiveExport) {
		t.Fatalf("data write during archive = %v, want ErrModuleArchiveExport", err)
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

func TestCrossedCrossServerDependencyAdmissionDoesNotWait(t *testing.T) {
	a, err := TryServerModuleMutation("cross-server-a")
	if err != nil {
		t.Fatalf("hold A: %v", err)
	}
	defer a.Release()
	b, err := TryServerModuleMutation("cross-server-b")
	if err != nil {
		t.Fatalf("hold B: %v", err)
	}
	defer b.Release()

	// Each side already owns its own gate and tries the other's. A waiting
	// acquisition would deadlock; the nested attempt must fail promptly.
	if _, err := TryServerModuleMutation("cross-server-b"); !errors.Is(err, ErrModuleLifecycleInFlight) {
		t.Fatalf("crossed acquisition B = %v, want ErrModuleLifecycleInFlight", err)
	}
	if _, err := TryServerModuleMutation("cross-server-a"); !errors.Is(err, ErrModuleLifecycleInFlight) {
		t.Fatalf("crossed acquisition A = %v, want ErrModuleLifecycleInFlight", err)
	}
}

// A module start that reaches a dependency on another server takes that
// server's gate without waiting, so crossed acyclic dependencies cannot
// deadlock while each start holds its own server's gate.
func TestCrossServerDependencyStartFailsWithoutWaiting(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	for _, id := range []string{"dep-server-a", "dep-server-b"} {
		if err := store.CreateServer(ctx, &storage.Server{ID: id, Name: id}); err != nil {
			t.Fatalf("create server %s: %v", id, err)
		}
	}
	dependency := &storage.Module{
		ID: "dep-module-b", Name: "dependency", ServerID: "dep-server-b",
		TemplateID: "custom", Status: storage.ModuleStatusStopped, ContainerID: "container-b",
	}
	if err := store.CreateModule(ctx, dependency); err != nil {
		t.Fatalf("create dependency: %v", err)
	}
	moduleA := &storage.Module{
		ID: "dep-module-a", Name: "dependent", ServerID: "dep-server-a",
		TemplateID: "custom", Status: storage.ModuleStatusStopped,
		Dependencies: []*v1.ModuleDependency{{ModuleId: dependency.ID}},
	}
	if err := store.CreateModule(ctx, moduleA); err != nil {
		t.Fatalf("create dependent: %v", err)
	}

	manager := NewManager(store, nil, nil, nil, nil, logger.New())
	held, err := TryServerModuleMutation(dependency.ServerID)
	if err != nil {
		t.Fatalf("hold dependency gate: %v", err)
	}
	defer held.Release()

	started := time.Now()
	err = manager.startDependencies(ctx, moduleA)
	if !errors.Is(err, ErrModuleLifecycleInFlight) {
		t.Fatalf("startDependencies = %v, want ErrModuleLifecycleInFlight", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cross-server dependency waited %s instead of failing fast", elapsed)
	}
}

func TestManagerStopModuleHonorsArchiveFence(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()
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
