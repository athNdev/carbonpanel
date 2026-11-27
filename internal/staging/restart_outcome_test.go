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
		inPlace                        bool
		timedOut                       bool
		wantUndo                       bool
	}{
		{name: "failure before runtime mutation", wantUndo: true},
		{name: "typed preflight leaves old container untouched", attempted: true, preflight: true, wantUndo: true},
		{name: "in-place mutation survives a fallback preflight", attempted: true, preflight: true, inPlace: true},
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
				RuntimeInPlaceMayHaveMutated: tc.inPlace,
			}
			if tc.attempted || tc.accepted {
				require.NoError(t, publication.BeginDispatch(ctx))
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
	pub := publicationFor(commit, applied)
	require.NoError(t, pub.BeginDispatch(ctx))
	require.NoError(t, pub.Resolve(ctx, store, nil))
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
	pub := &RestartPublication{Commit: commit, Configs: applied, RuntimeAttempted: true, RuntimeAccepted: true}
	require.NoError(t, pub.BeginDispatch(ctx))
	require.NoError(t, pub.Resolve(ctx, store, nil))

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
	failed := &RestartPublication{Commit: commit, RuntimeAttempted: true, RuntimeAccepted: true}
	require.NoError(t, failed.BeginDispatch(ctx))
	err = failed.Resolve(ctx, store, nil)
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
	first := &RestartPublication{Commit: commit, RuntimeAttempted: true, RuntimeAccepted: true}
	require.NoError(t, first.BeginDispatch(ctx))
	require.Error(t, first.Resolve(ctx, store, nil))
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
	require.NoError(t, retained.BeginDispatch(ctx))
	require.NoError(t, retained.RecordReplacement(ctx, "replacement-id"))
	require.ErrorContains(t, retained.Resolve(ctx, store, errors.New("replacement may contain them")), "retained")
	require.DirExists(t, recovery)
	require.FileExists(t, filepath.Join(data, "already.txt"))

	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.NotNil(t, open.SealedAt, "a retained publication seals the batch")
	require.Nil(t, open.PublishedAt, "an unobserved outcome is not an acknowledgement")
	rec, err := store.GetUpdatePublication(ctx, cfg.BatchID)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationDispatched, rec.Phase)

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
	require.DirExists(t, recovery, "refused admission must not touch retained recovery")
	require.NoFileExists(t, filepath.Join(data, "late.txt"))

	// Retry verifies the exact replacement identity recorded at creation. It
	// owes the unverified runtime publication, then finalizes the retained
	// cleanup and closes.
	retry, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, retry)
	require.True(t, retry.HasPriorRuntimeState())
	require.True(t, retry.NeedsIdentityVerification(), "an unverified dispatch is resolved by identity")
	verified, err := retry.VerifyExpectedReplacement(ctx, "replacement-id")
	require.NoError(t, err)
	require.True(t, verified)
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
	retained := &RestartPublication{Commit: commit, Configs: applied, RuntimeAttempted: true}
	require.NoError(t, retained.BeginDispatch(ctx))
	require.NoError(t, retained.RecordReplacement(ctx, "replacement-id"))
	require.ErrorContains(t, retained.Resolve(ctx, store, errors.New("outcome unknown")), "retained")
	require.FileExists(t, filepath.Join(recovery, "000000.data"))

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
	require.True(t, retry.NeedsIdentityVerification())
	verified, err := retry.VerifyExpectedReplacement(ctx, "replacement-id")
	require.NoError(t, err)
	require.True(t, verified)
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
	server := &storage.Server{ID: "identity-unresolved", Name: "identity-unresolved", DataPath: data, ContainerID: "old-id", Status: storage.StatusRunning}
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
	publication := &RestartPublication{Commit: commit, Configs: applied}
	require.NoError(t, publication.BeginDispatch(ctx))
	// Docker created the replacement and its identity was returned, but the
	// server row could not be updated. The returned identity is still recorded
	// as the only acceptable verification evidence for this attempt.
	require.NoError(t, publication.RecordReplacement(ctx, "replacement-id"))
	publication.RuntimeAttempted = true
	publication.RuntimeIdentityUnresolved = true
	require.ErrorContains(t, publication.Resolve(ctx, store, errors.New("replacement created but ID was not saved")), "canonical identity")

	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.NotNil(t, open.SealedAt)
	require.Nil(t, open.PublishedAt, "a replacement without a persisted server ID is not an acknowledgement")
	require.FileExists(t, filepath.Join(data, "file.txt"))

	// The retry knows the exact replacement identity captured at creation. It
	// verifies that identity; discovery alone would not be publication evidence.
	retry, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, retry)
	require.True(t, retry.NeedsIdentityVerification())
	require.Equal(t, "replacement-id", retry.ExpectedReplacementID())
	verified, err := retry.VerifyExpectedReplacement(ctx, "replacement-id")
	require.NoError(t, err)
	require.True(t, verified)
	require.NoError(t, publicationFor(retry, nil).Resolve(ctx, store, nil))
	open, err = store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
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
	require.NoError(t, os.WriteFile(filepath.Join(data, "victim.txt"), []byte("untouched"), 0o644))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "new")
	rows, err := store.ListBatchContent(ctx, batch)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// Simulate a crash after the durable snapshot and live copy but before
	// APPLIED: the row is still staged, the live file is new, and the durable
	// phase proves live writes began.
	record, err := store.BeginUpdatePublication(ctx, batch, server.ID, server.ContainerID)
	require.NoError(t, err)
	pub := newPublicationAttempt(store, record)
	acc, err := openAccessor(ctx, store, server, nil)
	require.NoError(t, err)
	_, err = commitFilesForPublication(ctx, acc, rows, pub)
	require.NoError(t, err)
	require.NoError(t, acc.Close())
	current, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationApplying, current.Phase)
	body, err := os.ReadFile(filepath.Join(data, "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "new", string(body))

	// A game-owned manifest is mutable data. Recovery must ignore it and use
	// only the controller-owned snapshot: a tampered entry must not be restored.
	require.NoError(t, os.WriteFile(
		filepath.Join(data, ".carbon-panel", "undo", batch, "manifest.json"),
		[]byte(`{"version":1,"batch_id":"`+batch+`","entries":[{"target":"victim.txt","existed":false}]}`),
		0o644,
	))

	// The retry restores the recorded snapshot, restages the interrupted row,
	// then re-publishes normally.
	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, commit)
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	pub2 := &RestartPublication{Commit: commit, Configs: applied, RuntimeAttempted: true, RuntimeAccepted: true}
	require.NoError(t, pub2.BeginDispatch(ctx))
	require.NoError(t, pub2.Resolve(ctx, store, nil))
	body, err = os.ReadFile(filepath.Join(data, "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "new", string(body))
	victim, err := os.ReadFile(filepath.Join(data, "victim.txt"))
	require.NoError(t, err)
	require.Equal(t, "untouched", string(victim), "game-owned manifest entries are never recovery evidence")
	require.NoDirExists(t, filepath.Join(data, ".carbon-panel", "undo", batch))
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
	_, err = store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"difficulty": "hard"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err, "normal admission works after the interrupted attempt resolves")
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
	ack := &RestartPublication{Commit: commit, Configs: applied, RuntimeAttempted: true, RuntimeAccepted: true}
	require.NoError(t, ack.BeginDispatch(ctx))
	require.NoError(t, store.DB().Exec(
		`CREATE TRIGGER fail_ack BEFORE UPDATE OF published_at ON server_update_batches
		 WHEN NEW.published_at IS NOT NULL BEGIN SELECT RAISE(ABORT, 'ack unavailable'); END`).Error)
	err = ack.Resolve(ctx, store, nil)
	require.ErrorContains(t, err, "ack unavailable")
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open, "a failed acknowledgement must not close the batch")
	require.Nil(t, open.PublishedAt)
	require.NotNil(t, open.SealedAt)
	rec, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationDispatched, rec.Phase, "a failed acknowledgement must leave the dispatch unverified")
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

