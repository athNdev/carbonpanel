// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/pkg/download"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/upload"
)

type recordingTransport struct {
	opened     int
	workerRoot string
}

func (r *recordingTransport) Open(_ context.Context, _ nodestorage.NodeInfo, _ string) (nodestorage.Accessor, error) {
	r.opened++
	return nodestorage.NewLocal(r.workerRoot)
}

func transportOnlyFixture(t *testing.T) (*db.Store, *db.Server, *FileService, *recordingTransport, string, string, func()) {
	t.Helper()
	store := setupTestStore(t)
	ctx := context.Background()

	workerRoot := filepath.Join(t.TempDir(), "worker-data")
	if err := os.MkdirAll(workerRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerRoot, "server.properties"), []byte("from-worker"), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := &recordingTransport{workerRoot: workerRoot}
	nodestorage.SetTransport(rt)

	phantomRoot := filepath.Join(t.TempDir(), "phantom-node-root")
	if err := store.CreateNode(ctx, &db.Node{
		ID: "transport-node", Name: "transport-node", Host: "tcp://10.0.0.50:2375", Enabled: true,
		StorageRoot: phantomRoot,
	}); err != nil {
		t.Fatal(err)
	}
	server := &db.Server{
		ID: "srv-transport", Name: "srv-transport", NodeID: "transport-node",
		DataPath: filepath.Join("var", "data", "servers", "srv-transport"),
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	downloads := download.NewManager(t.TempDir(), time.Minute, logger.New())
	svc := NewFileService(store, nil, nil, downloads, logger.New())
	cleanup := func() {
		svc.Stop()
		downloads.Stop()
		nodestorage.SetTransport(nil)
		_ = store.Close()
	}
	return store, server, svc, rt, workerRoot, phantomRoot, cleanup
}

func assertNoControllerLocalTree(t *testing.T, phantomRoot, dataPath string) {
	t.Helper()
	if _, err := os.Stat(phantomRoot); !os.IsNotExist(err) {
		t.Fatalf("controller created node storage_root %s: %v", phantomRoot, err)
	}
	mapped := filepath.Join(phantomRoot, filepath.Base(dataPath))
	if _, err := os.Stat(mapped); !os.IsNotExist(err) {
		t.Fatalf("controller-local mapped root %s exists: %v", mapped, err)
	}
}

func TestListUpdateDeleteUseTransportNotControllerLocalPath(t *testing.T) {
	_, server, svc, rt, workerRoot, phantomRoot, cleanup := transportOnlyFixture(t)
	defer cleanup()
	ctx := context.Background()

	resp, err := svc.ListFiles(ctx, connect.NewRequest(&v1.ListFilesRequest{ServerId: server.ID, Path: "."}))
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if rt.opened == 0 {
		t.Fatal("expected ListFiles to open remote transport, not controller-local IO")
	}
	names := map[string]bool{}
	for _, f := range resp.Msg.Files {
		names[f.Name] = true
	}
	if !names["server.properties"] {
		t.Fatalf("listing must come from transport accessor, got %v", names)
	}

	openedBefore := rt.opened
	_, err = svc.UpdateFile(ctx, connect.NewRequest(&v1.UpdateFileRequest{
		ServerId: server.ID, Path: "server.properties", Content: []byte("patched"),
	}))
	if err != nil {
		t.Fatalf("UpdateFile: %v", err)
	}
	if rt.opened <= openedBefore {
		t.Fatal("expected UpdateFile to open transport")
	}
	got, err := os.ReadFile(filepath.Join(workerRoot, "server.properties"))
	if err != nil || string(got) != "patched" {
		t.Fatalf("worker file = %q, %v", got, err)
	}

	openedBefore = rt.opened
	if err := os.WriteFile(filepath.Join(workerRoot, "gone.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = svc.DeleteFile(ctx, connect.NewRequest(&v1.DeleteFileRequest{
		ServerId: server.ID, Path: "gone.txt",
	}))
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if rt.opened <= openedBefore {
		t.Fatal("expected DeleteFile to open transport")
	}
	if _, err := os.Stat(filepath.Join(workerRoot, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("worker file should be deleted: %v", err)
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
}

func TestCreateCopyMoveFolderUseTransport(t *testing.T) {
	_, server, svc, rt, workerRoot, phantomRoot, cleanup := transportOnlyFixture(t)
	defer cleanup()
	ctx := context.Background()

	openedBefore := rt.opened
	_, err := svc.CreateFolder(ctx, connect.NewRequest(&v1.CreateFolderRequest{
		ServerId: server.ID, Path: "mods",
	}))
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if rt.opened <= openedBefore {
		t.Fatal("expected CreateFolder to open transport")
	}
	if st, err := os.Stat(filepath.Join(workerRoot, "mods")); err != nil || !st.IsDir() {
		t.Fatalf("mods dir on worker: %v", err)
	}

	openedBefore = rt.opened
	_, err = svc.CopyFile(ctx, connect.NewRequest(&v1.CopyFileRequest{
		ServerId: server.ID, SourcePath: "server.properties", DestinationPath: "mods/server.properties",
	}))
	if err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	if rt.opened <= openedBefore {
		t.Fatal("expected CopyFile to open transport")
	}
	got, err := os.ReadFile(filepath.Join(workerRoot, "mods", "server.properties"))
	if err != nil || string(got) != "from-worker" {
		t.Fatalf("copied file = %q, %v", got, err)
	}

	openedBefore = rt.opened
	_, err = svc.MoveFile(ctx, connect.NewRequest(&v1.MoveFileRequest{
		ServerId: server.ID, SourcePath: "mods/server.properties", DestinationPath: "moved.properties",
	}))
	if err != nil {
		t.Fatalf("MoveFile: %v", err)
	}
	if rt.opened <= openedBefore {
		t.Fatal("expected MoveFile to open transport")
	}
	if _, err := os.Stat(filepath.Join(workerRoot, "mods", "server.properties")); !os.IsNotExist(err) {
		t.Fatalf("source should be gone after move: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(workerRoot, "moved.properties"))
	if err != nil || string(got) != "from-worker" {
		t.Fatalf("moved file = %q, %v", got, err)
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
}

func TestFileRPCsRefuseTransportOnlyWithoutProvider(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	nodestorage.SetTransport(nil)

	phantomRoot := filepath.Join(t.TempDir(), "phantom-node-root")
	if err := store.CreateNode(ctx, &db.Node{
		ID: "transport-bare", Name: "transport-bare", Host: "tcp://10.0.0.51:2375", Enabled: true,
		StorageRoot: phantomRoot,
	}); err != nil {
		t.Fatal(err)
	}
	server := &db.Server{
		ID: "srv-tbare", Name: "srv-tbare", NodeID: "transport-bare",
		DataPath: filepath.Join("var", "data", "servers", "srv-tbare"),
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	downloads := download.NewManager(t.TempDir(), time.Minute, logger.New())
	t.Cleanup(downloads.Stop)
	svc := NewFileService(store, nil, nil, downloads, logger.New())
	t.Cleanup(svc.Stop)

	cases := []struct {
		name string
		call func() error
	}{
		{"ListFiles", func() error {
			_, err := svc.ListFiles(ctx, connect.NewRequest(&v1.ListFilesRequest{ServerId: server.ID, Path: "."}))
			return err
		}},
		{"UpdateFile", func() error {
			_, err := svc.UpdateFile(ctx, connect.NewRequest(&v1.UpdateFileRequest{
				ServerId: server.ID, Path: "x.txt", Content: []byte("x"),
			}))
			return err
		}},
		{"DeleteFile", func() error {
			_, err := svc.DeleteFile(ctx, connect.NewRequest(&v1.DeleteFileRequest{
				ServerId: server.ID, Path: "x.txt",
			}))
			return err
		}},
		{"CreateFolder", func() error {
			_, err := svc.CreateFolder(ctx, connect.NewRequest(&v1.CreateFolderRequest{
				ServerId: server.ID, Path: "mods",
			}))
			return err
		}},
		{"CopyFile", func() error {
			_, err := svc.CopyFile(ctx, connect.NewRequest(&v1.CopyFileRequest{
				ServerId: server.ID, SourcePath: "a", DestinationPath: "b",
			}))
			return err
		}},
		{"MoveFile", func() error {
			_, err := svc.MoveFile(ctx, connect.NewRequest(&v1.MoveFileRequest{
				ServerId: server.ID, SourcePath: "a", DestinationPath: "b",
			}))
			return err
		}},
		{"InitFileDownload", func() error {
			_, err := svc.InitFileDownload(ctx, connect.NewRequest(&v1.InitFileDownloadRequest{
				ServerId: server.ID, Path: "server.properties",
			}))
			return err
		}},
		{"CreateArchive", func() error {
			_, err := svc.CreateArchive(ctx, connect.NewRequest(&v1.CreateArchiveRequest{
				ServerId: server.ID, Paths: []string{"."},
			}))
			return err
		}},
		{"DownloadArchive", func() error {
			_, err := svc.DownloadArchive(ctx, connect.NewRequest(&v1.DownloadArchiveRequest{
				ServerId: server.ID, Paths: []string{"."},
			}))
			return err
		}},
		{"ExtractArchive", func() error {
			_, err := svc.ExtractArchive(ctx, connect.NewRequest(&v1.ExtractArchiveRequest{
				ServerId: server.ID, Path: "a.zip",
			}))
			return err
		}},
		{"DownloadRemoteArchive", func() error {
			_, err := svc.DownloadRemoteArchive(ctx, connect.NewRequest(&v1.DownloadRemoteArchiveRequest{
				ServerId: server.ID, Url: "http://1.1.1.1/archive.zip", DestinationPath: "mods",
			}))
			return err
		}},
		{"ExportWorld", func() error {
			_, err := svc.ExportWorld(ctx, connect.NewRequest(&v1.ExportWorldRequest{ServerId: server.ID}))
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if connect.CodeOf(err) != connect.CodeUnavailable {
				t.Fatalf("%s error = %v, want unavailable", tc.name, err)
			}
			if _, err := os.Stat(phantomRoot); !os.IsNotExist(err) {
				t.Fatalf("controller created storage_root after %s: %v", tc.name, err)
			}
			mappedMods := filepath.Join(phantomRoot, filepath.Base(server.DataPath), "mods")
			if _, err := os.Stat(mappedMods); !os.IsNotExist(err) {
				t.Fatalf("controller-local dest %s exists after %s: %v", mappedMods, tc.name, err)
			}
		})
	}
}

func TestZipPipelinesRefuseTransportOnlyEvenWithProvider(t *testing.T) {
	_, server, svc, _, _, phantomRoot, cleanup := transportOnlyFixture(t)
	defer cleanup()
	ctx := context.Background()

	// DownloadRemoteArchive still walks HTTP-to-disk on the controller.
	// Refuse must run before os.MkdirAll or this dest directory is created
	// and the RPC still looks "successful".
	_, err := svc.DownloadRemoteArchive(ctx, connect.NewRequest(&v1.DownloadRemoteArchiveRequest{
		ServerId: server.ID, Url: "http://1.1.1.1/archive.zip", DestinationPath: "mods",
	}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("DownloadRemoteArchive error = %v, want unavailable (no controller zip fallback)", err)
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
	mappedMods := filepath.Join(phantomRoot, filepath.Base(server.DataPath), "mods")
	if _, err := os.Stat(mappedMods); !os.IsNotExist(err) {
		t.Fatalf("controller-local dest %s exists after DownloadRemoteArchive: %v", mappedMods, err)
	}
}

func TestCreateArchiveUsesTransportNotControllerLocalPath(t *testing.T) {
	_, server, svc, rt, workerRoot, phantomRoot, cleanup := transportOnlyFixture(t)
	defer cleanup()
	ctx := context.Background()

	openedBefore := rt.opened
	resp, err := svc.CreateArchive(ctx, connect.NewRequest(&v1.CreateArchiveRequest{
		ServerId: server.ID, Paths: []string{"server.properties"}, ArchiveName: "out.zip",
	}))
	if err != nil {
		t.Fatalf("CreateArchive: %v", err)
	}
	if rt.opened <= openedBefore {
		t.Fatal("expected CreateArchive to open remote transport")
	}
	if resp.Msg.GetArchivePath() != "out.zip" || resp.Msg.GetFilesArchived() != 1 {
		t.Fatalf("create archive = %+v", resp.Msg)
	}
	raw, err := os.ReadFile(filepath.Join(workerRoot, "out.zip"))
	if err != nil {
		t.Fatalf("worker zip missing (controller-local write?): %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range zr.File {
		if f.Name == "server.properties" {
			found = true
		}
	}
	if !found {
		t.Fatalf("worker zip entries = %v", zipNames(zr))
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
	mapped := filepath.Join(phantomRoot, filepath.Base(server.DataPath), "out.zip")
	if _, err := os.Stat(mapped); !os.IsNotExist(err) {
		t.Fatalf("controller-local archive %s exists: %v", mapped, err)
	}
}

func TestDownloadArchiveUsesTransportNotControllerLocalPath(t *testing.T) {
	_, server, svc, rt, _, phantomRoot, cleanup := transportOnlyFixture(t)
	defer cleanup()
	ctx := context.Background()

	openedBefore := rt.opened
	resp, err := svc.DownloadArchive(ctx, connect.NewRequest(&v1.DownloadArchiveRequest{
		ServerId: server.ID, Paths: []string{"server.properties"},
	}))
	if err != nil {
		t.Fatalf("DownloadArchive: %v", err)
	}
	if rt.opened <= openedBefore {
		t.Fatal("expected DownloadArchive to open remote transport")
	}
	session, err := svc.downloadManager.GetSession(resp.Msg.GetSessionId())
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(session.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = zr.Close() })
	found := false
	for _, f := range zr.File {
		if f.Name == "server.properties" {
			found = true
		}
	}
	if !found {
		t.Fatal("download zip missing server.properties")
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
}

func TestExportWorldUsesTransportNotControllerLocalPath(t *testing.T) {
	_, server, svc, rt, _, phantomRoot, cleanup := transportOnlyFixture(t)
	defer cleanup()
	ctx := context.Background()

	openedBefore := rt.opened
	resp, err := svc.ExportWorld(ctx, connect.NewRequest(&v1.ExportWorldRequest{ServerId: server.ID}))
	if err != nil {
		t.Fatalf("ExportWorld: %v", err)
	}
	if rt.opened <= openedBefore {
		t.Fatal("expected ExportWorld to open remote transport")
	}
	if resp.Msg.GetFilename() != "srv-transport-world.zip" {
		t.Fatalf("filename = %q", resp.Msg.GetFilename())
	}
	session, err := svc.downloadManager.GetSession(resp.Msg.GetSessionId())
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(session.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = zr.Close() })
	found := false
	for _, f := range zr.File {
		if f.Name == "server.properties" {
			found = true
		}
	}
	if !found {
		t.Fatal("world export zip missing server.properties")
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
}

func zipNames(zr *zip.Reader) []string {
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

func TestSaveUploadedFileUsesTransport(t *testing.T) {
	_, server, svc, rt, workerRoot, phantomRoot, cleanup := transportOnlyFixture(t)
	defer cleanup()
	ctx := context.Background()

	um := upload.NewManager(t.TempDir(), time.Minute, 10<<20, logger.New())
	t.Cleanup(um.Stop)
	svc.uploadManager = um

	sess, err := um.InitSession("uploaded.jar", 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	_, done, err := um.WriteStream(sess.ID, bytes.NewReader([]byte("data")), 0)
	if err != nil || !done {
		t.Fatalf("upload: done=%v err=%v", done, err)
	}

	_, err = svc.SaveUploadedFile(ctx, connect.NewRequest(&v1.SaveUploadedFileRequest{
		ServerId: server.ID, UploadSessionId: sess.ID, DestinationPath: "mods",
	}))
	if err != nil {
		t.Fatalf("SaveUploadedFile: %v", err)
	}
	if rt.opened == 0 {
		t.Fatal("expected SaveUploadedFile to use transport")
	}
	got, err := os.ReadFile(filepath.Join(workerRoot, "mods", "uploaded.jar"))
	if err != nil || string(got) != "data" {
		t.Fatalf("uploaded file = %q, %v", got, err)
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
}

func TestExtractArchiveUsesTransportNotControllerLocalPath(t *testing.T) {
	_, server, svc, rt, workerRoot, phantomRoot, cleanup := transportOnlyFixture(t)
	defer cleanup()
	ctx := context.Background()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("mods/demo.jar")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("mod-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerRoot, "pack.zip"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	openedBefore := rt.opened
	resp, err := svc.ExtractArchive(ctx, connect.NewRequest(&v1.ExtractArchiveRequest{
		ServerId: server.ID, Path: "pack.zip",
	}))
	if err != nil {
		t.Fatalf("ExtractArchive: %v", err)
	}
	if resp.Msg.GetOperationId() == "" {
		t.Fatal("expected extraction operation id")
	}
	if rt.opened <= openedBefore {
		t.Fatal("expected ExtractArchive to open remote transport")
	}

	want := filepath.Join(workerRoot, "pack", "mods", "demo.jar")
	deadline := time.Now().Add(5 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, err = os.ReadFile(want)
		if err == nil && string(got) == "mod-bytes" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(got) != "mod-bytes" {
		t.Fatalf("worker extract = %q, %v (controller zip fallback?)", got, err)
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
	mapped := filepath.Join(phantomRoot, filepath.Base(server.DataPath), "pack")
	if _, err := os.Stat(mapped); !os.IsNotExist(err) {
		t.Fatalf("controller-local extract tree %s exists: %v", mapped, err)
	}
}

func TestInitFileDownloadUsesTransport(t *testing.T) {
	_, server, svc, rt, _, phantomRoot, cleanup := transportOnlyFixture(t)
	defer cleanup()
	ctx := context.Background()

	resp, err := svc.InitFileDownload(ctx, connect.NewRequest(&v1.InitFileDownloadRequest{
		ServerId: server.ID, Path: "server.properties",
	}))
	if err != nil {
		t.Fatalf("InitFileDownload: %v", err)
	}
	if rt.opened == 0 {
		t.Fatal("expected InitFileDownload to open transport")
	}
	if resp.Msg.GetFilename() != "server.properties" || resp.Msg.GetSessionId() == "" {
		t.Fatalf("download session = %+v", resp.Msg)
	}
	session, err := svc.downloadManager.GetSession(resp.Msg.GetSessionId())
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(session.FilePath)
	if err != nil || string(got) != "from-worker" {
		t.Fatalf("staged download = %q, %v", got, err)
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
}

func TestServerLatestLogUsesTransport(t *testing.T) {
	store, server, _, rt, workerRoot, phantomRoot, cleanup := transportOnlyFixture(t)
	defer cleanup()
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(workerRoot, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerRoot, "logs", "latest.log"), []byte("log-line"), 0o644); err != nil {
		t.Fatal(err)
	}

	body, err := serverLatestLog(ctx, store, server)
	if err != nil || string(body) != "log-line" {
		t.Fatalf("log = %q, %v", body, err)
	}
	if rt.opened == 0 {
		t.Fatal("expected serverLatestLog to open transport")
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
}
