// SPDX-License-Identifier: MPL-2.0
package worldstore

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
)

type worldZeros struct{}

func (worldZeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestAccessorLargeProcessBackupRestoreDeterministic(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "carbon-storage")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/carbon-storage")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build worker: %v %s", err, out)
	}
	open := func() nodestorage.Accessor {
		root := filepath.Join(t.TempDir(), "data")
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		acc, err := (&nodestorage.ProcessTransport{Bin: bin}).Open(context.Background(), nodestorage.NodeInfo{Name: "worker", StorageRoot: filepath.Dir(root)}, root)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = acc.Close() })
		return acc
	}
	acc := open()
	const size int64 = 130 << 20
	w, err := acc.BeginWrite(context.Background(), "world/region.mca", size)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.New()
	buf := make([]byte, storageworker.ChunkBytes)
	if _, err := io.CopyBuffer(io.MultiWriter(w, expected), io.LimitReader(worldZeros{}, size), buf); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	first := filepath.Join(t.TempDir(), "first.zip")
	second := filepath.Join(t.TempDir(), "second.zip")
	h1, err := BackupToAccessorContext(context.Background(), acc, first, nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := BackupToAccessorContext(context.Background(), acc, second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("unchanged world produced different manifest/archive hash")
	}
	local, id, _ := func() (*Local, Identity, string) {
		id := Identity{Server: "srv", Node: "node", Daemon: "daemon", Owner: "owner", Generation: 1}
		store, err := NewLocal(id, acc.Root(), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return store, store.Identity(), acc.Root()
	}()
	defer func() { _ = local.Close() }()
	localHash, err := local.BackupTo(id, filepath.Join(t.TempDir(), "local.zip"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if localHash != h1 {
		t.Fatal("local and process backups disagree")
	}
	dest := open()
	if err := dest.WriteFile("old", []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := RestoreFromAccessorContext(context.Background(), dest, first); err != nil {
		t.Fatal(err)
	}
	r, err := dest.OpenRead(context.Background(), "world/region.mca")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got := sha256.New()
	if n, err := io.CopyBuffer(got, r, buf); err != nil || n != size {
		t.Fatalf("restore %d %v", n, err)
	}
	if string(expected.Sum(nil)) != string(got.Sum(nil)) {
		t.Fatal("restored world hash changed")
	}
	if _, err := dest.Stat("old"); !os.IsNotExist(err) {
		t.Fatalf("old entry survived or error identity lost: %v", err)
	}
}

type cancelWriteAccessor struct {
	nodestorage.Accessor
	cancel context.CancelFunc
}

func (a cancelWriteAccessor) BeginWrite(ctx context.Context, rel string, limit int64) (storageworker.AtomicWriter, error) {
	w, err := a.Accessor.BeginWrite(ctx, rel, limit)
	if err != nil {
		return nil, err
	}
	return cancelAfterWrite{AtomicWriter: w, cancel: a.cancel}, nil
}

type cancelAfterWrite struct {
	storageworker.AtomicWriter
	cancel context.CancelFunc
}

func (w cancelAfterWrite) Write(p []byte) (int, error) {
	n, err := w.AtomicWriter.Write(p)
	w.cancel()
	return n, err
}

func TestAccessorInterruptedRestoreRetainsRecoveryAndOldWorld(t *testing.T) {
	source, err := nodestorage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	_ = source.WriteFile("world", make([]byte, 1<<20))
	archive := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := BackupToAccessor(source, archive, nil); err != nil {
		t.Fatal(err)
	}
	dest, err := nodestorage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dest.Close() }()
	_ = dest.WriteFile("world", []byte("old"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := RestoreFromAccessorContext(ctx, cancelWriteAccessor{Accessor: dest, cancel: cancel}, archive); !errors.Is(err, context.Canceled) {
		t.Fatalf("restore error %v", err)
	}
	b, err := dest.ReadFile("world")
	if err != nil || string(b) != "old" {
		t.Fatalf("old world changed %q %v", b, err)
	}
	if _, err := dest.Stat(restoreStageRel); err != nil {
		t.Fatalf("recovery stage lost: %v", err)
	}
	if err := RestoreFromAccessor(dest, archive); err == nil {
		t.Fatal("restart silently erased prior recovery")
	}
}

type failPromotionAccessor struct{ nodestorage.Accessor }

func (a failPromotionAccessor) Rename(old, new string) error {
	if strings.HasPrefix(old, restoreStageRel+"/") {
		return errors.New("injected promotion failure")
	}
	return a.Accessor.Rename(old, new)
}

func TestAccessorPromotionFailurePreservesBothRecoveryTrees(t *testing.T) {
	source, err := nodestorage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	_ = source.WriteFile("world", []byte("new"))
	archive := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := BackupToAccessor(source, archive, nil); err != nil {
		t.Fatal(err)
	}
	dest, err := nodestorage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dest.Close() }()
	_ = dest.WriteFile("world", []byte("old"))
	if err := RestoreFromAccessor(failPromotionAccessor{dest}, archive); err == nil {
		t.Fatal("missing promotion failure")
	}
	for rel, want := range map[string]string{restoreOldRel + "/world": "old", restoreStageRel + "/world": "new"} {
		got, err := dest.ReadFile(rel)
		if err != nil || string(got) != want {
			t.Fatalf("recovery %s=%q %v", rel, got, err)
		}
	}
	if err := RestoreFromAccessor(dest, archive); err == nil {
		t.Fatal("retry overwrote recovery")
	}
}
