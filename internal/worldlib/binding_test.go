// SPDX-License-Identifier: MPL-2.0
package worldlib

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

type fakeLifecycle struct {
	running bool
	stops   int
	starts  int
	failStart bool
}

func (f *fakeLifecycle) IsRunning(context.Context, string) (bool, error) { return f.running, nil }
func (f *fakeLifecycle) StopServer(context.Context, string) error {
	f.stops++
	f.running = false
	return nil
}
func (f *fakeLifecycle) StartServer(context.Context, string) error {
	f.starts++
	if f.failStart {
		return errors.New("start failed")
	}
	f.running = true
	return nil
}

func newWorldTestStore(t *testing.T) *storage.Store {
	t.Helper()
	cfg := &config.Config{Database: config.DatabaseConfig{
		Path: filepath.Join(t.TempDir(), "world.db"), AutoMigrate: true,
	}}
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedWorldServer(t *testing.T, store *storage.Store, dataPath string) *storage.Server {
	t.Helper()
	server := &storage.Server{
		ID: "srv-1", Name: "Test", NodeID: "local",
		DataPath: dataPath, Status: storage.StatusStopped,
	}
	if err := store.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	return server
}

func seedRevision(t *testing.T, store *storage.Store, entryID, archivePath, sha string) *storage.WorldRevision {
	t.Helper()
	rev := &storage.WorldRevision{
		ID: RevisionID(sha), WorldEntryID: entryID, SHA256: sha,
		SizeBytes: 1024, EntryCount: 3, MCVersion: "1.21.4",
		ModLoader: "VANILLA", StoragePath: archivePath, CreatedBy: "test",
	}
	if err := store.PutWorldRevision(context.Background(), rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

func TestBindStagesPublishesAndRestarts(t *testing.T) {
	ctx := context.Background()
	store := newWorldTestStore(t)
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	libraryRoot := filepath.Join(root, "library")
	if err := os.MkdirAll(filepath.Join(dataRoot, "world"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "world", "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(libraryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	archive, err := WriteFreshWorldArchive(map[string]string{
		"level-name": "world", "level-seed": "42", "gamemode": "survival", "difficulty": "normal",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(libraryRoot, "rev.zip")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	server := seedWorldServer(t, store, dataRoot)
	rev := seedRevision(t, store, "wld_1", archivePath, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	lifecycle := &fakeLifecycle{running: true}
	binder := NewBinder(store, logger.New(), nodestorage.NewResolver(nil), nil, lifecycle, libraryRoot)
	result, err := binder.Bind(ctx, BindRequest{
		ServerID: server.ID, LevelName: "world", RevisionID: rev.ID,
		NodeID: "local", Generation: 1, RequestID: "req-1", Window: "now",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Binding == nil || result.Outcome != "success" {
		t.Fatalf("unexpected result %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "world", "level.dat")); err != nil {
		t.Fatalf("published world has no level.dat: %v", err)
	}
	if lifecycle.stops != 1 || lifecycle.starts != 1 {
		t.Fatalf("lifecycle stops=%d starts=%d", lifecycle.stops, lifecycle.starts)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "world", "old.txt")); !os.IsNotExist(err) {
		t.Fatal("old world file should have been replaced")
	}
	// Idempotent replay returns the same binding.
	again, err := binder.Bind(ctx, BindRequest{
		ServerID: server.ID, LevelName: "world", RevisionID: rev.ID,
		NodeID: "local", Generation: 1, RequestID: "req-2",
	})
	if err != nil || again.Binding.ID != result.Binding.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
}

func TestBindCancellationBeforeMutationCreatesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := newWorldTestStore(t)
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	libraryRoot := filepath.Join(root, "library")
	_ = os.MkdirAll(filepath.Join(dataRoot, "world"), 0o755)
	_ = os.MkdirAll(libraryRoot, 0o755)
	archivePath := filepath.Join(libraryRoot, "rev.zip")
	archive, err := WriteFreshWorldArchive(map[string]string{"level-name": "world"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	server := seedWorldServer(t, store, dataRoot)
	rev := seedRevision(t, store, "wld_2", archivePath, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	binder := NewBinder(store, logger.New(), nodestorage.NewResolver(nil), nil, &fakeLifecycle{}, libraryRoot)
	cancel()
	_, err = binder.Bind(ctx, BindRequest{
		ServerID: server.ID, LevelName: "world", RevisionID: rev.ID,
		NodeID: "local", Generation: 1, RequestID: "cancelled",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dataRoot, "world", "level.dat")); !os.IsNotExist(statErr) {
		t.Fatal("cancelled bind mutated the live world")
	}
	bindings, _ := store.ListWorldBindings(ctx, server.ID, "")
	if len(bindings) != 0 {
		t.Fatal("cancelled bind created a binding")
	}
}

func TestBindRemoteWithoutResolverFailsClosed(t *testing.T) {
	store := newWorldTestStore(t)
	root := t.TempDir()
	server := seedWorldServer(t, store, filepath.Join(root, "data"))
	rev := seedRevision(t, store, "wld_3", filepath.Join(root, "rev.zip"), "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	binder := NewBinder(store, logger.New(), nil, nil, &fakeLifecycle{}, root)
	_, err := binder.Bind(context.Background(), BindRequest{
		ServerID: server.ID, LevelName: "world", RevisionID: rev.ID,
		NodeID: "remote-node", Generation: 1, RequestID: "remote",
	})
	if !errors.Is(err, ErrRemoteUnavailable) {
		t.Fatalf("want ErrRemoteUnavailable, got %v", err)
	}
}

func TestTwoWritersRefusedAtStore(t *testing.T) {
	ctx := context.Background()
	store := newWorldTestStore(t)
	b1 := &storage.WorldBinding{ID: "wlb1", ServerID: "s1", LevelName: "world", RevisionID: "rev_x", NodeID: "local", Generation: 1}
	if err := store.CreateWorldBinding(ctx, b1); err != nil {
		t.Fatal(err)
	}
	b2 := &storage.WorldBinding{ID: "wlb2", ServerID: "s2", LevelName: "world", RevisionID: "rev_x", NodeID: "local", Generation: 1}
	if err := store.CreateWorldBinding(ctx, b2); !errors.Is(err, storage.ErrWorldRevisionBound) {
		t.Fatalf("want ErrWorldRevisionBound, got %v", err)
	}
	if b1.MatchesOwner("local", 2) {
		t.Fatal("stale generation must not match")
	}
	if !b1.MatchesOwner("local", 1) {
		t.Fatal("matching generation must match")
	}
}

func TestKeyedAdmissionReleasesOnCancel(t *testing.T) {
	a := NewKeyedAdmission()
	ctx := context.Background()
	release, err := a.Acquire(ctx, "b", "a")
	if err != nil {
		t.Fatal(err)
	}
	release()
	release() // idempotent
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := a.Acquire(cancelled, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = a.Acquire(ctx, "a")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("admission lock was not released")
	}
}
