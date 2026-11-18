// SPDX-License-Identifier: MPL-2.0
package worldlib

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/operation"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

type fakeLifecycle struct {
	running   bool
	stops     int
	starts    int
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

func TestRecoverRestoresRetainedFamilyAndRefusesAmbiguity(t *testing.T) {
	ctx := context.Background()
	store := newWorldTestStore(t)
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	oldFamily := filepath.Join(dataRoot, ".world-library", "old-drill", "world")
	if err := os.MkdirAll(oldFamily, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldFamily, "level.dat"), []byte("old-world"), 0o644); err != nil {
		t.Fatal(err)
	}
	binder := NewBinder(store, logger.New(), nodestorage.NewResolver(nil), nil, &fakeLifecycle{}, root)
	summary, err := binder.Recover(ctx, "srv-1", dataRoot, "local", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "restored 1") {
		t.Fatalf("unexpected recovery summary %q", summary)
	}
	data, err := os.ReadFile(filepath.Join(dataRoot, "world", "level.dat"))
	if err != nil || string(data) != "old-world" {
		t.Fatalf("recovery did not restore old world: %q, %v", data, err)
	}

	// Both live and retained trees present: ambiguous, operator must resolve.
	ambiguousOld := filepath.Join(dataRoot, ".world-library", "old-ambiguous", "world")
	if err := os.MkdirAll(ambiguousOld, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ambiguousOld, "level.dat"), []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = binder.Recover(ctx, "srv-1", dataRoot, "local", 1)
	if !errors.Is(err, ErrUncertainOutcome) {
		t.Fatalf("ambiguous recovery must fail closed, got %v", err)
	}
}

func TestBindSwapReplacesBindingAtomically(t *testing.T) {
	ctx := context.Background()
	store := newWorldTestStore(t)
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	libraryRoot := filepath.Join(root, "library")
	_ = os.MkdirAll(filepath.Join(dataRoot, "world"), 0o755)
	_ = os.MkdirAll(libraryRoot, 0o755)
	server := seedWorldServer(t, store, dataRoot)
	makeRev := func(sha string) *storage.WorldRevision {
		archive, err := WriteFreshWorldArchive(map[string]string{"level-name": "world"}, "")
		if err != nil {
			t.Fatal(err)
		}
		archivePath := filepath.Join(libraryRoot, sha[:8]+".zip")
		if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
			t.Fatal(err)
		}
		return seedRevision(t, store, "wld_swap", archivePath, sha)
	}
	first := makeRev("1111111111111111111111111111111111111111111111111111111111111111")
	second := makeRev("2222222222222222222222222222222222222222222222222222222222222222")
	binder := NewBinder(store, logger.New(), nodestorage.NewResolver(nil), nil, &fakeLifecycle{}, libraryRoot)
	base := BindRequest{ServerID: server.ID, LevelName: "world", NodeID: "local", Generation: 1}
	one, err := binder.Bind(ctx, BindRequest{
		ServerID: base.ServerID, LevelName: base.LevelName, RevisionID: first.ID,
		NodeID: base.NodeID, Generation: base.Generation, RequestID: "swap-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	two, err := binder.Bind(ctx, BindRequest{
		ServerID: base.ServerID, LevelName: base.LevelName, RevisionID: second.ID,
		NodeID: base.NodeID, Generation: base.Generation, RequestID: "swap-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if two.Binding.ID != one.Binding.ID {
		t.Fatalf("swap replaced row id %s with %s; want an in-place update", one.Binding.ID, two.Binding.ID)
	}
	bindings, err := store.ListWorldBindings(ctx, server.ID, "")
	if err != nil || len(bindings) != 1 || bindings[0].RevisionID != second.ID {
		t.Fatalf("bindings = %+v, %v; want one row on the second revision", bindings, err)
	}
}

