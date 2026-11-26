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
				open, readErr := store.OpenUpdateBatch(context.Background(), server.ID)
				require.NoError(t, readErr)
				require.NotNil(t, open)
				require.Nil(t, open.SealedAt, "full compensation leaves the batch open to new staging")
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
					// A new preflight can compensate only the work this attempt
					// applied. The snapshot's prior applied rows stay, because a
					// replacement from the earlier attempt may still embed them.
					retryPublication := RestartPublication{
						Commit: retry, Configs: pending,
						RuntimeAttempted: true, RuntimePreflight: true,
					}
					require.NoError(t, retryPublication.Resolve(ctx, store, errors.New("preflight failed")))
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
	// A row written before the batch_id column existed can carry SQL NULL
	// rather than the zero string. Both are legacy unbatched rows.
	require.NoError(t, store.DB().Exec(
		`INSERT INTO staged_config_changes (id, server_id, batch_id, payload, base_payload, apply_mode, cron_expr, status, created_at)
		 VALUES (?, ?, NULL, ?, '', ?, '', ?, ?)`,
		"legacy-null", server.ID, `{"hardcore":true}`, string(storage.StagedApplyOnRestart),
		string(storage.StagedStatusStaged), time.Now(),
	).Error)
	require.NoError(t, store.DB().Create(&storage.StagedConfigChange{
		ID: "other-batch", ServerID: server.ID, BatchID: "not-this-restart", Payload: `{"difficulty":"hard"}`,
		ApplyMode: storage.StagedApplyOnRestart, Status: storage.StagedStatusStaged,
	}).Error)
	now := time.Now()
	require.NoError(t, store.DB().Create(&storage.StagedConfigChange{
		ID: "timed-legacy", ServerID: server.ID, BatchID: "", Payload: `{"motd":"timed"}`,
		ApplyMode: storage.StagedApplyAtTime, ApplyAt: &now, Status: storage.StagedStatusStaged,
	}).Error)
	require.NoError(t, store.DB().Create(&storage.StagedConfigChange{
		ID: "idle-legacy", ServerID: server.ID, BatchID: "", Payload: `{"motd":"idle"}`,
		ApplyMode: storage.StagedApplyIdle, IdleMinutes: 5, Status: storage.StagedStatusStaged,
	}).Error)

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	t.Cleanup(commit.Finish)
	require.Equal(t, 3, commit.ConfigSnapshot(), "owned batch plus legacy '' and NULL unbatched rows")

	// A row staged after the snapshot (direct store access bypasses the gate)
	// must not enter this restart.
	require.NoError(t, store.DB().Create(&storage.StagedConfigChange{
		ID: "late", ServerID: server.ID, BatchID: "", Payload: `{"difficulty":"hard"}`,
		ApplyMode: storage.StagedApplyOnRestart, Status: storage.StagedStatusStaged,
	}).Error)

	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.Len(t, applied, 3)
	live, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, live.MOTD)
	require.Equal(t, "owned", *live.MOTD)
	require.NotNil(t, live.Difficulty)
	require.Equal(t, "peaceful", *live.Difficulty)
	require.NotNil(t, live.Hardcore)
	require.True(t, *live.Hardcore)

	other, err := store.GetStagedConfigChange(ctx, "other-batch")
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, other.Status)
	late, err := store.GetStagedConfigChange(ctx, "late")
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, late.Status)
	for _, id := range []string{"timed-legacy", "idle-legacy"} {
		row, err := store.GetStagedConfigChange(ctx, id)
		require.NoError(t, err)
		require.Equal(t, storage.StagedStatusStaged, row.Status, "%s is not a next-restart row", id)
	}
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
	require.NoError(t, store.DB().Exec(
		`INSERT INTO staged_config_changes (id, server_id, batch_id, payload, base_payload, apply_mode, cron_expr, status, created_at)
		 VALUES (?, ?, NULL, ?, '', ?, '', ?, ?)`,
		"legacy-null", server.ID, `{"hardcore":true}`, string(storage.StagedApplyOnRestart),
		string(storage.StagedStatusStaged), time.Now(),
	).Error)
	// A timed row with no batch must stay out of an incidental restart even
	// though it is also unbatched.
	now := time.Now()
	require.NoError(t, store.DB().Create(&storage.StagedConfigChange{
		ID: "legacy-timed", ServerID: server.ID, BatchID: "", Payload: `{"difficulty":"hard"}`,
		ApplyMode: storage.StagedApplyAtTime, ApplyAt: &now, Status: storage.StagedStatusStaged,
	}).Error)

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, commit, "legacy rows still need an owned snapshot")
	t.Cleanup(commit.Finish)
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.Len(t, applied, 2)
	live, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, live.MOTD)
	require.Equal(t, "legacy", *live.MOTD)
	require.NotNil(t, live.Hardcore)
	require.True(t, *live.Hardcore)
	timed, err := store.GetStagedConfigChange(ctx, "legacy-timed")
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, timed.Status)
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
	require.False(t, retry.NeedsRuntimePublication(), "acknowledged content owes cleanup, not a new publication")
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

