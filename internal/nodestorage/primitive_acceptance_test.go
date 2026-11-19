package nodestorage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/athNdev/carbon-panel/pkg/files"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

func dockerLogFrame(stream byte, payload string) []byte {
	frame := make([]byte, 8+len(payload))
	frame[0] = stream
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[8:], payload)
	return frame
}

func tarGzArchive(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func runPublicStoragePrimitives(t *testing.T, root string) {
	t.Helper()

	resolved, err := files.ResolveInRoot(root, "world/level.dat")
	if err != nil {
		t.Fatalf("ResolveInRoot: %v", err)
	}
	if !files.WithinReal(root, resolved) {
		t.Fatalf("resolved path must be within root: %s", resolved)
	}

	target := filepath.Join(root, "server.properties")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := files.NewAtomicWriter(target, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("hello")); !errors.Is(err, files.ErrFileTooLarge) {
		t.Fatalf("overflow: %v", err)
	}
	_ = w.Abort()
	if body, err := os.ReadFile(target); err != nil || string(body) != "old" {
		t.Fatalf("aborted oversized atomic write must preserve target for retry: %q %v", body, err)
	}

	w, err = files.NewAtomicWriter(target, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("motd=node")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != "motd=node" {
		t.Fatalf("committed atomic write: %q %v", body, err)
	}

	if err := os.MkdirAll(filepath.Join(root, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugins", "a.jar"), []byte("jar"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := files.CopyFile(filepath.Join(root, "plugins", "a.jar"), filepath.Join(root, "plugins", "b.jar")); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	sum, err := files.SHA256File(filepath.Join(root, "plugins", "b.jar"))
	if err != nil || sum == "" {
		t.Fatalf("SHA256File: %q %v", sum, err)
	}

	srcWorld := filepath.Join(root, "src-world")
	if err := os.MkdirAll(srcWorld, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcWorld, "level.dat"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(root, "world-backup.zip")
	if _, err := files.CreateZipArchive([]string{"level.dat"}, srcWorld, zipPath, true); err != nil {
		t.Fatalf("CreateZipArchive: %v", err)
	}
	if err := files.VerifyZipArchive(zipPath); err != nil {
		t.Fatalf("VerifyZipArchive: %v", err)
	}

	tarDest := filepath.Join(root, "tar-out")
	if err := os.MkdirAll(tarDest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := files.ExtractTarGz(bytes.NewReader(tarGzArchive(t, "config/paper.yml", "ok")), tarDest); err != nil {
		t.Fatalf("ExtractTarGz: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(tarDest, "config", "paper.yml")); err != nil || string(body) != "ok" {
		t.Fatalf("extracted tar.gz file: %q %v", body, err)
	}

	worldRoot := filepath.Join(root, "world-data")
	if err := os.MkdirAll(worldRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worldRoot, "precious.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(root, "broken.zip")
	if err := os.WriteFile(broken, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := files.ExtractArchiveAtomic(context.Background(), broken, worldRoot); err == nil {
		t.Fatal("broken archive must fail")
	}
	if body, err := os.ReadFile(filepath.Join(worldRoot, "precious.txt")); err != nil || string(body) != "keep" {
		t.Fatalf("failed/interrupted restore must preserve world data for retry: %q %v", body, err)
	}
	if err := os.WriteFile(filepath.Join(worldRoot, "stale.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := files.ExtractArchiveAtomic(context.Background(), zipPath, worldRoot); err != nil {
		t.Fatalf("retry ExtractArchiveAtomic restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worldRoot, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("successful restore must replace stale world data, stat err=%v", err)
	}
	if body, err := os.ReadFile(filepath.Join(worldRoot, "level.dat")); err != nil || string(body) != "new" {
		t.Fatalf("restored world file: %q %v", body, err)
	}
}

func TestPublicFileArchiveLogPrimitivesAcrossLocalAndNodeBackedFixtures(t *testing.T) {
	var mux bytes.Buffer
	mux.Write(dockerLogFrame(1, "hello\n"))
	mux.Write(dockerLogFrame(2, "warn\n"))
	lines, err := logger.ReadDockerLogLines(&mux)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Stream != "stdout" || lines[0].Text != "hello" || lines[1].Stream != "stderr" || lines[1].Text != "warn" {
		t.Fatalf("docker log demux primitive: %#v", lines)
	}

	t.Run("local", func(t *testing.T) {
		runPublicStoragePrimitives(t, t.TempDir())
	})

	t.Run("node-backed-process-fixture", func(t *testing.T) {
		bin := carbonStorageBin(t)
		nodeBase := filepath.Join(t.TempDir(), "node-storage")
		node := NodeInfo{ID: "node-acceptance", Name: "node-acceptance", StorageRoot: nodeBase}
		serverID := "srv-acceptance"
		controllerDataRoot := filepath.Join(t.TempDir(), "controller-data", "servers", serverID)
		root := nodeDataRoot(node, controllerDataRoot)
		if !RequiresRemoteTransport(node) {
			t.Fatal("fixture must require node-backed transport before the node-side root exists")
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}

		transport := &ProcessTransport{Bin: bin}
		acc, err := transport.Open(context.Background(), node, controllerDataRoot)
		if err != nil {
			t.Fatalf("open node-backed accessor: %v", err)
		}
		defer func() { _ = acc.Close() }()

		runPublicStoragePrimitives(t, root)

		if _, err := os.Stat(controllerDataRoot); !os.IsNotExist(err) {
			t.Fatalf("transport-only fixture must not create controller-local data root: %v", err)
		}

		if got, err := acc.ReadFile("server.properties"); err != nil || string(got) != "motd=node" {
			t.Fatalf("node-backed accessor sees primitive output: %q %v", got, err)
		}

		cancelCtx, cancel := context.WithCancel(context.Background())
		cancelAcc, err := transport.Open(cancelCtx, node, controllerDataRoot)
		if err != nil {
			t.Fatalf("open cancellable accessor: %v", err)
		}
		if err := cancelAcc.WriteFile("cancel.txt", []byte("before")); err != nil {
			t.Fatal(err)
		}
		cancel()
		_, _ = cancelAcc.ReadFile("cancel.txt")
		_ = cancelAcc.Close()

		raw, err := os.ReadFile(filepath.Join(root, "cancel.txt"))
		if err != nil || string(raw) != "before" {
			t.Fatalf("cancellation must not corrupt node-backed fixture data: %q %v", raw, err)
		}
		if _, err := os.Stat(controllerDataRoot); !os.IsNotExist(err) {
			t.Fatalf("cancelled node-backed fixture must not fall back to controller-local data root: %v", err)
		}
	})
}
