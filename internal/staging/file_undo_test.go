// SPDX-License-Identifier: MPL-2.0
package staging

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/google/uuid"
)

func TestLargeStagedModpackAndUndoUseBoundedStreams(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "large-pack", Name: "large-pack", DataPath: data, Status: storage.StatusStopped}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	const size int64 = 130 << 20
	source := t.TempDir()
	f, err := os.Create(filepath.Join(source, "large.jar"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	live := filepath.Join(data, "large.jar")
	old, err := os.Create(live)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = old.Write([]byte{1})
	if err := old.Truncate(size); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	var peak atomic.Uint64
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak.Load() {
					peak.Store(m.HeapAlloc)
				}
			}
		}
	}()
	defer func() { close(done); <-stopped }()
	row, err := StageModpackTree(ctx, store, server, source, "large pack")
	if err != nil {
		t.Fatal(err)
	}
	acc, err := nodestorage.NewLocal(data)
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	snaps, err := commitFiles(ctx, acc, []storage.StagedContentChange{*row})
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 {
		t.Fatalf("snapshots %d", len(snaps))
	}
	if _, err := acc.Stat(snaps[0].recoveryRoot + "/manifest.json"); err != nil {
		t.Fatal("recovery manifest missing", err)
	}
	if err := restoreFiles(ctx, acc, snaps); err != nil {
		t.Fatal(err)
	}
	r, err := acc.OpenRead(ctx, "large.jar")
	if err != nil {
		t.Fatal(err)
	}
	var head [1]byte
	if _, err := r.Read(head[:]); err != nil || head[0] != 1 {
		t.Fatalf("old file not restored: %v %v", head, err)
	}
	_ = r.Close()
	if _, err := acc.Stat(snaps[0].recoveryRoot); !os.IsNotExist(err) {
		t.Fatalf("successful rollback retained owned undo: %v", err)
	}
	if peak.Load() > baseline.HeapAlloc+(48<<20) {
		t.Fatalf("undo retained file-sized heap: baseline %d peak %d", baseline.HeapAlloc, peak.Load())
	}
	t.Logf("130 MiB stage/apply/undo heap baseline=%d peak=%d", baseline.HeapAlloc, peak.Load())
}

func TestCorruptUndoRefusesPromotionAndPreservesRecovery(t *testing.T) {
	acc, err := nodestorage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	_ = acc.WriteFile("target", []byte("old"))
	_ = acc.WriteFile("stage", []byte("new"))
	rows := []storage.StagedContentChange{{BatchID: uuid.NewString(), Kind: storage.StagedKindFile, Action: storage.StagedActionReplace, TargetPath: "target", StagePath: "stage"}}
	snaps, err := commitFiles(context.Background(), acc, rows)
	if err != nil {
		t.Fatal(err)
	}
	_ = acc.WriteFile(snaps[0].backup, []byte("bad"))
	if err := restoreFiles(context.Background(), acc, snaps); err == nil {
		t.Fatal("corrupt undo accepted")
	}
	if got, err := acc.ReadFile("target"); err != nil || string(got) != "new" {
		t.Fatalf("corrupt undo replaced target: %q %v", got, err)
	}
	if _, err := acc.Stat(snaps[0].recoveryRoot + "/manifest.json"); err != nil {
		t.Fatal("failed rollback lost recovery", err)
	}
	if _, err := commitFiles(context.Background(), acc, rows); err == nil {
		t.Fatal("retry erased unresolved recovery")
	}
}
