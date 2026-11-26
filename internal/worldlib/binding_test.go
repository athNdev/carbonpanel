// SPDX-License-Identifier: MPL-2.0
package worldlib

import (
	"archive/zip"
	"bytes"
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

func (f *fakeLifecycle) BeginTransition(string) (func(), error) {
	return func() {}, nil
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
	ctx := context.Background()
	store := newWorldTestStore(t)
	root := t.TempDir()
	server := seedWorldServer(t, store, filepath.Join(root, "data"))
	node := &storage.Node{Name: "Remote daemon", Host: "tcp://remote.example:2376", Enabled: true}
	if err := store.CreateNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	server.NodeID = node.ID
	if err := store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	rev := seedRevision(t, store, "wld_3", filepath.Join(root, "rev.zip"), "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	binder := NewBinder(store, logger.New(), nil, nil, &fakeLifecycle{}, root)
	request := BindRequest{ServerID: server.ID, LevelName: "world", RevisionID: rev.ID,
		NodeID: node.ID, Generation: 1, RequestID: "remote"}
	_, err := binder.Bind(ctx, request)
	if !errors.Is(err, ErrRemoteUnavailable) {
		t.Fatalf("want ErrRemoteUnavailable, got %v", err)
	}
	request.NodeID = "local"
	_, err = binder.Bind(ctx, request)
	if !errors.Is(err, ErrRemoteUnavailable) {
		t.Fatalf("remote bind must not accept a local node override, got %v", err)
	}
}

func TestBindAndRecoverOnRegisteredLocalNode(t *testing.T) {
	ctx := context.Background()
	store := newWorldTestStore(t)
	server, rev, dataRoot, libraryRoot := seedMultiFamilyFixture(t, store, []string{"world"})
	node := &storage.Node{
		Name: "Registered local daemon", Host: "unix:///var/run/docker.sock",
		Enabled: true, IsLocal: true, EndpointGeneration: 3,
	}
	if err := store.CreateNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	server.NodeID = node.ID
	if err := store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	lifecycle := &fakeLifecycle{running: true}
	binder := NewBinder(store, logger.New(), nodestorage.NewResolver(nil), nil, lifecycle, libraryRoot)
	request := BindRequest{
		ServerID: server.ID, LevelName: "world", RevisionID: rev.ID, Window: "now",
		NodeID: node.ID, Generation: 3, RequestID: "registered-local",
	}
	result, err := binder.Bind(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Binding == nil || !result.Binding.MatchesOwner(node.ID, 3) ||
		lifecycle.stops != 1 || lifecycle.starts != 1 {
		t.Fatalf("bind did not restart and persist the owning node: %+v, lifecycle=%+v", result, lifecycle)
	}
	family := filepath.Join(dataRoot, "world")
	held := filepath.Join(dataRoot, ".world-library", "old-registered-node", "world")
	if err := os.MkdirAll(filepath.Dir(held), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(family, held); err != nil {
		t.Fatal(err)
	}
	if _, err := binder.Recover(ctx, server.ID, dataRoot, node.ID, 2); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("recovery should refuse a stale node generation, got %v", err)
	}
	summary, err := binder.Recover(ctx, server.ID, dataRoot, node.ID, 3)
	if err != nil || !strings.Contains(summary, "restored 1") {
		t.Fatalf("registered-node recovery = %q, %v", summary, err)
	}
	got, err := os.ReadFile(filepath.Join(family, "level.dat"))
	if err != nil || string(got) != "new-world" {
		t.Fatalf("recovered level.dat = %q, %v; want published world bytes", got, err)
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

// faultyAccessor injects I/O faults at the real node-accessor boundary. The
// predicate receives the same relative paths production uses, so tests can
// target a promotion or a restore without changing production behaviour.
type faultyAccessor struct {
	nodestorage.Accessor
	failRename      func(oldRel, newRel string) bool
	loseRenameReply func(oldRel, newRel string) bool
}

func (f *faultyAccessor) Rename(oldRel, newRel string) error {
	if f.failRename != nil && f.failRename(oldRel, newRel) {
		return errors.New("injected rename fault")
	}
	if err := f.Accessor.Rename(oldRel, newRel); err != nil {
		return err
	}
	if f.loseRenameReply != nil && f.loseRenameReply(oldRel, newRel) {
		return errors.New("injected lost rename reply")
	}
	return nil
}

func multiFamilyTestArchive(t *testing.T, families []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, family := range families {
		w, err := zw.Create(family + "/level.dat")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("new-" + family)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func seedMultiFamilyFixture(t *testing.T, store *storage.Store, families []string) (*storage.Server, *storage.WorldRevision, string, string) {
	t.Helper()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	libraryRoot := filepath.Join(root, "library")
	for _, family := range families {
		if err := os.MkdirAll(filepath.Join(dataRoot, family), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataRoot, family, "level.dat"), []byte("old-"+family), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(libraryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(libraryRoot, "rev.zip")
	if err := os.WriteFile(archivePath, multiFamilyTestArchive(t, families), 0o644); err != nil {
		t.Fatal(err)
	}
	server := seedWorldServer(t, store, dataRoot)
	rev := seedRevision(t, store, "wld_multi", archivePath, strings.Repeat("9", 64))
	return server, rev, dataRoot, libraryRoot
}

func libraryEntryWithPrefix(t *testing.T, dataRoot, prefix string) string {
	t.Helper()
	lib := filepath.Join(dataRoot, ".world-library")
	entries, err := os.ReadDir(lib)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			return filepath.Join(lib, entry.Name())
		}
	}
	return ""
}

func TestBindMultiFamilyPartialPromotionRollbackPreservesEveryWorld(t *testing.T) {
	ctx := context.Background()
	store := newWorldTestStore(t)
	// Sorted order is world, world_end, world_nether. Fail the last promotion
	// so two families are already on the new revision when compensation starts.
	families := []string{"world", "world_end", "world_nether"}
	server, rev, dataRoot, libraryRoot := seedMultiFamilyFixture(t, store, families)
	binder := NewBinder(store, logger.New(), nodestorage.NewResolver(nil), nil, &fakeLifecycle{}, libraryRoot)
	binder.SetAccessorDecorator(func(acc nodestorage.Accessor) nodestorage.Accessor {
		return &faultyAccessor{Accessor: acc, failRename: func(oldRel, newRel string) bool {
			return newRel == "world_nether" && strings.Contains(oldRel, "stage-")
		}}
	})

	_, err := binder.Bind(ctx, BindRequest{
		ServerID: server.ID, LevelName: "world", RevisionID: rev.ID,
		NodeID: "local", Generation: 1, RequestID: "rollback-multi",
	})
	if err == nil {
		t.Fatal("expected the injected promotion failure")
	}
	if errors.Is(err, ErrUncertainOutcome) {
		t.Fatalf("expected a proven rollback, got uncertain outcome: %v", err)
	}

	// Every family is back on its retained old bytes; none was dropped.
	for _, family := range families {
		got, readErr := os.ReadFile(filepath.Join(dataRoot, family, "level.dat"))
		if readErr != nil || string(got) != "old-"+family {
			t.Errorf("family %s after rollback = %q, %v; want old-%s", family, string(got), readErr, family)
		}
	}
	bindings, err := store.ListWorldBindings(ctx, server.ID, "")
	if err != nil || len(bindings) != 0 {
		t.Fatalf("bindings after proven rollback = %+v, %v; want none", bindings, err)
	}
	// Temporary trees are released only after bytes and binding were proven.
	entries, err := os.ReadDir(filepath.Join(dataRoot, ".world-library"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary trees survived a proven rollback: %v", entries)
	}
	active, err := store.ListActiveServerOperations(ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("active operations after failed publish = %+v, %v", active, err)
	}
}

func TestBindRollbackRestoreFailureKeepsOldDataAndUncertainBinding(t *testing.T) {
	ctx := context.Background()
	store := newWorldTestStore(t)
	families := []string{"world", "world_end", "world_nether"}
	server, rev, dataRoot, libraryRoot := seedMultiFamilyFixture(t, store, families)
	binder := NewBinder(store, logger.New(), nodestorage.NewResolver(nil), nil, &fakeLifecycle{}, libraryRoot)
	binder.SetAccessorDecorator(func(acc nodestorage.Accessor) nodestorage.Accessor {
		return &faultyAccessor{Accessor: acc, failRename: func(oldRel, newRel string) bool {
			// Trigger compensation, then fail one retained-family restore.
			if newRel == "world_nether" && strings.Contains(oldRel, "stage-") {
				return true
			}
			return strings.Contains(oldRel, "old-") && strings.HasSuffix(oldRel, "/world_end")
		}}
	})

	_, err := binder.Bind(ctx, BindRequest{
		ServerID: server.ID, LevelName: "world", RevisionID: rev.ID,
		NodeID: "local", Generation: 1, RequestID: "rollback-restore-io",
	})
	if !errors.Is(err, ErrUncertainOutcome) {
		t.Fatalf("ambiguous restore must report uncertain outcome, got %v", err)
	}
	oldRoot := libraryEntryWithPrefix(t, dataRoot, "old-")
	if oldRoot == "" {
		t.Fatal("retained old tree was deleted on ambiguous I/O")
	}
	for _, family := range families {
		// A family restored before the injected failure is back in live; the
		// rest stay in the retained tree. Old bytes must exist in one place.
		got, readErr := os.ReadFile(filepath.Join(dataRoot, family, "level.dat"))
		if readErr != nil || string(got) != "old-"+family {
			got, readErr = os.ReadFile(filepath.Join(oldRoot, family, "level.dat"))
			if readErr != nil || string(got) != "old-"+family {
				t.Errorf("old family %s lost on ambiguous restore: live=%q old=%q err=%v", family, string(got), string(got), readErr)
			}
		}
	}
	if q := libraryEntryWithPrefix(t, dataRoot, "quarantine-"); q == "" {
		t.Fatal("quarantined new bytes were deleted on ambiguous I/O")
	}
	binding, err := store.GetWorldBinding(ctx, server.ID, "world")
	if err != nil || binding.RevisionID != rev.ID {
		t.Fatalf("binding must remain on the new revision until bytes are proven, got %+v, %v", binding, err)
	}
	active, err := store.ListActiveServerOperations(ctx)
	if err != nil || len(active) != 1 {
		t.Fatalf("journal must keep its lock on ambiguity, active = %+v, %v", active, err)
	}
}

func TestBindRollbackBindingRefusalKeepsOldDataAndUncertainBinding(t *testing.T) {
	ctx := context.Background()
	store := newWorldTestStore(t)
	families := []string{"world", "world_end", "world_nether"}
	server, rev, dataRoot, libraryRoot := seedMultiFamilyFixture(t, store, families)
	binder := NewBinder(store, logger.New(), nodestorage.NewResolver(nil), nil, &fakeLifecycle{}, libraryRoot)
	binder.SetAccessorDecorator(func(acc nodestorage.Accessor) nodestorage.Accessor {
		return &faultyAccessor{Accessor: acc, failRename: func(oldRel, newRel string) bool {
			return newRel == "world_nether" && strings.Contains(oldRel, "stage-")
		}}
	})
	called := false
	binder.rollbackBindingHook = func() error {
		called = true
		return errors.New("binding store unavailable")
	}

	_, err := binder.Bind(ctx, BindRequest{
		ServerID: server.ID, LevelName: "world", RevisionID: rev.ID,
		NodeID: "local", Generation: 1, RequestID: "rollback-binding-refusal",
	})
	if !called {
		t.Fatal("rollback binding decision was not attempted")
	}
	if !errors.Is(err, ErrUncertainOutcome) {
		t.Fatalf("refused binding restore must be uncertain, got %v", err)
	}
	// Bytes were proven, but the row was not: the new binding still stands and
	// the quarantined new bytes remain for operator inspection.
	binding, err := store.GetWorldBinding(ctx, server.ID, "world")
	if err != nil || binding.RevisionID != rev.ID {
		t.Fatalf("binding must not be reported rolled back, got %+v, %v", binding, err)
	}
	if q := libraryEntryWithPrefix(t, dataRoot, "quarantine-"); q == "" {
		t.Fatal("quarantine removed before the binding decision was durable")
	}
	for _, family := range families {
		got, readErr := os.ReadFile(filepath.Join(dataRoot, family, "level.dat"))
		if readErr != nil || string(got) != "old-"+family {
			t.Errorf("old family %s lost after refused binding restore: %q, %v", family, string(got), readErr)
		}
	}
}

func TestRecoverPreservesQuarantineAndRestoresOnlyMissingOldFamily(t *testing.T) {
	ctx := context.Background()
	store := newWorldTestStore(t)
	dataRoot := filepath.Join(t.TempDir(), "data")
	oldFamily := filepath.Join(dataRoot, ".world-library", "old-drill", "world")
	quarantineFamily := filepath.Join(dataRoot, ".world-library", "quarantine-drill", "world")
	if err := os.MkdirAll(oldFamily, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldFamily, "level.dat"), []byte("old-world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(quarantineFamily, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(quarantineFamily, "level.dat"), []byte("new-world"), 0o644); err != nil {
		t.Fatal(err)
	}
	binder := NewBinder(store, logger.New(), nodestorage.NewResolver(nil), nil, &fakeLifecycle{}, t.TempDir())
	summary, err := binder.Recover(ctx, "srv-1", dataRoot, "local", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "restored 1") || !strings.Contains(summary, "preserved 1") {
		t.Fatalf("unexpected recovery summary %q", summary)
	}
	got, err := os.ReadFile(filepath.Join(dataRoot, "world", "level.dat"))
	if err != nil || string(got) != "old-world" {
		t.Fatalf("old family not restored: %q, %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(quarantineFamily, "level.dat"))
	if err != nil || string(got) != "new-world" {
		t.Fatalf("quarantine was not preserved for operator inspection: %q, %v", got, err)
	}
}

func TestBindMultiFamilyLostRenameReplyPreservesRecoveryAndRefusesRetry(t *testing.T) {
	for _, transition := range []string{"retain", "promote"} {
		t.Run(transition, func(t *testing.T) {
			ctx := context.Background()
			store := newWorldTestStore(t)
			families := []string{"world", "world_nether"}
			server, rev, dataRoot, libraryRoot := seedMultiFamilyFixture(t, store, families)
			binder := NewBinder(store, logger.New(), nodestorage.NewResolver(nil), nil, &fakeLifecycle{}, libraryRoot)
			binder.SetAccessorDecorator(func(acc nodestorage.Accessor) nodestorage.Accessor {
				return &faultyAccessor{Accessor: acc, loseRenameReply: func(src, dst string) bool {
					if transition == "retain" {
						return src == "world_nether" && strings.Contains(dst, "old-")
					}
					return dst == "world_nether" && strings.Contains(src, "stage-")
				}}
			})
			req := BindRequest{ServerID: server.ID, LevelName: "world", RevisionID: rev.ID, RequestID: "lost-reply"}
			first, err := binder.Bind(ctx, req)
			if !errors.Is(err, ErrUncertainOutcome) || first == nil || first.OperationID == "" {
				t.Fatalf("outcome = %+v, %v; want journaled uncertainty", first, err)
			}
			// A fresh binder is used on startup, with the same durable database.
			restarted := NewBinder(store, logger.New(), nodestorage.NewResolver(nil), nil, &fakeLifecycle{}, libraryRoot)
			_, err = restarted.Recover(ctx, server.ID, dataRoot, "local", 1)
			if !errors.Is(err, ErrUncertainOutcome) {
				t.Fatalf("startup recovery = %v; want an unresolved operation", err)
			}
			req.RequestID = "retry-lost-reply"
			retry, err := restarted.Bind(ctx, req)
			if !errors.Is(err, ErrUncertainOutcome) || retry == nil || retry.OperationID != first.OperationID {
				t.Fatalf("retry = %+v, %v; want original unresolved operation", retry, err)
			}
			oldRoot := libraryEntryWithPrefix(t, dataRoot, "old-")
			for _, family := range families {
				got, err := os.ReadFile(filepath.Join(oldRoot, family, "level.dat"))
				if err != nil || string(got) != "old-"+family {
					t.Fatalf("retained %s = %q, %v", family, got, err)
				}
			}
		})
	}
}

func TestBindPreservesOldFamiliesUntilJournalCompletion(t *testing.T) {
	ctx := context.Background()
	store := newWorldTestStore(t)
	families := []string{"world", "world_nether"}
	server, rev, dataRoot, libraryRoot := seedMultiFamilyFixture(t, store, families)
	// Force the journal update to fail after the storage mutation has finished.
	if err := store.DB().Exec(`CREATE TRIGGER refuse_world_completion BEFORE UPDATE ON server_operations
		WHEN NEW.phase = 'complete' BEGIN SELECT RAISE(ABORT, 'injected journal write failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	binder := NewBinder(store, logger.New(), nodestorage.NewResolver(nil), nil, &fakeLifecycle{}, libraryRoot)
	req := BindRequest{ServerID: server.ID, LevelName: "world", RevisionID: rev.ID, RequestID: "journal-write-failure"}
	result, err := binder.Bind(ctx, req)
	if !errors.Is(err, ErrUncertainOutcome) || result == nil || result.Outcome != "unknown" {
		t.Fatalf("bind = %+v, %v; want unknown", result, err)
	}
	op, err := store.GetServerOperation(ctx, result.OperationID)
	if err != nil || op.ActiveServer == nil || op.Phase != storage.ServerOperationExecuting {
		t.Fatalf("operation = %+v, %v; want active executing", op, err)
	}
	oldRoot := libraryEntryWithPrefix(t, dataRoot, "old-")
	for _, family := range families {
		got, err := os.ReadFile(filepath.Join(oldRoot, family, "level.dat"))
		if err != nil || string(got) != "old-"+family {
			t.Fatalf("retained %s = %q, %v", family, got, err)
		}
	}
}