func TestNextRestartPriorStateIncludesAppliedEvidenceBesideStagedRows(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "mixed-prior", Name: "mixed-prior", DataPath: data, Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))

	// A prior attempt applied one content row and one config row, then failed
	// before the batch could close. A new row joins the still-open batch.
	cfg, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "prior"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	stageFile(t, store, data, cfg.BatchID, server.ID, "already.txt", "already")
	rows, err := store.ListBatchContent(ctx, cfg.BatchID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NoError(t, store.MarkStagedContentApplied(ctx, rows[0].ID))
	_, err = store.ApplyStagedConfigChange(ctx, cfg.ID)
	require.NoError(t, err)
	stageFile(t, store, data, cfg.BatchID, server.ID, "next.txt", "next")

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	t.Cleanup(commit.Finish)
	require.True(t, commit.HasContent())
	require.True(t, commit.HasPriorRuntimeState(),
		"applied evidence must survive when staged rows also participate")
	require.Equal(t, 1, commit.ConfigSnapshot())
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.Len(t, applied, 1)
	require.Equal(t, storage.StagedStatusApplied, applied[0].Status)

	// A preflight in this new attempt may undo only the newly committed file.
	// The prior applied row and config stay because an earlier replacement may
	// already embed them.
	require.NoError(t, (&RestartPublication{
		Commit: commit, Configs: applied,
		RuntimeAttempted: true, RuntimePreflight: true,
	}).Resolve(ctx, store, errors.New("preflight failed")))
	prior, err := store.GetStagedContent(ctx, rows[0].ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusApplied, prior.Status)
	next, err := store.GetStagedContent(ctx, server.ID+"-next.txt")
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, next.Status)
	require.NoFileExists(t, filepath.Join(data, "next.txt"))
	live, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, live.MOTD)
	require.Equal(t, "prior", *live.MOTD)
}

func TestRetainedNextRestartSealsAdmissionAndRetryResolvesSnapshot(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "retain-seal", Name: "retain-seal", DataPath: data, Status: storage.StatusRunning}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	cfg, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "retained"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	stageFile(t, store, data, cfg.BatchID, server.ID, "already.txt", "already")
	recovery := filepath.Join(data, ".carbon-panel", "undo", cfg.BatchID)

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.Len(t, applied, 1)
	require.True(t, commit.NeedsRuntimePublication())

	// The Docker request may have been accepted but its outcome was not
	// observed. The applied row and its recovery manifest must be retained.
	retained := &RestartPublication{Commit: commit, Configs: applied, RuntimeAttempted: true}
	require.ErrorContains(t, retained.Resolve(ctx, store, errors.New("replacement may contain them")), "retained")
	require.FileExists(t, filepath.Join(recovery, "manifest.json"))
	require.FileExists(t, filepath.Join(data, "already.txt"))

	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.NotNil(t, open.SealedAt, "a retained publication seals the batch")
	require.Nil(t, open.PublishedAt, "an unobserved outcome is not an acknowledgement")

	// Normal admission must not append a different staged file against the
	// retained batch recovery tree.
	require.NoError(t, os.MkdirAll(filepath.Join(data, ".carbon-panel", "staged", "late"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(data, ".carbon-panel", "staged", "late", "late.txt"), []byte("late"), 0o644))
	_, err = store.AddStagedContent(ctx, &storage.StagedContentChange{
		ServerID: server.ID, Kind: storage.StagedKindFile, Action: storage.StagedActionInstall,
		TargetPath: "late.txt", StagePath: ".carbon-panel/staged/late/late.txt", FileName: "late.txt",
	})
	require.ErrorIs(t, err, storage.ErrUpdateBatchConflict)
	require.ErrorContains(t, err, "retained publication")
	_, err = store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"difficulty": "hard"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.ErrorIs(t, err, storage.ErrUpdateBatchConflict)
	require.FileExists(t, filepath.Join(recovery, "manifest.json"), "refused admission must not touch retained recovery")
	require.NoFileExists(t, filepath.Join(data, "late.txt"))

	// Retry resolves the original snapshot. It still owes the unobserved
	// runtime publication, then finalizes the retained cleanup and closes.
	retry, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, retry)
	require.True(t, retry.HasPriorRuntimeState())
	require.True(t, retry.NeedsRuntimePublication(), "an unacknowledged publication still needs the runtime")
	retryConfigs, err := retry.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.Len(t, retryConfigs, 1)
	require.NoError(t, (&RestartPublication{Commit: retry, Configs: retryConfigs}).Resolve(ctx, store, nil))

	require.NoDirExists(t, recovery)
	require.FileExists(t, filepath.Join(data, "already.txt"))
	require.NoFileExists(t, filepath.Join(data, "late.txt"))
	open, err = store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
}

