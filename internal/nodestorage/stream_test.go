// SPDX-License-Identifier: MPL-2.0
package nodestorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/storageworker"
)

func TestStreamTransactionsPreserveTarget(t *testing.T) {
	acc, root := testAccessor(t)
	if err := acc.WriteFile("target", []byte("old")); err != nil {
		t.Fatal(err)
	}
	committed, err := acc.BeginWrite(context.Background(), "target", 100)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = committed.Write([]byte("replaced"))
	if err = committed.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = committed.Close()
	if b, err := acc.ReadFile("target"); err != nil || string(b) != "replaced" {
		t.Fatalf("replacement failed: %q %v", b, err)
	}
	if err := acc.WriteFile("target", []byte("old")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w, err := acc.BeginWrite(ctx, "target", 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := w.Write([]byte("more")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := acc.ReadFile("target")
	if err != nil || string(b) != "old" {
		t.Fatalf("old target lost: %q %v", b, err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".carbon-write-*.partial"))
	if len(matches) != 0 {
		t.Fatalf("abort leaked %v", matches)
	}

	if err := acc.MkdirAll("blocked"); err != nil {
		t.Fatal(err)
	}
	w, err = acc.BeginWrite(context.Background(), "blocked", 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("recover")); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(); err == nil || !strings.Contains(err.Error(), "recovery staging") {
		t.Fatalf("missing recovery error: %v", err)
	}
	_ = w.Close()
	matches, _ = filepath.Glob(filepath.Join(root, ".carbon-write-*.partial"))
	if len(matches) != 1 {
		t.Fatalf("failed commit lost recovery: %v", matches)
	}
	if fi, err := os.Stat(filepath.Join(root, "blocked")); err != nil || !fi.IsDir() {
		t.Fatalf("blocked target changed: %v", err)
	}
	recovered, err := os.ReadFile(matches[0])
	if err != nil || string(recovered) != "recover" {
		t.Fatalf("recovery bytes %q %v", recovered, err)
	}
}

func TestStreamLimitAndPartialWriteNeverPromote(t *testing.T) {
	acc, _ := testAccessor(t)
	_ = acc.WriteFile("target", []byte("old"))
	w, err := acc.BeginWrite(context.Background(), "target", 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("four")); err == nil {
		t.Fatal("missing limit")
	}
	if err = w.Commit(); err == nil {
		t.Fatal("promoted rejected write")
	}
	_ = w.Close()
	w, err = acc.BeginWrite(context.Background(), "target", 100)
	if err != nil {
		t.Fatal(err)
	}
	local := w.(*localWriter)
	_ = local.file.Close()
	if _, err = w.Write([]byte("partial")); err == nil {
		t.Fatal("missing write failure")
	}
	if err = w.Commit(); err == nil {
		t.Fatal("promoted partial write")
	}
	_ = w.Close()
	b, err := acc.ReadFile("target")
	if err != nil || string(b) != "old" {
		t.Fatalf("old target lost: %q %v", b, err)
	}
}

type repeatedBytes struct{}

type shortStreamFile struct{ streamFile }

func (f shortStreamFile) Write(p []byte) (int, error) {
	n, err := f.streamFile.Write(p[:len(p)/2])
	if err != nil {
		return n, err
	}
	return n, io.ErrShortWrite
}

func TestStreamPartialFilesystemWritePreservesTarget(t *testing.T) {
	acc, root := testAccessor(t)
	_ = acc.WriteFile("target", []byte("old"))
	w, err := acc.BeginWrite(context.Background(), "target", 100)
	if err != nil {
		t.Fatal(err)
	}
	local := w.(*localWriter)
	local.file = shortStreamFile{local.file}
	if n, err := w.Write([]byte("new-data")); n != 4 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("partial write n=%d err=%v", n, err)
	}
	if err := w.Commit(); err == nil {
		t.Fatal("partial write promoted")
	}
	_ = w.Close()
	if b, err := acc.ReadFile("target"); err != nil || string(b) != "old" {
		t.Fatalf("target changed %q %v", b, err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".carbon-write-*.partial"))
	if len(matches) != 1 {
		t.Fatalf("recovery %v", matches)
	}
	if b, err := os.ReadFile(matches[0]); err != nil || string(b) != "new-" {
		t.Fatalf("partial recovery %q %v", b, err)
	}
}

func (repeatedBytes) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(i % 251)
	}
	return len(p), nil
}