func TestPreDispatchInterruptedWithoutContainerResumesStandardCreation(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "pre-dispatch", Name: "pre-dispatch", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "new")
	rows, err := store.ListBatchContent(ctx, batch)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// Crashing after the live copy but before any dispatch with an initially
	// absent container must not be mistaken for a replacement that needs
	// identity reconciliation. There is no canonical container to reconcile.
	record, err := store.BeginUpdatePublication(ctx, batch, server.ID, "")
	require.NoError(t, err)
	acc, err := openAccessor(ctx, store, server, nil)
	require.NoError(t, err)
	_, err = commitFilesForPublication(ctx, acc, rows, newPublicationAttempt(store, record))
	require.NoError(t, err)
	require.NoError(t, acc.Close())

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, commit)
	require.False(t, commit.NeedsIdentityVerification(), "no dispatch was recorded")
	require.Empty(t, commit.ExpectedReplacementID())
	require.True(t, commit.NeedsRuntimePublication(), "the prepare phase still owes a runtime")

	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	publication := &RestartPublication{Commit: commit, Configs: applied, RuntimeAttempted: true, RuntimeAccepted: true}
	// The standard no-container path records dispatch intent, creates the
	// container, records that exact identity, and resolves the publication.
	require.NoError(t, publication.BeginDispatch(ctx))
	require.NoError(t, publication.RecordReplacement(ctx, "created-container"))
	require.NoError(t, publication.Resolve(ctx, store, nil))

	body, err := os.ReadFile(filepath.Join(data, "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "new", string(body))
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
	stored, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationCleaned, stored.Phase)
}

