// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/datawrite"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/upload"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

// A bulk staged delete is one request-wide shared lease. A publication that
// starts while the first item is still being recorded must not acquire between
// items and split the delete.
func TestStagedBulkDeleteHoldsOneDataWriteLease(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "staged-bulk-delete", storage.StatusStopped)
	files := NewFileService(store, nil, um, nil, logger.New())
	t.Cleanup(files.Stop)

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var once sync.Once
	const callbackName = "fixture:block-first-staged-delete"
	require.NoError(t, store.DB().Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*storage.StagedContentChange)
		if !ok || row.Action != storage.StagedActionDelete {
			return
		}
		once.Do(func() {
			close(firstEntered)
			<-releaseFirst
		})
	}))
	t.Cleanup(func() { _ = store.DB().Callback().Create().Remove(callbackName) })

	done := make(chan error, 1)
	go func() {
		_, err := files.DeleteFile(ctx, connect.NewRequest(&v1.DeleteFileRequest{
			ServerId: server.ID, Paths: []string{"a.txt", "b.txt"}, Stage: true,
		}))
		done <- err
	}()
	select {
	case <-firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("staged delete never reached its first row")
	}

	exclusive := make(chan *datawrite.Lease, 1)
	exclusiveErr := make(chan error, 1)
	go func() {
		lease, err := datawrite.AcquireExclusive(context.Background(), server.ID)
		if err != nil {
			exclusiveErr <- err
			return
		}
		exclusive <- lease
	}()

	// Wait until the exclusive claim has reserved the drain. It cannot own the
	// path while the bulk request's shared lease is still held.
	deadline := time.Now().Add(5 * time.Second)
	for {
		lease, err := datawrite.TryShared(server.ID)
		if err != nil {
			require.ErrorIs(t, err, datawrite.ErrExclusiveHeld)
			break
		}
		lease.Release()
		if time.Now().After(deadline) {
			t.Fatal("exclusive publication never reserved the data path")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseFirst)
	require.NoError(t, <-done, "the whole bulk delete must finish under one lease")
	select {
	case lease := <-exclusive:
		lease.Release()
	case err := <-exclusiveErr:
		t.Fatalf("exclusive publication failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("exclusive publication did not proceed after the bulk delete finished")
	}
	rows, err := store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

// A batch conflict during a staged file write is an admission precondition,
// not an internal failure, and keeps the operator-facing reason.
func TestStageContentBatchConflictIsFailedPrecondition(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "staged-conflict-code", storage.StatusStopped)
	_, err := store.StageConfigChange(ctx, server.ID, map[string]any{"motd": "nightly"}, storage.StagedApplyScheduled, "0 3 * * *")
	require.NoError(t, err)
	files := NewFileService(store, nil, um, nil, logger.New())
	t.Cleanup(files.Stop)

	_, err = files.DeleteFile(ctx, connect.NewRequest(&v1.DeleteFileRequest{
		ServerId: server.ID, Paths: []string{"a.txt"}, Stage: true,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "independently scheduled config row")
	rows, err := store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
}