func TestPersistedMixedNextRestartRowsResolveByDiscardThenRetry(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "mixed-legacy", Name: "mixed-legacy", DataPath: data, Status: storage.StatusRunning}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	cfg, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "prior"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(data, "prior.txt"), []byte("old"), 0o644))
	stageFile(t, store, data, cfg.BatchID, server.ID, "prior.txt", "prior")
	recovery := filepath.Join(data, ".carbon-panel", "undo", cfg.BatchID)

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.ErrorContains(t, (&RestartPublication{Commit: commit, Configs: applied, RuntimeAttempted: true}).
		Resolve(ctx, store, errors.New("outcome unknown")), "retained")
	require.FileExists(t, filepath.Join(recovery, "manifest.json"))

	// Simulate a batch persisted before retention sealed admission: a second
	// staged file is already in the same batch. Clearing the seal through the
	// database is the fixture for that pre-existing mixed state; admission and
	// publication still use the normal production path.
	require.NoError(t, store.DB().Model(&storage.ServerUpdateBatch{}).
		Where("id = ?", cfg.BatchID).Update("sealed_at", nil).Error)
	require.NoError(t, os.MkdirAll(filepath.Join(data, ".carbon-panel", "staged", "mixed"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(data, ".carbon-panel", "staged", "mixed", "mixed.txt"), []byte("mixed"), 0o644))
	late, err := store.AddStagedContent(ctx, &storage.StagedContentChange{
		ServerID: server.ID, Kind: storage.StagedKindFile, Action: storage.StagedActionInstall,
		TargetPath: "mixed.txt", StagePath: ".carbon-panel/staged/mixed/mixed.txt", FileName: "mixed.txt",
	})
	require.NoError(t, err)

	// A new commit must not overwrite or delete the retained recovery tree.
	_, err = CommitNextRestartFiles(ctx, store, server, nil)
	require.ErrorContains(t, err, "discard the newly staged rows")
	require.FileExists(t, filepath.Join(recovery, "manifest.json"))
	require.FileExists(t, filepath.Join(recovery, "000000.data"))
	require.NoFileExists(t, filepath.Join(data, "mixed.txt"))

	// The explicit resolution: discard the new staged rows, keep the batch
	// open because applied content still owns recovery, then retry finalize.
	_, err = store.DiscardStagedContent(ctx, late.ID)
	require.NoError(t, err)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open, "applied content keeps the batch open")

	retry, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, retry)
	require.True(t, retry.NeedsRuntimePublication())
	retryConfigs, err := retry.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.NoError(t, (&RestartPublication{Commit: retry, Configs: retryConfigs}).Resolve(ctx, store, nil))
	require.NoDirExists(t, recovery)
	require.FileExists(t, filepath.Join(data, "prior.txt"))
	require.NoFileExists(t, filepath.Join(data, "mixed.txt"))
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