func TestRecordReplacementSurvivesCanceledRequestContext(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "cancel-identity", Name: "cancel-identity", DataPath: data, ContainerID: "old-id"}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "new")

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	t.Cleanup(commit.Finish)
	publication := &RestartPublication{Commit: commit, RuntimeAttempted: true}
	require.NoError(t, publication.BeginDispatch(ctx))

	// Docker created the replacement and the caller's request was cancelled in
	// the same window. The exact identity must still be durable so a retry can
	// verify the container that may already contain the staged bytes.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.NoError(t, publication.RecordReplacement(canceled, "created-after-cancel"))

	record, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, "created-after-cancel", record.ExpectedContainerID)

	commit.Finish()
	retry, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	t.Cleanup(retry.Finish)
	require.True(t, retry.NeedsIdentityVerification())
	verified, err := retry.VerifyExpectedReplacement(ctx, "created-after-cancel")
	require.NoError(t, err)
	require.True(t, verified, "the recorded replacement identity must survive caller cancellation")
}

func TestUnrelatedCanonicalDriftRefusesAcknowledgement(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "drift", Name: "drift", DataPath: data, ContainerID: "old-id", Status: storage.StatusRunning}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "new")
	recovery := filepath.Join(data, ".carbon-panel", "undo", batch)

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	first := &RestartPublication{Commit: commit, Configs: applied, RuntimeAttempted: true}
	require.NoError(t, first.BeginDispatch(ctx))
	require.NoError(t, first.RecordReplacement(ctx, "recorded-replacement"))
	require.ErrorContains(t, first.Resolve(ctx, store, errors.New("unobserved")), "retained")

	retry, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.True(t, retry.NeedsIdentityVerification())
	// A different canonical container is identity repair evidence only. It
	// must never acknowledge this batch or run its cleanup.
	verified, err := retry.VerifyExpectedReplacement(ctx, "unrelated-drift")
	require.NoError(t, err)
	require.False(t, verified)
	require.NoError(t, retry.Retain(ctx))

	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open.SealedAt)
	require.Nil(t, open.PublishedAt)
	record, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationDispatched, record.Phase)
	require.Equal(t, "recorded-replacement", record.ExpectedContainerID)
	require.DirExists(t, recovery, "a refused acknowledgement must not run cleanup")
	require.FileExists(t, filepath.Join(data, "file.txt"))
}

func TestNewlyAppliedRowIsNotCoveredByOlderPublication(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "coverage", Name: "coverage", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	cfg, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "covered"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	batch := cfg.BatchID

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	publication := &RestartPublication{Commit: commit, Configs: applied, RuntimeAttempted: true, RuntimeAccepted: true}
	require.NoError(t, publication.BeginDispatch(ctx))
	require.NoError(t, publication.Resolve(ctx, store, nil))

	record, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationCleaned, record.Phase)
	require.Contains(t, record.AppliedConfigIDs, cfg.ID)
	require.NotContains(t, record.AppliedConfigIDs, "later-row")

	// A row applied after the acknowledged publication is not covered by it.
	now := time.Now()
	require.NoError(t, store.DB().Create(&storage.StagedConfigChange{
		ID: "later-row", ServerID: server.ID, BatchID: batch, Payload: `{"difficulty":"hard"}`,
		ApplyMode: storage.StagedApplyOnRestart, Status: storage.StagedStatusApplied, AppliedAt: &now,
	}).Error)
	pending, err := store.ListPendingRestartStagedConfigs(ctx, server.ID, nil)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "later-row", pending[0].ID)
}