func TestBindReportsUnknownOutcomeAndKeepsLock(t *testing.T) {
	ctx := context.Background()
	store := newWorldTestStore(t)
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	libraryRoot := filepath.Join(root, "library")
	_ = os.MkdirAll(filepath.Join(dataRoot, "world"), 0o755)
	_ = os.MkdirAll(libraryRoot, 0o755)
	archive, err := WriteFreshWorldArchive(map[string]string{"level-name": "world"}, "")
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(libraryRoot, "rev.zip")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	server := seedWorldServer(t, store, dataRoot)
	rev := seedRevision(t, store, "wld_unknown", archivePath, "3333333333333333333333333333333333333333333333333333333333333333")
	// The server was running and the restart after the swap could not be
	// observed. That is an uncertain external effect, not a failed retry.
	binder := NewBinder(store, logger.New(), nodestorage.NewResolver(nil), nil,
		&fakeLifecycle{running: true, failStart: true}, libraryRoot)
	result, err := binder.Bind(ctx, BindRequest{
		ServerID: server.ID, LevelName: "world", RevisionID: rev.ID,
		NodeID: "local", Generation: 1, RequestID: "unknown-1",
	})
	if !errors.Is(err, ErrUncertainOutcome) {
		t.Fatalf("want ErrUncertainOutcome, got %v", err)
	}
	if result == nil || result.Outcome != string(operation.OutcomeUnknown) || result.OperationID == "" {
		t.Fatalf("result = %+v, want an unknown outcome with an operation id", result)
	}
	active, err := store.ListActiveServerOperations(ctx)
	if err != nil || len(active) != 1 || active[0].ID != result.OperationID {
		t.Fatalf("active = %+v, %v; uncertain operation must keep its lock", active, err)
	}
	if !strings.Contains(active[0].LastError, operation.UncertainOutcomeMarker) {
		t.Fatalf("LastError = %q, want the uncertainty marker", active[0].LastError)
	}
}

func TestConcurrentSameNodeBindsSerializeAndSwap(t *testing.T) {
	ctx := context.Background()
	store := newWorldTestStore(t)
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	libraryRoot := filepath.Join(root, "library")
	_ = os.MkdirAll(filepath.Join(dataRoot, "world"), 0o755)
	_ = os.MkdirAll(libraryRoot, 0o755)
	server := seedWorldServer(t, store, dataRoot)
	makeRev := func(sha string) *storage.WorldRevision {
		archive, err := WriteFreshWorldArchive(map[string]string{"level-name": "world"}, "")
		if err != nil {
			t.Fatal(err)
		}
		archivePath := filepath.Join(libraryRoot, sha[:8]+".zip")
		if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
			t.Fatal(err)
		}
		return seedRevision(t, store, "wld_race", archivePath, sha)
	}
	r1 := makeRev("dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	r2 := makeRev("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	binder := NewBinder(store, logger.New(), nodestorage.NewResolver(nil), nil, &fakeLifecycle{}, libraryRoot)

	var wg sync.WaitGroup
	results := make([]error, 2)
	start := make(chan struct{})
	for i, rev := range []*storage.WorldRevision{r1, r2} {
		wg.Add(1)
		go func(i int, rev *storage.WorldRevision) {
			defer wg.Done()
			<-start
			_, err := binder.Bind(ctx, BindRequest{
				ServerID: server.ID, LevelName: "world", RevisionID: rev.ID,
				NodeID: "local", Generation: 1, RequestID: "race-" + rev.ID,
			})
			results[i] = err
		}(i, rev)
	}
	close(start)
	wg.Wait()
	for _, err := range results {
		if err != nil {
			t.Fatalf("concurrent bind/swap must serialize cleanly, got %v", err)
		}
	}
	// The admission gate serializes the two mutations; the second is a valid
	// swap of the binding created by the first. Exactly one row survives and
	// it points at one of the requested revisions - there is no overcommit and
	// no unique-index collision on (server_id, level_name).
	bindings, err := store.ListWorldBindings(ctx, server.ID, "")
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings = %+v, %v; want exactly one", bindings, err)
	}
	if bindings[0].RevisionID != r1.ID && bindings[0].RevisionID != r2.ID {
		t.Fatalf("binding points to unexpected revision %q", bindings[0].RevisionID)
	}
	ops, err := store.ListRecentServerOperations(ctx, server.ID, 10)
	if err != nil || len(ops) != 2 {
		t.Fatalf("operations = %+v, %v; want two journal rows", ops, err)
	}
	for _, op := range ops {
		if op.ActiveServer != nil {
			t.Fatalf("operation %s still holds a lock after success", op.ID)
		}
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
