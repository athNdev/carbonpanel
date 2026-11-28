// SPDX-License-Identifier: MPL-2.0

package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/module"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/require"
)

// A direct panel file write participates in the same admission as module
// lifecycles: while an export owns the server, the write is refused and cannot
// create a false stopped-consistency guarantee during the archive read.
func TestDirectFileWriteIsRefusedDuringArchiveExport(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	dataRoot := t.TempDir()
	server := &storage.Server{
		ID: "file-export-server", Name: "Export", NodeID: "local",
		Status: storage.StatusStopped, DataPath: dataRoot,
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	live := filepath.Join(dataRoot, "server.properties")
	if err := os.WriteFile(live, []byte("motd=original\n"), 0o644); err != nil {
		t.Fatalf("write live file: %v", err)
	}

	files := NewFileService(store, nil, nil, nil, logger.New())
	t.Cleanup(files.Stop)
	fence, err := module.TryBeginServerModuleArchive(server.ID)
	if err != nil {
		t.Fatalf("archive fence: %v", err)
	}

	_, err = files.UpdateFile(ctx, connect.NewRequest(&v1.UpdateFileRequest{
		ServerId: server.ID, Path: "server.properties", Content: []byte("motd=changed\n"),
	}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("UpdateFile code = %v, want FailedPrecondition (%v)", got, err)
	}
	body, readErr := os.ReadFile(live)
	if readErr != nil {
		t.Fatalf("read live file: %v", readErr)
	}
	if string(body) != "motd=original\n" {
		t.Fatalf("file changed during export: %q", body)
	}

	fence.Release()
	if _, err := files.UpdateFile(ctx, connect.NewRequest(&v1.UpdateFileRequest{
		ServerId: server.ID, Path: "server.properties", Content: []byte("motd=changed\n"),
	})); err != nil {
		t.Fatalf("UpdateFile after export: %v", err)
	}
	body, readErr = os.ReadFile(live)
	if readErr != nil {
		t.Fatalf("read live file after export: %v", readErr)
	}
	if string(body) != "motd=changed\n" {
		t.Fatalf("write after export = %q", body)
	}
}

// A staged single-file upload during an archive export is refused by the same
// admission as a live write. It must keep the retryable FailedPrecondition
// guidance instead of being remapped to Internal.
func TestStagedFileUploadKeepsArchiveExportRetryGuidance(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	dataRoot := t.TempDir()
	server := &storage.Server{
		ID: "file-staged-export-server", Name: "Staged Export", NodeID: "local",
		Status: storage.StatusStopped, DataPath: dataRoot,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	live := filepath.Join(dataRoot, "server.properties")
	require.NoError(t, os.WriteFile(live, []byte("motd=original\n"), 0o644))

	files := NewFileService(store, nil, nil, nil, logger.New())
	t.Cleanup(files.Stop)
	fence, err := module.TryBeginServerModuleArchive(server.ID)
	require.NoError(t, err)
	t.Cleanup(fence.Release)

	_, err = files.UpdateFile(ctx, connect.NewRequest(&v1.UpdateFileRequest{
		ServerId: server.ID, Path: "server.properties", Content: []byte("motd=staged\n"), Stage: true,
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "archive export is in progress")
	body, readErr := os.ReadFile(live)
	require.NoError(t, readErr)
	require.Equal(t, "motd=original\n", string(body), "a staged upload must not change the live file")
	rows, listErr := store.ListStagedContent(ctx, server.ID)
	require.NoError(t, listErr)
	require.Empty(t, rows)
}

func TestCreateFolderRefusalDoesNotAllocateServerData(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{
		ID: "mkdir-export-server", Name: "Folder Export", NodeID: "local",
		Status: storage.StatusStopped, DataPath: filepath.Join(t.TempDir(), "unallocated"),
	}
	require.NoError(t, store.CreateServer(ctx, server))
	svc := NewFileService(store, nil, nil, nil, logger.New())
	t.Cleanup(svc.Stop)
	fence, err := module.TryBeginServerModuleArchive(server.ID)
	require.NoError(t, err)
	t.Cleanup(fence.Release)

	req := connect.NewRequest(&v1.CreateFolderRequest{ServerId: server.ID, Path: "world"})
	_, err = svc.CreateFolder(ctx, req)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, err = os.Stat(server.DataPath)
	require.ErrorIs(t, err, os.ErrNotExist)

	fence.Release()
	_, err = svc.CreateFolder(ctx, req)
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(server.DataPath, "world"))
	require.NoError(t, err)
	require.True(t, info.IsDir())
}

// A remote-archive download allocates and writes the server data tree, so both
// the local and the transport-only paths must take the same admission as
// ExtractArchive before creating any destination directory.
func TestDownloadRemoteArchiveHoldsServerDataAdmission(t *testing.T) {
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "1")
	ctx := context.Background()
	for _, remote := range []bool{false, true} {
		name := "local-node"
		if remote {
			name = "registered-node"
		}
		t.Run(name, func(t *testing.T) {
			var (
				server      *storage.Server
				svc         *FileService
				cleanup     func()
				destination string
			)
			if remote {
				var workerRoot string
				var stop func()
				_, server, svc, _, workerRoot, _, stop = transportOnlyFixture(t)
				cleanup = stop
				destination = filepath.Join(workerRoot, "new-mods")
			} else {
				store := setupTestStore(t)
				server = &storage.Server{
					ID: "download-admission-local", Name: "Download Admission",
					NodeID: "local", Status: storage.StatusStopped,
					DataPath: filepath.Join(t.TempDir(), "servers", "download-admission-local"),
				}
				require.NoError(t, store.CreateServer(ctx, server))
				svc = NewFileService(store, nil, nil, nil, logger.New())
				cleanup = func() {
					svc.Stop()
					_ = store.Close()
				}
				destination = filepath.Join(server.DataPath, "new-mods")
			}
			t.Cleanup(cleanup)

			fence, err := module.TryBeginServerModuleArchive(server.ID)
			require.NoError(t, err)
			t.Cleanup(fence.Release)

			_, err = svc.DownloadRemoteArchive(ctx, connect.NewRequest(&v1.DownloadRemoteArchiveRequest{
				ServerId: server.ID, Url: "http://127.0.0.1:1/archive.zip",
				DestinationPath: "new-mods",
			}))
			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			require.Contains(t, err.Error(), "archive export is in progress")
			_, statErr := os.Stat(destination)
			require.ErrorIs(t, statErr, os.ErrNotExist, "destination must not be allocated during an export")

			fence.Release()

			// A synchronous destination error after admission still releases the
			// write guard; otherwise the next export would be blocked.
			_, err = svc.DownloadRemoteArchive(ctx, connect.NewRequest(&v1.DownloadRemoteArchiveRequest{
				ServerId: server.ID, Url: "http://127.0.0.1:1/archive.zip",
				DestinationPath: "../escape",
			}))
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			probe, err := module.TryBeginServerModuleArchive(server.ID)
			require.NoError(t, err, "download error left the server data write guard held")
			probe.Release()
		})
	}
}