func TestInterruptedBackupBeforeSnapshotIsDiscardedAndRetried(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "partial-backup", Name: "partial-backup", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "new")
	recovery := filepath.Join(data, ".carbon-panel", "undo", batch)

	// Preparation began but crashed while writing the first backup, before the
	// durable snapshot existed. The phase proves no live write began.
	_, err := store.BeginUpdatePublication(ctx, batch, server.ID, server.ContainerID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(recovery, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(recovery, "000000.data"), []byte("partial"), 0o600))

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, commit)
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	publication := &RestartPublication{Commit: commit, Configs: applied, RuntimeAttempted: true, RuntimeAccepted: true}
	require.NoError(t, publication.BeginDispatch(ctx))
	require.NoError(t, publication.Resolve(ctx, store, nil))
	body, err := os.ReadFile(filepath.Join(data, "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "new", string(body))
	require.NoDirExists(t, recovery)
}

func TestModuleIntentMakesInterruptedRecoveryAmbiguousAndSealed(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "module-intent", Name: "module-intent", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, os.WriteFile(filepath.Join(data, "file.txt"), []byte("old"), 0o644))
	stageFile(t, store, data, batch, server.ID, "file.txt", "new")
	rows, err := store.ListBatchContent(ctx, batch)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	record, err := store.BeginUpdatePublication(ctx, batch, server.ID, server.ContainerID)
	require.NoError(t, err)
	acc, err := openAccessor(ctx, store, server, nil)
	require.NoError(t, err)
	_, err = commitFilesForPublication(ctx, acc, rows, newPublicationAttempt(store, record))
	require.NoError(t, err)
	require.NoError(t, acc.Close())
	require.NoError(t, store.RecordUpdatePublicationModuleIntent(ctx, batch, record.Attempt, `["module-1"]`))
	recovery := filepath.Join(data, ".carbon-panel", "undo", batch)
	before, err := os.ReadFile(filepath.Join(recovery, "000000.data"))
	require.NoError(t, err)

	_, err = CommitNextRestartFiles(ctx, store, server, nil)
	require.ErrorContains(t, err, "module runtime mutation")
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open.SealedAt, "an ambiguous module transition must be sealed")
	after, err := os.ReadFile(filepath.Join(recovery, "000000.data"))
	require.NoError(t, err)
	require.Equal(t, before, after, "ambiguous recovery must not restore or clean up")
}

// stageTwoFileBatch stages two ordinary file rows in created order and returns
// the batch id plus the rows as the publisher will select them.
func stageTwoFileBatch(t *testing.T, store *storage.Store, data, serverID, first, second string) (string, []storage.StagedContentChange) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(data, first), []byte("old-"+first), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(data, second), []byte("old-"+second), 0o644))
	batch := openBatch(t, store, serverID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, serverID, first, "new-"+first)
	stageFile(t, store, data, batch, serverID, second, "new-"+second)
	rows, err := store.ListBatchContent(context.Background(), batch)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	return batch, rows
}

// failPublishingSecondFile removes the later row's staged source so the publish
// loop writes the first target and then fails before touching the second. The
// durable APPLYING phase and its snapshot stay in place.
func failPublishingSecondFile(t *testing.T, store *storage.Store, server *storage.Server, data string, rows []storage.StagedContentChange) error {
	t.Helper()
	require.Equal(t, "second.txt", rows[1].TargetPath, "rows are ordered by creation")
	require.NoError(t, os.Remove(filepath.Join(data, filepath.FromSlash(rows[1].StagePath))))
	_, err := CommitNextRestartFiles(context.Background(), store, server, nil)
	require.Error(t, err)
	require.NotErrorIs(t, err, datawrite.ErrExclusiveBusy)
	return err
}

// TestApplyingRecoveryRefusesToOverwriteAnOrdinarySave covers the data-safety
// hazard after an interrupted APPLYING attempt: ordinary saves may land on the
// same targets before the retry. The retry must refuse instead of restoring the
// snapshot over bytes it cannot prove it wrote.
func TestApplyingRecoveryRefusesToOverwriteAnOrdinarySave(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "recover-save", Name: "recover-save", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	require.NoError(t, os.WriteFile(filepath.Join(data, "file.txt"), []byte("old"), 0o644))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "new")
	rows, err := store.ListBatchContent(ctx, batch)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// A first attempt records the snapshot and copies the staged file, then
	// stops before the row is marked APPLIED.
	record, err := store.BeginUpdatePublication(ctx, batch, server.ID, server.ContainerID)
	require.NoError(t, err)
	acc, err := openAccessor(ctx, store, server, nil)
	require.NoError(t, err)
	_, err = commitFilesForPublication(ctx, acc, rows, newPublicationAttempt(store, record))
	require.NoError(t, err)
	require.NoError(t, acc.Close())
	applying, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationApplying, applying.Phase)

	// The panel restarted, so the in-process lease is gone; an ordinary save
	// then lands on the target the interrupted attempt already wrote.
	lease, err := datawrite.TryShared(server.ID)
	require.NoError(t, err, "no lease survives a process restart")
	require.NoError(t, os.WriteFile(filepath.Join(data, "file.txt"), []byte("saved"), 0o644))
	lease.Release()
	recovery := filepath.Join(data, ".carbon-panel", "undo", batch)

	_, err = CommitNextRestartFiles(ctx, store, server, nil)
	require.ErrorContains(t, err, "refuses to overwrite it")
	live, err := os.ReadFile(filepath.Join(data, "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "saved", string(live), "the later save must be preserved")
	require.DirExists(t, recovery, "an ambiguous recovery must keep its evidence")
	current, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationApplying, current.Phase)
	// Recovery stays fail-closed: ordinary writes are still refused until the
	// ambiguous target is resolved.
	_, err = datawrite.TryShared(server.ID)
	require.ErrorIs(t, err, datawrite.ErrExclusiveHeld)
}

