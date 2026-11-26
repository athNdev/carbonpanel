// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/datawrite"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/upload"
	"github.com/stretchr/testify/require"
)

// A staged publication holds the exclusive data-write lease from before its
// snapshot through rollback. Every ordinary panel mutation shares that path, so
// it must be refused instead of changing a target between snapshot and commit.
func TestDirectFileWritesAreRefusedDuringStagedPublication(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	dataRoot := t.TempDir()
	server := &storage.Server{
		ID: "file-write-gate", Name: "File write gate", NodeID: "local",
		Status: storage.StatusStopped, DataPath: dataRoot,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, os.WriteFile(filepath.Join(dataRoot, "live.jar"), []byte("installed"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dataRoot, "obsolete.jar"), []byte("obsolete"), 0o644))

	um := upload.NewManager(t.TempDir(), time.Minute, 10<<20, logger.New())
	t.Cleanup(um.Stop)
	session, err := um.InitSession("upload.jar", 3, 3)
	require.NoError(t, err)
	_, err = um.WriteChunk(session.ID, 0, []byte("new"))
	require.NoError(t, err)

	files := NewFileService(store, nil, um, nil, logger.New())
	t.Cleanup(files.Stop)

	fence, err := datawrite.AcquireExclusive(ctx, server.ID)
	require.NoError(t, err)
	t.Cleanup(fence.Release)

	assertRefused := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		require.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	}

	_, err = files.UpdateFile(ctx, connect.NewRequest(&v1.UpdateFileRequest{
		ServerId: server.ID, Path: "live.jar", Content: []byte("panel-edit"),
	}))
	assertRefused(t, err)
	body, readErr := os.ReadFile(filepath.Join(dataRoot, "live.jar"))
	require.NoError(t, readErr)
	require.Equal(t, "installed", string(body))

	_, err = files.SaveUploadedFile(ctx, connect.NewRequest(&v1.SaveUploadedFileRequest{
		ServerId: server.ID, UploadSessionId: session.ID, DestinationPath: ".", Filename: "uploaded.jar",
	}))
	assertRefused(t, err)
	require.NoFileExists(t, filepath.Join(dataRoot, "uploaded.jar"))

	_, err = files.DeleteFile(ctx, connect.NewRequest(&v1.DeleteFileRequest{
		ServerId: server.ID, Path: "obsolete.jar",
	}))
	assertRefused(t, err)
	require.FileExists(t, filepath.Join(dataRoot, "obsolete.jar"))

	_, err = files.RenameFile(ctx, connect.NewRequest(&v1.RenameFileRequest{
		ServerId: server.ID, Path: "live.jar", NewName: "renamed.jar",
	}))
	assertRefused(t, err)
	require.FileExists(t, filepath.Join(dataRoot, "live.jar"))
	require.NoFileExists(t, filepath.Join(dataRoot, "renamed.jar"))

	_, err = files.CreateFolder(ctx, connect.NewRequest(&v1.CreateFolderRequest{
		ServerId: server.ID, Path: "newdir",
	}))
	assertRefused(t, err)
	require.NoDirExists(t, filepath.Join(dataRoot, "newdir"))

	fence.Release()

	_, err = files.UpdateFile(ctx, connect.NewRequest(&v1.UpdateFileRequest{
		ServerId: server.ID, Path: "live.jar", Content: []byte("panel-edit"),
	}))
	require.NoError(t, err)
	body, readErr = os.ReadFile(filepath.Join(dataRoot, "live.jar"))
	require.NoError(t, readErr)
	require.Equal(t, "panel-edit", string(body))
}