func TestIdentityUnresolvedReplacementIsSealedNotAcknowledged(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "identity-unresolved", Name: "identity-unresolved", DataPath: data, Status: storage.StatusRunning}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	cfg, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "applied"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	stageFile(t, store, data, cfg.BatchID, server.ID, "file.txt", "new")

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.ErrorContains(t, (&RestartPublication{
		Commit: commit, Configs: applied, RuntimeAttempted: true, RuntimeIdentityUnresolved: true,
	}).Resolve(ctx, store, errors.New("replacement created but ID was not saved")), "canonical identity")

	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.NotNil(t, open.SealedAt)
	require.Nil(t, open.PublishedAt, "a replacement without a persisted identity is not an acknowledgement")
	require.FileExists(t, filepath.Join(data, "file.txt"))

	// The retry knows a prior replacement may own the canonical container and
	// must reconcile it before deciding to start or recreate.
	retry, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, retry)
	require.True(t, retry.RequiresIdentityReconciliation())
}

func TestNextRestartClaimsAdmissionBeforeLiveWritesAndReleasesOnCompensation(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "claim-admission", Name: "claim-admission", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	cfg, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "later"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	stageFile(t, store, data, cfg.BatchID, server.ID, "file.txt", "new")

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	// DB config staging does not take the in-process data lease; the durable
	// claim in the batch transaction must refuse it.
	_, err = store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"difficulty": "hard"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.ErrorIs(t, err, storage.ErrUpdateBatchConflict)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open.SealedAt, "the claim is durable before the first live write")

	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.NoError(t, (&RestartPublication{
		Commit: commit, Configs: applied, RuntimeAttempted: true, RuntimePreflight: true,
	}).Resolve(ctx, store, errors.New("image pull failed")))

	open, err = store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.Nil(t, open.SealedAt, "a fully compensated attempt releases its claim")
	_, err = store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"difficulty": "hard"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err, "normal admission must work after compensation")
}

func TestNextRestartEmptyBatchDoesNotKeepItsClaim(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "claim-noop", Name: "claim-noop", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.Nil(t, commit)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.Equal(t, batch, open.ID)
	require.Nil(t, open.SealedAt, "a no-op must not wedge admission behind a seal")
}

func TestInterruptedNextRestartRecoveryIsRestoredBeforeReuse(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "interrupted", Name: "interrupted", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	require.NoError(t, os.WriteFile(filepath.Join(data, "file.txt"), []byte("old"), 0o644))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "new")
	rows, err := store.ListBatchContent(ctx, batch)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// Simulate a crash after the manifest and live copy but before APPLIED:
	// the rows are still staged and the batch carries the durable claim.
	acc, err := openAccessor(ctx, store, server, nil)
	require.NoError(t, err)
	_, err = commitFiles(ctx, acc, rows)
	require.NoError(t, err)
	require.NoError(t, acc.Close())
	claimed, err := store.ClaimUpdateBatchForPublication(ctx, batch)
	require.NoError(t, err)
	require.True(t, claimed)
	body, err := os.ReadFile(filepath.Join(data, "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "new", string(body))

	// The retry restores the interrupted tree, then re-publishes normally.
	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, commit)
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.NoError(t, (&RestartPublication{Commit: commit, Configs: applied}).Resolve(ctx, store, nil))
	body, err = os.ReadFile(filepath.Join(data, "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "new", string(body))
	require.NoDirExists(t, filepath.Join(data, ".carbon-panel", "undo", batch))
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
}

func TestAcknowledgementPersistenceFailureDoesNotCleanUpOrClose(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "ack-failure", Name: "ack-failure", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "new")
	recovery := filepath.Join(data, ".carbon-panel", "undo", batch)

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	require.NoError(t, store.DB().Exec(
		`CREATE TRIGGER fail_ack BEFORE UPDATE OF published_at ON server_update_batches
		 WHEN NEW.published_at IS NOT NULL BEGIN SELECT RAISE(ABORT, 'ack unavailable'); END`).Error)
	err = (&RestartPublication{Commit: commit, Configs: applied}).Resolve(ctx, store, nil)
	require.ErrorContains(t, err, "ack unavailable")
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open, "a failed acknowledgement must not close the batch")
	require.Nil(t, open.PublishedAt)
	require.NotNil(t, open.SealedAt)
	require.DirExists(t, recovery, "a failed acknowledgement must not run cleanup")
	require.FileExists(t, filepath.Join(data, "file.txt"))
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