// TestInterruptedApplyingAttemptRecoversItsOwnWrites proves a plain interrupted
// attempt still restores and republishes automatically once the obstacle is
// gone, including the untouched target it never reached.
func TestInterruptedApplyingAttemptRecoversItsOwnWrites(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "recover-partial", Name: "recover-partial", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch, rows := stageTwoFileBatch(t, store, data, server.ID, "first.txt", "second.txt")
	failPublishingSecondFile(t, store, server, data, rows)

	written, err := os.ReadFile(filepath.Join(data, "first.txt"))
	require.NoError(t, err)
	require.Equal(t, "new-first.txt", string(written), "the attempt wrote the first target before failing")
	untouched, err := os.ReadFile(filepath.Join(data, "second.txt"))
	require.NoError(t, err)
	require.Equal(t, "old-second.txt", string(untouched), "the failing copy must not touch its target")

	// A process restart drops the retained lease; recovery must still prove the
	// written target by its staged post-image and restore it automatically.
	retainedRecoveryLeases.Lock()
	if lease := retainedRecoveryLeases.byServer[server.ID]; lease != nil {
		delete(retainedRecoveryLeases.byServer, server.ID)
		lease.Release()
	}
	retainedRecoveryLeases.Unlock()
	require.NoError(t, os.WriteFile(filepath.Join(data, filepath.FromSlash(rows[1].StagePath)), []byte("new-second.txt"), 0o644))
	before, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, commit)
	t.Cleanup(commit.Finish)
	for _, target := range []string{"first.txt", "second.txt"} {
		body, readErr := os.ReadFile(filepath.Join(data, target))
		require.NoError(t, readErr)
		require.Equal(t, "new-"+target, string(body), "the retry republishes the staged bytes")
	}
	// The interrupted attempt's tree was consumed and replaced by this
	// attempt's own snapshot. Recovery restored the recorded pre-image first, so
	// the fresh snapshot records that restored content.
	backup, err := os.ReadFile(filepath.Join(data, ".carbon-panel", "undo", batch, "000000.data"))
	require.NoError(t, err)
	require.Equal(t, "old-first.txt", string(backup), "the new snapshot records the restored pre-image")
	commit.Finish()
	after, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, before.Attempt+1, after.Attempt, "the interrupted attempt was restored and retired")
	lease, err := datawrite.TryShared(server.ID)
	require.NoError(t, err, "the resolved retry releases the data path")
	lease.Release()
}

// TestFailedApplyingAttemptKeepsTheLeaseUntilRetry proves the failed attempt
// keeps the exclusive data-write lease while its recovery is pending, so an
// ordinary write cannot land under the snapshot, and that the retry re-adopts
// that lease instead of deadlocking on a second exclusive claim.
func TestFailedApplyingAttemptKeepsTheLeaseUntilRetry(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "recover-lease", Name: "recover-lease", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	_, rows := stageTwoFileBatch(t, store, data, server.ID, "first.txt", "second.txt")
	failPublishingSecondFile(t, store, server, data, rows)

	_, err := datawrite.TryShared(server.ID)
	require.ErrorIs(t, err, datawrite.ErrExclusiveHeld,
		"ordinary writes must stay refused while an APPLYING recovery is pending")

	require.NoError(t, os.WriteFile(filepath.Join(data, filepath.FromSlash(rows[1].StagePath)), []byte("new-second.txt"), 0o644))
	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err, "the retry re-adopts the retained lease")
	require.NotNil(t, commit)
	for _, target := range []string{"first.txt", "second.txt"} {
		body, readErr := os.ReadFile(filepath.Join(data, target))
		require.NoError(t, readErr)
		require.Equal(t, "new-"+target, string(body))
	}
	_, err = datawrite.TryShared(server.ID)
	require.ErrorIs(t, err, datawrite.ErrExclusiveHeld, "the commit handle still owns the path")
	commit.Finish()
	lease, err := datawrite.TryShared(server.ID)
	require.NoError(t, err)
	lease.Release()
}
