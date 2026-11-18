// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

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
