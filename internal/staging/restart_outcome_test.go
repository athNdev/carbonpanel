// SPDX-License-Identifier: MPL-2.0
package staging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/datawrite"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/stretchr/testify/require"
)

type restartStatus func(context.Context, string) (storage.ServerStatus, error)

func (f restartStatus) GetContainerStatus(ctx context.Context, id string) (storage.ServerStatus, error) {
	return f(ctx, id)
}

func TestRestartPublicationCompensatesOnlyDefiniteFailures(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		attempted, canceled, timedOut bool
		status                        storage.ServerStatus
		wantUndo                      bool
	}{
		{name: "canceled preflight", canceled: true, wantUndo: true},
		{name: "confirmed stopped", attempted: true, status: storage.StatusStopped, wantUndo: true},
		{name: "running after error", attempted: true, status: storage.StatusRunning},
		{name: "unknown after error", attempted: true, status: storage.StatusError},
		{name: "timeout still in flight", attempted: true, timedOut: true, status: storage.StatusStopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store, data := newStageStore(t)
			server := &storage.Server{ID: "pub", Name: "pub", DataPath: data}
			require.NoError(t, store.CreateServer(ctx, server))
			require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
			cfg, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "later"}, storage.UpdateWindowNextRestart, nil, 0)
			require.NoError(t, err)
			stageFile(t, store, data, cfg.BatchID, server.ID, "file.txt", "new")
			commit, err := CommitNextRestartFiles(ctx, store, server, nil)
			require.NoError(t, err)
			t.Cleanup(commit.Finish)
			record, err := store.ApplyStagedConfigChange(ctx, cfg.ID)
			require.NoError(t, err)
			publication := RestartPublication{Commit: commit, Configs: []*storage.StagedConfigChange{record}, RuntimeAttempted: tc.attempted}
			if tc.canceled {
				cancel()
			}
			cause := errors.New("preflight or runtime failure")
			if tc.timedOut {
				cause = context.DeadlineExceeded
			}
			inspected := false
			err = publication.Resolve(ctx, store, restartStatus(func(check context.Context, id string) (storage.ServerStatus, error) {
				inspected = true
				require.Equal(t, "container", id)
				require.NoError(t, check.Err())
				_, gateErr := datawrite.TryShared(server.ID)
				require.ErrorIs(t, gateErr, datawrite.ErrExclusiveHeld)
				return tc.status, nil
			}), "container", cause)
			require.Equal(t, tc.attempted && !tc.timedOut, inspected)
			record, readErr := store.GetStagedConfigChange(context.Background(), cfg.ID)
			require.NoError(t, readErr)
			rows, readErr := store.ListBatchContent(context.Background(), cfg.BatchID)
			require.NoError(t, readErr)
			if tc.wantUndo {
				require.NoError(t, err)
				require.Equal(t, storage.StagedStatusStaged, record.Status)
				require.Len(t, rows, 1)
				require.NoFileExists(t, filepath.Join(data, "file.txt"))
			} else {
				require.ErrorContains(t, err, "retained")
				require.Equal(t, storage.StagedStatusApplied, record.Status)
				require.Empty(t, rows)
				body, readErr := os.ReadFile(filepath.Join(data, "file.txt"))
				require.NoError(t, readErr)
				require.Equal(t, "new", string(body))
				retry, retryErr := CommitNextRestartFiles(context.Background(), store, server, nil)
				require.NoError(t, retryErr)
				require.True(t, retry.HasContent(), "a retry must wait for a successful runtime outcome")
				retry.Finish()
			}
			lease, err := datawrite.TryShared(server.ID)
			require.NoError(t, err)
			lease.Release()
		})
	}
}

func TestNextRestartSelectsWindowAfterDataAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "selection", Name: "selection", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "new")
	writer, err := datawrite.TryShared(server.ID)
	require.NoError(t, err)
	t.Cleanup(writer.Release)
	type result struct {
		commit *NextRestartCommit
		err    error
	}
	done := make(chan result, 1)
	go func() { commit, err := CommitNextRestartFiles(ctx, store, server, nil); done <- result{commit, err} }()
	require.Eventually(t, func() bool {
		probe, err := datawrite.TryShared(server.ID)
		if err == nil {
			probe.Release()
		}
		return errors.Is(err, datawrite.ErrExclusiveHeld)
	}, 3*time.Second, time.Millisecond)
	at := time.Now().Add(time.Hour)
	_, err = store.SetOpenBatchWindow(ctx, server.ID, storage.UpdateWindowAtTime, &at, 0)
	require.NoError(t, err)
	writer.Release()
	got := <-done
	require.NoError(t, got.err)
	require.Nil(t, got.commit)
	require.NoFileExists(t, filepath.Join(data, "file.txt"))
}

func TestNextRestartUndoReportsRestagingFailureAndReleasesGate(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "undo-report", Name: "undo-report", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "new.txt", "candidate")
	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	t.Cleanup(commit.Finish)
	require.NoError(t, store.DB().Exec(
		"CREATE TRIGGER reject_restage BEFORE UPDATE OF status ON staged_content_changes WHEN NEW.status = 'staged' BEGIN SELECT RAISE(ABORT, 'restage unavailable'); END").Error)
	require.ErrorContains(t, commit.Undo(), "restage unavailable")
	require.NoFileExists(t, filepath.Join(data, "new.txt"))
	gate, err := datawrite.TryShared(server.ID)
	require.NoError(t, err)
	gate.Release()
}
