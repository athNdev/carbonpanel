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

func TestRestartPublicationCompensatesOnlyBeforeRuntimeMutation(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		attempted, accepted, preflight bool
		timedOut                       bool
		wantUndo                       bool
	}{
		{name: "failure before runtime mutation", wantUndo: true},
		{name: "typed preflight leaves old container untouched", attempted: true, preflight: true, wantUndo: true},
		{name: "transport timeout after request", attempted: true, timedOut: true},
		{name: "definite failure after request", attempted: true},
		{name: "accepted operation", attempted: true, accepted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
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
			applied, err := commit.ApplyConfigs(ctx, store)
			require.NoError(t, err)
			publication := RestartPublication{
				Commit: commit, Configs: applied,
				RuntimeAttempted: tc.attempted, RuntimeAccepted: tc.accepted, RuntimePreflight: tc.preflight,
			}
			cause := errors.New("preflight or runtime failure")
			if tc.timedOut {
				cause = context.DeadlineExceeded
			}
			err = publication.Resolve(ctx, store, cause)
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
				if !tc.accepted {
					require.ErrorContains(t, err, "retained")
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, storage.StagedStatusApplied, record.Status)
				require.Empty(t, rows)
				body, readErr := os.ReadFile(filepath.Join(data, "file.txt"))
				require.NoError(t, readErr)
				require.Equal(t, "new", string(body))
				if !tc.accepted {
					retry, retryErr := CommitNextRestartFiles(ctx, store, server, nil)
					require.NoError(t, retryErr)
					require.NotNil(t, retry)
					require.True(t, retry.HasPriorRuntimeState(), "a prior attempt may have left a replacement")
					pending, applyErr := retry.ApplyConfigs(ctx, store)
					require.NoError(t, applyErr)
					// Even a new preflight cannot prove the current container is
					// the old one after a prior attempt already applied the snapshot.
					retryPublication := RestartPublication{
						Commit: retry, Configs: pending,
						RuntimePreflight: true, RuntimePriorState: retry.HasPriorRuntimeState(),
					}
					require.ErrorContains(t, retryPublication.Resolve(ctx, store, errors.New("preflight failed")), "retained")
					retained, readErr := store.GetStagedConfigChange(context.Background(), cfg.ID)
					require.NoError(t, readErr)
					require.Equal(t, storage.StagedStatusApplied, retained.Status)
				}
			}
			lease, err := datawrite.TryShared(server.ID)
			require.NoError(t, err)
			lease.Release()
		})
	}
}

func TestNextRestartConfigSnapshotIncludesOwnedAndLegacyRows(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "snapshot", Name: "snapshot", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	owned, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "owned"}, storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	stageFile(t, store, data, owned.BatchID, server.ID, "file.txt", "new")
	require.NoError(t, store.DB().Create(&storage.StagedConfigChange{
		ID: "legacy", ServerID: server.ID, BatchID: "", Payload: `{"difficulty":"peaceful"}`,
		ApplyMode: storage.StagedApplyOnRestart, Status: storage.StagedStatusStaged,
	}).Error)
	require.NoError(t, store.DB().Create(&storage.StagedConfigChange{
		ID: "other-batch", ServerID: server.ID, BatchID: "not-this-restart", Payload: `{"difficulty":"hard"}`,
		ApplyMode: storage.StagedApplyOnRestart, Status: storage.StagedStatusStaged,
	}).Error)

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	t.Cleanup(commit.Finish)
	require.Equal(t, 2, commit.ConfigSnapshot(), "owned batch plus legacy unbatched row")

	// A row staged after the snapshot (direct store access bypasses the gate)
	// must not enter this restart.
	require.NoError(t, store.DB().Create(&storage.StagedConfigChange{
		ID: "late", ServerID: server.ID, BatchID: "", Payload: `{"difficulty":"hard"}`,
		ApplyMode: storage.StagedApplyOnRestart, Status: storage.StagedStatusStaged,
	}).Error)

	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.Len(t, applied, 2)
	live, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, live.MOTD)
	require.Equal(t, "owned", *live.MOTD)
	require.NotNil(t, live.Difficulty)
	require.Equal(t, "peaceful", *live.Difficulty)

	other, err := store.GetStagedConfigChange(ctx, "other-batch")
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, other.Status)
	late, err := store.GetStagedConfigChange(ctx, "late")
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, late.Status)
}

func TestNextRestartWithoutBatchAppliesOnlyLegacyConfig(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "legacy-only", Name: "legacy-only", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	require.NoError(t, store.DB().Create(&storage.StagedConfigChange{
		ID: "legacy", ServerID: server.ID, BatchID: "", Payload: `{"motd":"legacy"}`,
		ApplyMode: storage.StagedApplyOnRestart, Status: storage.StagedStatusStaged,
	}).Error)

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, commit, "legacy rows still need an owned snapshot")
	t.Cleanup(commit.Finish)
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.Len(t, applied, 1)
	live, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, live.MOTD)
	require.Equal(t, "legacy", *live.MOTD)
	require.NoError(t, publicationFor(commit, applied).Resolve(ctx, store, nil))
}

func publicationFor(commit *NextRestartCommit, configs []*storage.StagedConfigChange) *RestartPublication {
	return &RestartPublication{Commit: commit, Configs: configs, RuntimeAttempted: true, RuntimeAccepted: true}
}

func TestConfigOnlyNextRestartFinalizeDoesNotOpenFileStorage(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "config-only", Name: "config-only", DataPath: data, NodeID: "missing-node"}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	_, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "only"}, storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	t.Cleanup(commit.Finish)
	require.False(t, commit.HasContent())
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.NoError(t, (&RestartPublication{Commit: commit, Configs: applied, RuntimeAttempted: true, RuntimeAccepted: true}).Resolve(ctx, store, nil))

	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, batch, "config-only batch should close without touching file storage")
}

func TestNextRestartCleanupFailureLeavesRetryableBatch(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "cleanup", Name: "cleanup", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "new")
	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	t.Cleanup(commit.Finish)
	// Make file recovery storage unreachable so cleanup has a real failure.
	server.NodeID = "missing-node"
	err = (&RestartPublication{Commit: commit, RuntimeAttempted: true, RuntimeAccepted: true}).Resolve(ctx, store, nil)
	require.Error(t, err)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open, "a failed cleanup must not close the batch")
	require.Equal(t, storage.UpdateBatchOpen, open.Status)
}

func TestNextRestartCleanupRetryAfterAppliedRowsClosesWithoutRepublish(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "cleanup-next", Name: "cleanup-next", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "new")

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.True(t, commit.HasContent())
	server.NodeID = "missing-node"
	require.Error(t, (&RestartPublication{Commit: commit, RuntimeAttempted: true, RuntimeAccepted: true}).Resolve(ctx, store, nil))
	require.DirExists(t, filepath.Join(data, ".carbon-panel", "undo", batch))

	// The applied row proves cleanup is still owed even though the batch has
	// no staged content left. A later restart finishes it without touching the
	// published bytes.
	require.FileExists(t, filepath.Join(data, "file.txt"))
	server.NodeID = ""
	retry, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, retry, "applied content keeps the next-restart finalize eligible")
	require.True(t, retry.HasPriorRuntimeState())
	applied, err := retry.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.Empty(t, applied)
	require.NoError(t, (&RestartPublication{Commit: retry, RuntimeAttempted: true, RuntimeAccepted: true}).Resolve(ctx, store, nil))
	require.NoDirExists(t, filepath.Join(data, ".carbon-panel", "undo", batch))
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
	body, err := os.ReadFile(filepath.Join(data, "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "new", string(body))
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
