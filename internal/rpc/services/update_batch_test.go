// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/operation"
	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	"github.com/athNdev/carbon-panel/internal/staging"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/require"
)

func TestUpdateBatchListsAndDiscardsWithoutTouchingLiveFiles(t *testing.T) {
	ctx := context.Background()
	svc, store := setupConfigTestService(t)
	data := t.TempDir()
	server := &storage.Server{ID: "batch-ui", Name: "batch-ui", DataPath: data, Status: storage.StatusRunning}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.DB().Create(&storage.ServerConfig{ID: server.ID, ServerID: server.ID}).Error)
	require.NoError(t, os.WriteFile(filepath.Join(data, "keep.txt"), []byte("live"), 0o644))

	when := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	staged, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "later"}, storage.UpdateWindowAtTime, &when, 0)
	require.NoError(t, err)
	content, err := stageContentBytes(ctx, store, server, nil, storage.StagedKindFile, storage.StagedActionReplace, "keep.txt", "keep.txt", []byte("staged"))
	require.NoError(t, err)

	got, err := svc.GetUpdateBatch(ctx, connect.NewRequest(&v1.GetUpdateBatchRequest{ServerId: server.ID}))
	require.NoError(t, err)
	require.True(t, got.Msg.GetOpen())
	require.Equal(t, v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_AT_TIME, got.Msg.GetWindow())
	require.Equal(t, when.Unix(), got.Msg.GetApplyAtUnix())
	require.Len(t, got.Msg.GetItems(), 2)

	_, err = svc.DiscardUpdateBatch(ctx, connect.NewRequest(&v1.DiscardUpdateBatchRequest{
		ServerId: server.ID,
		ItemId:   content.ID,
	}))
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(data, "keep.txt"))
	require.NoError(t, err)
	require.Equal(t, "live", string(body))
	require.Empty(t, stagedFiles(t, data))

	_, err = svc.DiscardUpdateBatch(ctx, connect.NewRequest(&v1.DiscardUpdateBatchRequest{
		ServerId: server.ID,
		ItemId:   staged.ID,
	}))
	require.NoError(t, err)
	after, err := svc.GetUpdateBatch(ctx, connect.NewRequest(&v1.GetUpdateBatchRequest{ServerId: server.ID}))
	require.NoError(t, err)
	require.False(t, after.Msg.GetOpen())
}

func TestDiscardUpdateBatchReportsJournalContentionAsAborted(t *testing.T) {
	ctx := context.Background()
	svc, store := setupConfigTestService(t)
	server := &storage.Server{ID: "discard-contended", Name: "discard-contended", DataPath: t.TempDir(), Status: storage.StatusRunning}
	require.NoError(t, store.CreateServer(ctx, server))
	_, _, err := store.BeginServerOperation(ctx, server.ID, server.NodeID, server.ContainerID,
		"staging-holder", "update-mod", operation.Digest("update-mod", server))
	require.NoError(t, err)

	_, err = svc.DiscardUpdateBatch(ctx, connect.NewRequest(&v1.DiscardUpdateBatchRequest{ServerId: server.ID}))
	require.Equal(t, connect.CodeAborted, connect.CodeOf(err), "%v", err)
	require.Contains(t, err.Error(), "another operation owns the server")
}

func TestDiscardUpdateBatchRefusesRetainedPublication(t *testing.T) {
	ctx := context.Background()
	svc, store := setupConfigTestService(t)
	data := t.TempDir()
	server := &storage.Server{ID: "discard-retained", Name: "discard-retained", DataPath: data, Status: storage.StatusRunning}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	content, err := stageContentBytes(ctx, store, server, nil, storage.StagedKindFile, storage.StagedActionInstall, "retained.txt", "retained.txt", []byte("candidate"))
	require.NoError(t, err)

	commit, err := staging.CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.ErrorContains(t, (&staging.RestartPublication{Commit: commit, RuntimeAttempted: true}).
		Resolve(ctx, store, errors.New("runtime outcome unknown")), "retained")
	recovery := filepath.Join(data, ".carbon-panel", "undo", content.BatchID)
	require.DirExists(t, recovery)

	// Whole-batch discard would close the batch and orphan the retained
	// recovery tree. The lifecycle retry must resolve it first.
	_, err = svc.DiscardUpdateBatch(ctx, connect.NewRequest(&v1.DiscardUpdateBatchRequest{ServerId: server.ID}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.DirExists(t, recovery)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
}

func TestDiscardUpdateBatchWaitsForNextRestartOutcome(t *testing.T) {
	ctx := context.Background()
	svc, store := setupConfigTestService(t)
	server := &storage.Server{ID: "discard-ownership", Name: "discard-ownership", DataPath: t.TempDir()}
	require.NoError(t, store.CreateServer(ctx, server))
	content, err := stageContentBytes(ctx, store, server, nil, storage.StagedKindFile, storage.StagedActionInstall, "keep.txt", "keep.txt", []byte("candidate"))
	require.NoError(t, err)
	commit, err := staging.CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	t.Cleanup(commit.Finish)
	for _, id := range []string{content.ID, ""} {
		_, err = svc.DiscardUpdateBatch(ctx, connect.NewRequest(&v1.DiscardUpdateBatchRequest{ServerId: server.ID, ItemId: id}))
		require.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	}
	_, err = svc.UpdateServerConfig(ctx, connect.NewRequest(&v1.UpdateServerConfigRequest{ServerId: server.ID, Stage: true, Updates: map[string]string{"motd": "later"}}))
	require.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	_, err = handlers.NewStagedConfigOperations(store).Stage(ctx, server.ID, map[string]any{"motd": "later"}, storage.StagedApplyOnRestart, "")
	var conflict *handlers.StagedConfigError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, 409, conflict.Status)
	require.NoError(t, (&staging.RestartPublication{
		Commit: commit, RuntimeAttempted: true, RuntimePreflight: true,
	}).Resolve(ctx, store, errors.New("preflight failed")))
	_, err = svc.DiscardUpdateBatch(ctx, connect.NewRequest(&v1.DiscardUpdateBatchRequest{ServerId: server.ID}))
	require.NoError(t, err)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, batch)
}
