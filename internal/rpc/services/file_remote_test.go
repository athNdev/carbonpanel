// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func TestRemoteArchiveFilenameDoesNotEscapeDestination(t *testing.T) {
	tests := map[string]string{
		"https://example.test/releases/../../outside.zip?download=1": "outside.zip",
		"https://example.test/releases/":                             "downloaded_archive.zip",
		"://invalid":                                                 "downloaded_archive.zip",
	}
	for rawURL, want := range tests {
		if got := remoteArchiveFilename(rawURL); got != want {
			t.Errorf("remoteArchiveFilename(%q) = %q, want %q", rawURL, got, want)
		}
	}
}

func createTestZip(t *testing.T, filename, content string) ([]byte, string) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	f, err := zw.Create(filename)
	if err != nil {
		t.Fatalf("failed to create zip entry: %v", err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatalf("failed to write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close zip writer: %v", err)
	}

	data := buf.Bytes()
	h := sha256.Sum256(data)
	return data, hex.EncodeToString(h[:])
}

// faultingReader emits a prefix and then fails, modelling a download stream
// that dies partway through the staged copy.
type faultingReader struct {
	prefix []byte
	err    error
	failed bool
}

func (r *faultingReader) Read(p []byte) (int, error) {
	if len(r.prefix) > 0 {
		n := copy(p, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	if !r.failed {
		r.failed = true
		return 0, r.err
	}
	return 0, io.EOF
}

// TestSaveRemoteArchiveAtomicallyKeepsTargetOnFailedCopy is the A2 regression:
// a failed copy must leave the previous target intact and leave no staging
// file behind. The production download path uses this exact seam.
func TestSaveRemoteArchiveAtomicallyKeepsTargetOnFailedCopy(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "mods", "pack.jar")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	const original = "original-intact-content"
	if err := os.WriteFile(target, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	wantMode := originalInfo.Mode().Perm()

	injected := errors.New("injected copy failure")
	err = saveRemoteArchiveAtomically(target,
		&faultingReader{prefix: []byte("partial-new-content"), err: injected}, 64)
	if !errors.Is(err, injected) {
		t.Fatalf("copy failure not reported: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("failed copy truncated the target: got %q, want %q", got, original)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".atomic-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("failed copy left staging files: %v", matches)
	}

	// A successful publish replaces the target and preserves its mode.
	if err := saveRemoteArchiveAtomically(target, bytes.NewReader([]byte("new-content")), 64); err != nil {
		t.Fatalf("successful staged save failed: %v", err)
	}
	got, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-content" {
		t.Fatalf("successful save published wrong content: %q", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != wantMode {
		t.Fatalf("staged save did not preserve target mode: got %o, want %o", perm, wantMode)
	}
}

func TestFileService_DownloadRemoteArchive_AutoExtract(t *testing.T) {
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "1")
	store := setupTestStore(t)
	defer store.Close()

	tempDataDir, err := os.MkdirTemp("", "file_service_test_data_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDataDir)

	srvDataDir := filepath.Join(tempDataDir, "server1")
	if err := os.MkdirAll(srvDataDir, 0755); err != nil {
		t.Fatalf("failed to create server data dir: %v", err)
	}

	server := &storage.Server{
		ID:       "srv-test-1",
		Name:     "Test Server",
		DataPath: srvDataDir,
		NodeID:   "local",
	}
	if err := store.CreateServer(context.Background(), server); err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	zipData, expectedSHA := createTestZip(t, "plugins/hello.txt", "world from archive")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipData)
	}))
	defer ts.Close()

	svc := NewFileService(store, nil, nil, nil, logger.New())
	t.Cleanup(svc.Stop)

	req := connect.NewRequest(&v1.DownloadRemoteArchiveRequest{
		ServerId:        server.ID,
		Url:             ts.URL + "/test.zip",
		DestinationPath: "",
		Sha256Checksum:  expectedSHA,
		AutoExtract:     true,
	})

	resp, err := svc.DownloadRemoteArchive(context.Background(), req)
	if err != nil {
		t.Fatalf("DownloadRemoteArchive failed: %v", err)
	}

	taskID := resp.Msg.TaskId
	if taskID == "" {
		t.Fatal("empty task_id returned")
	}

	// Poll until completed
	deadline := time.Now().Add(5 * time.Second)
	completed := false
	for time.Now().Before(deadline) {
		pResp, err := svc.GetRemoteArchiveProgress(context.Background(), connect.NewRequest(&v1.GetRemoteArchiveProgressRequest{
			ServerId: server.ID,
			TaskId:   taskID,
		}))
		if err != nil {
			t.Fatalf("failed to get progress: %v", err)
		}
		if pResp.Msg.Status == "completed" {
			completed = true
			break
		}
		if pResp.Msg.Status == "failed" {
			t.Fatalf("download task failed: %s", pResp.Msg.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !completed {
		t.Fatal("download task did not complete within timeout")
	}

	// Verify extracted file exists
	extractedFile := filepath.Join(srvDataDir, "plugins", "hello.txt")
	content, err := os.ReadFile(extractedFile)
	if err != nil {
		t.Fatalf("extracted file not found: %v", err)
	}
	if string(content) != "world from archive" {
		t.Fatalf("unexpected content: %q", string(content))
	}
}

func TestFileService_DownloadRemoteArchive_ChecksumMismatch(t *testing.T) {
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "1")
	store := setupTestStore(t)
	defer store.Close()

	tempDataDir, err := os.MkdirTemp("", "file_service_test_data_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDataDir)

	srvDataDir := filepath.Join(tempDataDir, "server2")
	_ = os.MkdirAll(srvDataDir, 0755)

	server := &storage.Server{
		ID:       "srv-test-2",
		Name:     "Test Server 2",
		DataPath: srvDataDir,
		NodeID:   "local",
	}
	_ = store.CreateServer(context.Background(), server)

	zipData, _ := createTestZip(t, "test.txt", "data")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipData)
	}))
	defer ts.Close()

	svc := NewFileService(store, nil, nil, nil, logger.New())
	t.Cleanup(svc.Stop)

	req := connect.NewRequest(&v1.DownloadRemoteArchiveRequest{
		ServerId:       server.ID,
		Url:            ts.URL + "/test.zip",
		Sha256Checksum: "0000000000000000000000000000000000000000000000000000000000000000",
		AutoExtract:    true,
	})

	resp, err := svc.DownloadRemoteArchive(context.Background(), req)
	if err != nil {
		t.Fatalf("DownloadRemoteArchive failed: %v", err)
	}

	taskID := resp.Msg.TaskId
	deadline := time.Now().Add(5 * time.Second)
	failed := false
	for time.Now().Before(deadline) {
		pResp, _ := svc.GetRemoteArchiveProgress(context.Background(), connect.NewRequest(&v1.GetRemoteArchiveProgressRequest{
			ServerId: server.ID,
			TaskId:   taskID,
		}))
		if pResp.Msg.Status == "failed" {
			failed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !failed {
		t.Fatal("expected download task to fail on checksum mismatch, but it did not")
	}
}
