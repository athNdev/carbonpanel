// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/worldlib"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

type worldTestLifecycle struct {
	running   bool
	stops     int
	starts    int
	failStart bool
}

func (f *worldTestLifecycle) IsRunning(context.Context, string) (bool, error) { return f.running, nil }
func (f *worldTestLifecycle) StopServer(context.Context, string) error {
	f.stops++
	f.running = false
	return nil
}
func (f *worldTestLifecycle) StartServer(context.Context, string) error {
	f.starts++
	if f.failStart {
		return errors.New("restart not observed")
	}
	f.running = true
	return nil
}

func newWorldRPCStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{
		Path: filepath.Join(t.TempDir(), "world.db"), AutoMigrate: true,
	}, Docker: config.DockerConfig{Host: "unix:///var/run/docker.sock"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedWorldRPC(t *testing.T, store *storage.Store, dataRoot, libraryRoot string, generation int64) (*storage.Server, *storage.WorldRevision) {
	t.Helper()
	ctx := context.Background()
	// A legacy local node id keeps the storage path local, while the Node row
	// supplies a non-default generation so the test proves real DB sourcing.
	node := &storage.Node{ID: "local", Name: "local", IsLocal: true, EndpointGeneration: generation, Enabled: true}
	if err := store.CreateNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	server := &storage.Server{ID: "srv-real", Name: "Real", NodeID: node.ID, DataPath: dataRoot, Status: storage.StatusStopped}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataRoot, "world"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(libraryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	archive, err := worldlib.WriteFreshWorldArchive(map[string]string{"level-name": "world"}, "")
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(libraryRoot, "rev.zip")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	sha := "4444444444444444444444444444444444444444444444444444444444444444"
	rev := &storage.WorldRevision{
		ID: worldlib.RevisionID(sha), WorldEntryID: "wld_rpc", SHA256: sha,
		SizeBytes: int64(len(archive)), EntryCount: 3, MCVersion: "1.21.4",
		ModLoader: "VANILLA", StoragePath: archivePath,
	}
	if err := store.PutWorldRevision(ctx, rev); err != nil {
		t.Fatal(err)
	}
	return server, rev
}

func newWorldRPCService(t *testing.T, store *storage.Store, libraryRoot string, life *worldTestLifecycle) *WorldService {
	t.Helper()
	svc := NewWorldService(store, &config.Config{}, logger.New(), nodestorage.NewResolver(nil), nil, libraryRoot)
	svc.SetBinder(worldlib.NewBinder(store, logger.New(), svc.resolver, nil, life, libraryRoot))
	return svc
}

// TestBindWorldUsesRealNodeGeneration proves a bind succeeds end to end at the
// RPC seam: the generation comes from the Node row, not a context stub.
func TestBindWorldUsesRealNodeGeneration(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	life := &worldTestLifecycle{}
	svc := newWorldRPCService(t, store, libraryRoot, life)

	out, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if out.Msg.Outcome != "success" || out.Msg.Binding == nil {
		t.Fatalf("outcome = %q, binding = %+v", out.Msg.Outcome, out.Msg.Binding)
	}
	if out.Msg.Binding.Generation != 7 {
		t.Fatalf("binding generation = %d, want 7 from the Node row", out.Msg.Binding.Generation)
	}
	binding, err := store.GetWorldBinding(ctx, server.ID, "world")
	if err != nil || binding.Generation != 7 {
		t.Fatalf("stored binding = %+v, %v", binding, err)
	}
}

func TestBindWorldReportsUnknownOutcomeInsteadOfRetryableError(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	life := &worldTestLifecycle{running: true, failStart: true}
	svc := newWorldRPCService(t, store, libraryRoot, life)

	out, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-unknown",
	}))
	if err != nil {
		t.Fatalf("an uncertain outcome must be reported, not returned as a retryable error: %v", err)
	}
	if out.Msg.Outcome != "unknown" {
		t.Fatalf("outcome = %q, want unknown", out.Msg.Outcome)
	}
	if out.Msg.OperationId == "" {
		t.Fatal("unknown outcome must carry the operation id for resolution")
	}
	active, err := store.ListActiveServerOperations(ctx)
	if err != nil || len(active) != 1 || active[0].ID != out.Msg.OperationId {
		t.Fatalf("active = %+v, %v; journal must keep its lock", active, err)
	}
}