// This crosses the former 129 MiB JSON-line ceiling with a real worker binary.
// The payload is generated incrementally; heap sampling bounds the controller
// live heap, while per-frame protocol tests bound the worker's binary buffers.
func TestStreamLargeProcessAndLocalParity(t *testing.T) {
	const size int64 = 130 << 20
	bin := carbonStorageBin(t)
	for _, kind := range []string{"local", "shared", "process"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var acc Accessor
			var err error
			if kind == "local" {
				acc, err = NewLocal(t.TempDir())
			} else if kind == "shared" {
				root := t.TempDir()
				acc, err = NewResolver(nil).ForNode(ctx, NodeInfo{StorageRoot: "/remote-root", ControllerStorageRoot: root}, filepath.Join("controller", "srv"))
			} else {
				node := NodeInfo{Name: "worker", StorageRoot: t.TempDir()}
				_ = os.MkdirAll(nodeDataRoot(node, "srv"), 0700)
				acc, err = (&ProcessTransport{Bin: bin}).Open(ctx, node, "srv")
			}
			if err != nil {
				t.Fatal(err)
			}
			defer acc.Close()
			runtime.GC()
			var baseline runtime.MemStats
			runtime.ReadMemStats(&baseline)
			w, err := acc.BeginWrite(ctx, "large.bin", size)
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.New()
			buf := make([]byte, storageworker.ChunkBytes)
			n, err := io.CopyBuffer(io.MultiWriter(w, hash), io.LimitReader(repeatedBytes{}, size), buf)
			if err != nil || n != size {
				t.Fatalf("write %d: %v", n, err)
			}
			if err = w.Commit(); err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			r, err := acc.OpenRead(ctx, "large.bin")
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			got := sha256.New()
			var peak uint64
			for {
				n, err := r.Read(buf)
				if n > 0 {
					_, _ = got.Write(buf[:n])
				}
				var mem runtime.MemStats
				runtime.ReadMemStats(&mem)
				if mem.HeapAlloc > peak {
					peak = mem.HeapAlloc
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(hash.Sum(nil), got.Sum(nil)) {
				t.Fatal("stream digest changed")
			}
			if peak > baseline.HeapAlloc+(48<<20) {
				t.Fatalf("controller heap grew beyond fixed frame allowance: baseline %d peak %d", baseline.HeapAlloc, peak)
			}
			t.Logf("130 MiB roundtrip heap baseline=%d peak=%d", baseline.HeapAlloc, peak)
			if _, err := acc.ReadFile("large.bin"); err == nil {
				t.Fatal("whole-file helper accepted large file")
			}
			if kind == "process" {
				if _, err := CreateZip(ctx, acc, []string{"large.bin"}, "large.zip", false); err != nil {
					t.Fatal(err)
				}
				if _, err := ExtractZip(ctx, acc, "large.zip", "unpacked", nil); err != nil {
					t.Fatal(err)
				}
				unpacked, err := acc.OpenRead(ctx, "unpacked/large.bin")
				if err != nil {
					t.Fatal(err)
				}
				h := sha256.New()
				if _, err = io.CopyBuffer(h, unpacked, buf); err != nil {
					t.Fatal(err)
				}
				_ = unpacked.Close()
				if !bytes.Equal(hash.Sum(nil), h.Sum(nil)) {
					t.Fatal("large archive roundtrip changed data")
				}
			}
			_ = acc.WriteFile("target", []byte("old"))
			w, err = acc.BeginWrite(ctx, "target", size)
			if err != nil {
				t.Fatal(err)
			}
			_, err = w.Write(buf)
			if err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			old, err := acc.ReadFile("target")
			if err != nil || string(old) != "old" {
				t.Fatalf("abort changed target: %q %v", old, err)
			}
		})
	}
}