func countWorldEvents(t *testing.T, store *storage.Store, event, serverID, requestID string) int64 {
	t.Helper()
	var count int64
	q := store.DB().Model(&storage.ActivityLog{}).Where("event = ? AND subject_id = ?", event, serverID)
	if requestID != "" {
		q = q.Where("request_id = ?", requestID)
	}
	if err := q.Count(&count).Error; err != nil {
		t.Fatalf("count events: %v", err)
	}
	return count
}

func TestBindWorldEmitsExactlyOneEventOnSuccessAndReplay(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	svc := newWorldRPCService(t, store, libraryRoot, &worldTestLifecycle{})
	req := connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-event-1",
	})
	if _, err := svc.BindWorld(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "rpc-bind-event-1"); got != 1 {
		t.Fatalf("events after first bind = %d, want 1", got)
	}
	// A replay of the same completed request must not emit a duplicate event.
	if _, err := svc.BindWorld(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "rpc-bind-event-1"); got != 1 {
		t.Fatalf("events after replay = %d, want 1", got)
	}
}

func TestBindWorldReplayEmitsMissingEventAfterCrashRecovery(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	svc := newWorldRPCService(t, store, libraryRoot, &worldTestLifecycle{})
	req := connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-recover-1",
	})
	if _, err := svc.BindWorld(ctx, req); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after journal completion but before the activity event
	// was durably recorded. A client retry of the same request must recover the
	// missing event exactly once.
	if err := store.DB().Where("event = ? AND request_id = ?", activity.EventWorldBind, "rpc-bind-recover-1").
		Delete(&storage.ActivityLog{}).Error; err != nil {
		t.Fatalf("delete event: %v", err)
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "rpc-bind-recover-1"); got != 0 {
		t.Fatalf("events after simulated crash = %d, want 0", got)
	}
	if _, err := svc.BindWorld(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "rpc-bind-recover-1"); got != 1 {
		t.Fatalf("events after recovery replay = %d, want 1", got)
	}
}

func TestBindWorldCancellationBeforeMutationEmitsNoEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	svc := newWorldRPCService(t, store, libraryRoot, &worldTestLifecycle{})
	_, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-cancel-1",
	}))
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "rpc-bind-cancel-1"); got != 0 {
		t.Fatalf("events after cancelled bind = %d, want 0", got)
	}
}

func TestBindWorldUncertainOutcomeEmitsNoSuccessEvent(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	life := &worldTestLifecycle{running: true, failStart: true}
	svc := newWorldRPCService(t, store, libraryRoot, life)

	out, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-unknown-event",
	}))
	if err != nil {
		t.Fatalf("an uncertain outcome must be reported, not returned as a retryable error: %v", err)
	}
	if out.Msg.Outcome != "unknown" {
		t.Fatalf("outcome = %q, want unknown", out.Msg.Outcome)
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "rpc-bind-unknown-event"); got != 0 {
		t.Fatalf("success events after uncertain outcome = %d, want 0", got)
	}
	// Keep the existing uncertain-outcome contract: the journal retains its lock.
	active, err := store.ListActiveServerOperations(ctx)
	if err != nil || len(active) != 1 || active[0].ID != out.Msg.OperationId {
		t.Fatalf("active = %+v, %v; journal must keep its lock", active, err)
	}
	_ = time.Now() // keep time import used only if needed by future edits
}


func TestUnbindWorldEmitsExactlyOneEventOnSuccess(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	svc := newWorldRPCService(t, store, libraryRoot, &worldTestLifecycle{})
	if _, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-before-unbind",
	})); err != nil {
		t.Fatal(err)
	}
	out, err := svc.UnbindWorld(ctx, connect.NewRequest(&v1.UnbindWorldRequest{
		ServerId: server.ID, LevelName: "world", RequestId: "rpc-unbind-event-1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Msg.Removed {
		t.Fatal("expected binding to be removed")
	}
	if got := countWorldEvents(t, store, activity.EventWorldUnbind, server.ID, "rpc-unbind-event-1"); got != 1 {
		t.Fatalf("events after unbind = %d, want 1", got)
	}
}
