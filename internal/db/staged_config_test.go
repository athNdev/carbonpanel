// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiffMaps(t *testing.T) {
	current := map[string]any{"a": "1", "b": "2", "c": nil}
	desired := map[string]any{"a": "1", "b": "changed", "d": "new"}

	diff := DiffMaps(current, desired)
	assert.Equal(t, map[string]any{"b": "changed", "d": "new"}, diff)
}

func TestDiffMaps_NoDifferences(t *testing.T) {
	current := map[string]any{"a": "1"}
	assert.Empty(t, DiffMaps(current, map[string]any{"a": "1"}))
}

func TestMergeMaps(t *testing.T) {
	base := map[string]any{"a": "1", "b": "2"}
	merged := MergeMaps(base, map[string]any{"b": "override", "c": "3"})
	assert.Equal(t, map[string]any{"a": "1", "b": "override", "c": "3"}, merged)
	// base must be untouched
	assert.Equal(t, "2", base["b"])
}

func TestValidateCronExpr(t *testing.T) {
	assert.NoError(t, ValidateCronExpr("0 3 * * *"))
	assert.Error(t, ValidateCronExpr(""))
	assert.Error(t, ValidateCronExpr("not-a-cron"))
	assert.Error(t, ValidateCronExpr("0 3 * *")) // 4 fields
}

func TestNextRunAfter(t *testing.T) {
	ref := time.Date(2026, 9, 15, 10, 30, 0, 0, time.UTC)
	next, err := NextRunAfter("0 11 * * *", ref)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC), next.In(time.UTC))

	_, err = NextRunAfter("bogus", ref)
	assert.Error(t, err)
}

func newStagedTestStore(t *testing.T, serverID string) *Store {
	t.Helper()
	ctx := context.Background()
	store := newTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	require.NoError(t, store.CreateServer(ctx, &Server{ID: serverID, Name: "staged-test"}))
	cfg := store.CreateDefaultServerConfig(serverID)
	motd := "original"
	cfg.MOTD = &motd
	require.NoError(t, store.SaveServerConfig(ctx, cfg))
	return store
}

func TestStageConfigChange_DiffVsCurrent(t *testing.T) {
	ctx := context.Background()
	store := newStagedTestStore(t, "srv-staged-1")

	// Unchanged value + one real change: only the real change is stored.
	staged, err := store.StageConfigChange(ctx, "srv-staged-1", map[string]any{
		"motd":       "original",
		"difficulty": "hard",
	}, StagedApplyOnRestart, "")
	require.NoError(t, err)
	assert.Equal(t, StagedStatusStaged, staged.Status)
	assert.Equal(t, StagedApplyOnRestart, staged.ApplyMode)
	assert.JSONEq(t, `{"difficulty":"hard"}`, staged.Payload)
}

func TestStageConfigChange_Validation(t *testing.T) {
	ctx := context.Background()
	store := newStagedTestStore(t, "srv-staged-2")

	_, err := store.StageConfigChange(ctx, "srv-staged-2", map[string]any{}, StagedApplyOnRestart, "")
	assert.Error(t, err, "empty changes rejected")

	_, err = store.StageConfigChange(ctx, "srv-staged-2", map[string]any{"motd": "x"}, "sometimes", "")
	assert.Error(t, err, "bad apply mode rejected")

	_, err = store.StageConfigChange(ctx, "srv-staged-2", map[string]any{"motd": "x"}, StagedApplyScheduled, "bogus")
	assert.Error(t, err, "bad cron rejected")

	_, err = store.StageConfigChange(ctx, "srv-staged-2", map[string]any{"motd": "original"}, StagedApplyOnRestart, "")
	assert.Error(t, err, "no-op diff rejected")

	_, err = store.StageConfigChange(ctx, "no-such-server", map[string]any{"motd": "x"}, StagedApplyOnRestart, "")
	assert.Error(t, err, "unknown server rejected")

	// Scheduled mode with a valid cron is accepted.
	staged, err := store.StageConfigChange(ctx, "srv-staged-2", map[string]any{"motd": "midnight"}, StagedApplyScheduled, "0 3 * * *")
	require.NoError(t, err)
	assert.Equal(t, "0 3 * * *", staged.CronExpr)
}

func TestStagedConfigChange_ApplyAndDiscard(t *testing.T) {
	ctx := context.Background()
	store := newStagedTestStore(t, "srv-staged-3")

	staged, err := store.StageConfigChange(ctx, "srv-staged-3", map[string]any{"motd": "staged-value"}, StagedApplyOnRestart, "")
	require.NoError(t, err)

	listed, err := store.ListStagedConfigChanges(ctx, "srv-staged-3", StagedStatusStaged)
	require.NoError(t, err)
	require.Len(t, listed, 1)

	applied, err := store.ApplyStagedConfigChange(ctx, staged.ID)
	require.NoError(t, err)
	assert.Equal(t, StagedStatusApplied, applied.Status)
	require.NotNil(t, applied.AppliedAt)

	live, err := store.GetServerConfig(ctx, "srv-staged-3")
	require.NoError(t, err)
	require.NotNil(t, live.MOTD)
	assert.Equal(t, "staged-value", *live.MOTD)
	assert.Equal(t, "srv-staged-3-config", live.ID, "identity fields preserved")

	// Re-applying is rejected.
	_, err = store.ApplyStagedConfigChange(ctx, staged.ID)
	assert.Error(t, err)

	// Discard path.
	staged2, err := store.StageConfigChange(ctx, "srv-staged-3", map[string]any{"motd": "never"}, StagedApplyOnRestart, "")
	require.NoError(t, err)
	require.NoError(t, store.DiscardStagedConfigChange(ctx, staged2.ID))
	got, err := store.GetStagedConfigChange(ctx, staged2.ID)
	require.NoError(t, err)
	assert.Equal(t, StagedStatusDiscarded, got.Status)

	live, err = store.GetServerConfig(ctx, "srv-staged-3")
	require.NoError(t, err)
	assert.Equal(t, "staged-value", *live.MOTD, "discarded change left live config alone")
}

func TestListDueScheduledStagedChanges(t *testing.T) {
	ctx := context.Background()
	store := newStagedTestStore(t, "srv-staged-4")

	// "* * * * *" fires every minute, so anything staged in the past is due.
	staged, err := store.StageConfigChange(ctx, "srv-staged-4", map[string]any{"motd": "cron"}, StagedApplyScheduled, "* * * * *")
	require.NoError(t, err)

	due, err := store.ListDueScheduledStagedChanges(ctx, time.Now().Add(2*time.Minute))
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, staged.ID, due[0].ID)

	// Far-future reference before any fire: not due.
	due, err = store.ListDueScheduledStagedChanges(ctx, staged.CreatedAt)
	require.NoError(t, err)
	assert.Empty(t, due)

	// on_restart records are never cron-due.
	_, err = store.StageConfigChange(ctx, "srv-staged-4", map[string]any{"motd": "restart"}, StagedApplyOnRestart, "")
	require.NoError(t, err)
	due, err = store.ListDueScheduledStagedChanges(ctx, time.Now().Add(24*time.Hour))
	require.NoError(t, err)
	for _, d := range due {
		assert.Equal(t, StagedApplyScheduled, d.ApplyMode)
	}
}

func TestStageConfigChange_RejectsUnknownAndIdentityFields(t *testing.T) {
	ctx := context.Background()
	store := newStagedTestStore(t, "srv-staged-invalid-fields")

	_, err := store.StageConfigChange(ctx, "srv-staged-invalid-fields", map[string]any{"doesNotExist": "x"}, StagedApplyOnRestart, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown server config field")

	_, err = store.StageConfigChange(ctx, "srv-staged-invalid-fields", map[string]any{"serverId": "other"}, StagedApplyOnRestart, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identity field")
}

func TestApplyStagedConfigChange_InvalidPayloadRollsBack(t *testing.T) {
	ctx := context.Background()
	store := newStagedTestStore(t, "srv-staged-corrupt")
	staged, err := store.StageConfigChange(ctx, "srv-staged-corrupt", map[string]any{"motd": "changed"}, StagedApplyOnRestart, "")
	require.NoError(t, err)
	require.NoError(t, store.DB().Model(&StagedConfigChange{}).Where("id = ?", staged.ID).Update("payload", "{"+string([]byte{34})+"unknownField"+string([]byte{34})+":true}").Error)

	_, err = store.ApplyStagedConfigChange(ctx, staged.ID)
	require.Error(t, err)
	got, err := store.GetStagedConfigChange(ctx, staged.ID)
	require.NoError(t, err)
	assert.Equal(t, StagedStatusStaged, got.Status, "failed apply must roll back its claim")
	live, err := store.GetServerConfig(ctx, "srv-staged-corrupt")
	require.NoError(t, err)
	require.NotNil(t, live.MOTD)
	assert.Equal(t, "original", *live.MOTD, "invalid payload must not modify live config")
}

func TestStagedConfigChange_Rollback(t *testing.T) {
	ctx := context.Background()
	store := newStagedTestStore(t, "srv-staged-rollback")

	// Initially live config MOTD is "original".
	staged, err := store.StageConfigChange(ctx, "srv-staged-rollback", map[string]any{"motd": "new-motd"}, StagedApplyOnRestart, "")
	require.NoError(t, err)

	// Rolling back an unapplied (staged) change must fail.
	_, err = store.RollbackStagedConfigChange(ctx, staged.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only applied changes can be rolled back")

	// Apply the staged change.
	applied, err := store.ApplyStagedConfigChange(ctx, staged.ID)
	require.NoError(t, err)
	assert.Equal(t, StagedStatusApplied, applied.Status)
	assert.NotEmpty(t, applied.PreviousPayload)
	assert.Contains(t, applied.PreviousPayload, "original")

	live, err := store.GetServerConfig(ctx, "srv-staged-rollback")
	require.NoError(t, err)
	assert.Equal(t, "new-motd", *live.MOTD)

	// Roll back the applied change.
	rolledBack, err := store.RollbackStagedConfigChange(ctx, staged.ID)
	require.NoError(t, err)
	assert.Equal(t, StagedStatusRolledBack, rolledBack.Status)
	require.NotNil(t, rolledBack.RolledBackAt)

	// Verify live config restored to "original".
	live, err = store.GetServerConfig(ctx, "srv-staged-rollback")
	require.NoError(t, err)
	assert.Equal(t, "original", *live.MOTD)

	// Rolling back again must fail.
	_, err = store.RollbackStagedConfigChange(ctx, staged.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only applied changes can be rolled back")

	// Record with empty PreviousPayload cannot be rolled back.
	stagedEmptyPrev, err := store.StageConfigChange(ctx, "srv-staged-rollback", map[string]any{"motd": "test2"}, StagedApplyOnRestart, "")
	require.NoError(t, err)
	appliedEmptyPrev, err := store.ApplyStagedConfigChange(ctx, stagedEmptyPrev.ID)
	require.NoError(t, err)
	require.NoError(t, store.DB().Model(&StagedConfigChange{}).Where("id = ?", appliedEmptyPrev.ID).Update("previous_payload", "").Error)

	_, err = store.RollbackStagedConfigChange(ctx, appliedEmptyPrev.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no recorded previous state")
}

func TestApplyRefusesFieldThatMovedAfterStaging(t *testing.T) {
	ctx := context.Background()
	store := newStagedTestStore(t, "srv-moved")
	staged, err := store.StageConfigChange(ctx, "srv-moved", map[string]any{"motd": "staged"}, StagedApplyOnRestart, "")
	require.NoError(t, err)

	live, err := store.GetServerConfig(ctx, "srv-moved")
	require.NoError(t, err)
	moved := "edited since"
	live.MOTD = &moved
	require.NoError(t, store.SaveServerConfig(ctx, live))

	_, err = store.ApplyStagedConfigChange(ctx, staged.ID)
	require.ErrorIs(t, err, ErrStagedConfigMoved)
	got, err := store.GetStagedConfigChange(ctx, staged.ID)
	require.NoError(t, err)
	assert.Equal(t, StagedStatusStaged, got.Status)
	live, err = store.GetServerConfig(ctx, "srv-moved")
	require.NoError(t, err)
	assert.Equal(t, "edited since", *live.MOTD)
}

func TestStageServerConfigUpdateSharesOneWindow(t *testing.T) {
	ctx := context.Background()
	store := newStagedTestStore(t, "srv-window")
	first, err := store.StageServerConfigUpdate(ctx, "srv-window", map[string]any{"motd": "soon"}, UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	when := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	second, err := store.StageServerConfigUpdate(ctx, "srv-window", map[string]any{"difficulty": "hard"}, UpdateWindowAtTime, &when, 0)
	require.NoError(t, err)
	assert.Equal(t, first.BatchID, second.BatchID)
	assert.NotEmpty(t, first.BatchID)

	listed, err := store.ListStagedConfigChanges(ctx, "srv-window", StagedStatusStaged)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	for _, row := range listed {
		assert.Equal(t, StagedApplyAtTime, row.ApplyMode)
		require.NotNil(t, row.ApplyAt)
		assert.True(t, row.ApplyAt.Equal(when))
	}
	live, err := store.GetServerConfig(ctx, "srv-window")
	require.NoError(t, err)
	assert.Equal(t, "original", *live.MOTD)

	_, err = store.StageServerConfigUpdate(ctx, "srv-window", map[string]any{"motd": "idle"}, UpdateWindowIdle, nil, 0)
	require.Error(t, err)
}

func TestLegacyConfigRetargetsEmptyIdleBatchAndResetsIdleClock(t *testing.T) {
	ctx := context.Background()
	const serverID = "srv-empty-idle"
	store := newStagedTestStore(t, serverID)
	first, err := store.StageServerConfigUpdate(ctx, serverID, map[string]any{"motd": "first"}, UpdateWindowIdle, nil, 5)
	require.NoError(t, err)
	since := time.Now().Add(-time.Hour)
	require.NoError(t, store.SetBatchIdleSince(ctx, first.BatchID, &since))
	_, err = store.StageConfigChange(ctx, serverID, map[string]any{"difficulty": "hard"}, StagedApplyOnRestart, "")
	require.ErrorIs(t, err, ErrUpdateBatchConflict, "a pending timed row keeps its window")
	require.NoError(t, store.DiscardStagedConfigChange(ctx, first.ID))
	restarted, err := store.StageConfigChange(ctx, serverID, map[string]any{"difficulty": "hard"}, StagedApplyOnRestart, "")
	require.NoError(t, err)
	require.Equal(t, first.BatchID, restarted.BatchID)
	batch, err := store.OpenUpdateBatch(ctx, serverID)
	require.NoError(t, err)
	require.Equal(t, string(UpdateWindowNextRestart), batch.Window)
	require.Nil(t, batch.IdleSince)
	_, err = store.StageServerConfigUpdate(ctx, serverID, map[string]any{"motd": "second"}, UpdateWindowIdle, nil, 5)
	require.NoError(t, err)
	batch, err = store.OpenUpdateBatch(ctx, serverID)
	require.NoError(t, err)
	require.Nil(t, batch.IdleSince)
}

func TestContentAfterApplyingCronUsesNextRestartBatch(t *testing.T) {
	ctx := context.Background()
	const serverID = "srv-content-after-cron"
	store := newStagedTestStore(t, serverID)
	cron, err := store.StageConfigChange(ctx, serverID, map[string]any{"motd": "cron"}, StagedApplyScheduled, "0 3 * * *")
	require.NoError(t, err)
	_, err = store.ApplyStagedConfigChange(ctx, cron.ID)
	require.NoError(t, err)
	content, err := store.AddStagedContent(ctx, &StagedContentChange{
		ServerID: serverID, Kind: StagedKindMod, Action: StagedActionDelete, TargetPath: "mods/a.jar",
	})
	require.NoError(t, err)
	batch, err := store.OpenUpdateBatch(ctx, serverID)
	require.NoError(t, err)
	require.Equal(t, cron.BatchID, content.BatchID)
	require.Equal(t, string(UpdateWindowNextRestart), batch.Window)
}

func TestLegacyOnRestartBatchAdmitsCompatibleConfigAndReviewedContent(t *testing.T) {
	ctx := context.Background()
	const serverID = "srv-legacy-on-restart"
	store := newStagedTestStore(t, serverID)
	first, err := store.StageConfigChange(ctx, serverID, map[string]any{"motd": "soon"}, StagedApplyOnRestart, "")
	require.NoError(t, err)
	require.NoError(t, store.DB().Model(&ServerUpdateBatch{}).Where("id = ?", first.BatchID).
		UpdateColumn("window", string(StagedApplyOnRestart)).Error)
	before, err := store.OpenUpdateBatch(ctx, serverID)
	require.NoError(t, err)
	require.Equal(t, "on_restart", before.Window)
	second, err := store.StageConfigChange(ctx, serverID, map[string]any{"difficulty": "hard"}, StagedApplyOnRestart, "")
	require.NoError(t, err)
	require.Equal(t, first.BatchID, second.BatchID)
	require.NoError(t, store.CheckReviewedContentWindow(ctx, serverID, UpdateWindowNextRestart, nil, 0))
	prepared, err := store.PrepareReviewedContentBatch(ctx, serverID, UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	require.Equal(t, first.BatchID, prepared.ID)
	require.Equal(t, string(UpdateWindowNextRestart), prepared.Window)
	require.NoError(t, store.AddStagedContentToBatch(ctx, &StagedContentChange{
		ID: uuid.NewString(), ServerID: serverID, Kind: StagedKindMod, Action: StagedActionDelete,
		TargetPath: "mods/a.jar",
	}, prepared.ID, UpdateWindowNextRestart, nil, 0))
}

func TestStageConfigRetargetRollsBackOnInsertFailure(t *testing.T) {
	ctx := context.Background()
	const serverID = "srv-config-retarget"
	store := newStagedTestStore(t, serverID)
	first, err := store.StageServerConfigUpdate(ctx, serverID, map[string]any{"motd": "soon"}, UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	batch, err := store.OpenUpdateBatch(ctx, serverID)
	require.NoError(t, err)
	rows, err := store.ListBatchConfig(ctx, first.BatchID)
	require.NoError(t, err)

	require.NoError(t, store.DB().Exec(`CREATE TRIGGER fail_second_config BEFORE INSERT ON staged_config_changes
		BEGIN SELECT RAISE(ABORT, 'fixture rejected config insert'); END`).Error)
	_, err = store.StageServerConfigUpdate(ctx, serverID, map[string]any{"difficulty": "hard"}, UpdateWindowIdle, nil, 10)
	require.ErrorContains(t, err, "fixture rejected config insert")
	after, err := store.OpenUpdateBatch(ctx, serverID)
	require.NoError(t, err)
	require.Equal(t, batch, after)
	afterRows, err := store.ListBatchConfig(ctx, first.BatchID)
	require.NoError(t, err)
	require.Equal(t, rows, afterRows)
}

func TestConfigRequiresMatchingIdleIntervalForReviewedContent(t *testing.T) {
	ctx := context.Background()
	const serverID = "srv-config-idle-review"
	store := newStagedTestStore(t, serverID)
	batch, err := store.PrepareReviewedContentBatch(ctx, serverID, UpdateWindowIdle, nil, 5)
	require.NoError(t, err)
	content := &StagedContentChange{
		ID: uuid.NewString(), ServerID: serverID, Kind: StagedKindMod, Action: StagedActionDelete,
		TargetPath: "mods/a.jar",
	}
	require.NoError(t, store.AddStagedContentToBatch(ctx, content, batch.ID, UpdateWindowIdle, nil, 5))
	_, err = store.StageServerConfigUpdate(ctx, serverID, map[string]any{"motd": "idle"}, UpdateWindowIdle, nil, 12)
	require.ErrorIs(t, err, ErrUpdateBatchConflict)
	config, err := store.StageServerConfigUpdate(ctx, serverID, map[string]any{"motd": "idle"}, UpdateWindowIdle, nil, 5)
	require.NoError(t, err)
	require.Equal(t, batch.ID, config.BatchID)
	require.Equal(t, 5, config.IdleMinutes)
	require.Equal(t, StagedApplyIdle, config.ApplyMode)
}

func TestConcurrentCronAdmissionCannotJoinTimedModBatch(t *testing.T) {
	ctx := context.Background()
	store := newStagedTestStore(t, "srv-cron-race")
	batch, err := store.SetOpenBatchWindow(ctx, "srv-cron-race", UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	// Existing reviewed mod content in the open batch.
	_, err = store.AddStagedContent(ctx, &StagedContentChange{
		ID: uuid.NewString(), ServerID: "srv-cron-race", BatchID: batch.ID,
		Kind: StagedKindMod, Action: StagedActionReplace,
		TargetPath: "mods/a.jar", StagePath: ".carbon-panel/staged/a/a.jar", FileName: "a.jar",
		Status: StagedStatusStaged,
	})
	require.NoError(t, err)

	const attempts = 6
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = store.StageConfigChange(ctx, "srv-cron-race",
				map[string]any{"motd": "nightly"}, StagedApplyScheduled, "0 3 * * *")
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.ErrorIs(t, err, ErrUpdateBatchConflict, "every concurrent cron admission must be refused")
	}
	configs, err := store.ListBatchConfig(ctx, batch.ID)
	require.NoError(t, err)
	require.Empty(t, configs, "no cron row may be committed into the timed mod batch")
}

func TestSetOpenBatchWindowPreservesCronScheduledConfigRow(t *testing.T) {
	ctx := context.Background()
	store := newStagedTestStore(t, "srv-cron-preserve")
	scheduled, err := store.StageConfigChange(ctx, "srv-cron-preserve",
		map[string]any{"motd": "nightly"}, StagedApplyScheduled, "0 3 * * *")
	require.NoError(t, err)
	batch, err := store.OpenUpdateBatch(ctx, "srv-cron-preserve")
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, batch.ID, scheduled.BatchID)

	_, err = store.SetOpenBatchWindow(ctx, "srv-cron-preserve", UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	rows, err := store.ListBatchConfig(ctx, batch.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, StagedApplyScheduled, rows[0].ApplyMode, "a cron row owns its own schedule")
	assert.Equal(t, "0 3 * * *", rows[0].CronExpr)
}

func TestAddStagedContentToBatchRefusesChangedOrClosedBatch(t *testing.T) {
	ctx := context.Background()
	store := newStagedTestStore(t, "srv-conditional-insert")
	batch, err := store.SetOpenBatchWindow(ctx, "srv-conditional-insert", UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	row := &StagedContentChange{
		ID: uuid.NewString(), ServerID: "srv-conditional-insert",
		Kind: StagedKindMod, Action: StagedActionReplace,
		TargetPath: "mods/a.jar", StagePath: ".carbon-panel/staged/a/a.jar", FileName: "a.jar",
	}
	require.NoError(t, store.AddStagedContentToBatch(ctx, row, batch.ID, UpdateWindowNextRestart, nil, 0))

	// A changed window must fail the conditional insert.
	other := &StagedContentChange{
		ID: uuid.NewString(), ServerID: "srv-conditional-insert",
		Kind: StagedKindMod, Action: StagedActionReplace,
		TargetPath: "mods/b.jar", StagePath: ".carbon-panel/staged/b/b.jar", FileName: "b.jar",
	}
	err = store.AddStagedContentToBatch(ctx, other, batch.ID, UpdateWindowIdle, nil, 5)
	require.Error(t, err, "a batch whose window changed must refuse the row")

	require.NoError(t, store.CloseUpdateBatch(ctx, batch.ID, UpdateBatchDiscarded))
	err = store.AddStagedContentToBatch(ctx, other, batch.ID, UpdateWindowNextRestart, nil, 0)
	require.Error(t, err, "a discarded batch must refuse the row")
}

func TestSetOpenBatchWindowKeepsMixedConfigAndModBatchTogether(t *testing.T) {
	ctx := context.Background()
	store := newStagedTestStore(t, "srv-mod-window")
	config, err := store.StageServerConfigUpdate(ctx, "srv-mod-window", map[string]any{"motd": "soon"}, UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	content, err := store.AddStagedContent(ctx, &StagedContentChange{
		ID:         uuid.NewString(),
		ServerID:   "srv-mod-window",
		Kind:       StagedKindMod,
		Action:     StagedActionReplace,
		TargetPath: "mods/mod.jar",
		StagePath:  ".carbon-panel/staged/mod/mod.jar",
		FileName:   "mod.jar",
	})
	require.NoError(t, err)
	require.Equal(t, config.BatchID, content.BatchID)

	when := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	batch, err := store.SetOpenBatchWindow(ctx, "srv-mod-window", UpdateWindowAtTime, &when, 0)
	require.NoError(t, err)
	require.Equal(t, config.BatchID, batch.ID)
	require.Equal(t, string(UpdateWindowAtTime), batch.Window)
	require.NotNil(t, batch.ApplyAt)
	assert.True(t, batch.ApplyAt.Equal(when))

	rows, err := store.ListStagedConfigChanges(ctx, "srv-mod-window", StagedStatusStaged)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, StagedApplyAtTime, rows[0].ApplyMode)
	contentRows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.Len(t, contentRows, 1)

	// An idempotent submission must not rewrite a matching window.
	again, err := store.SetOpenBatchWindow(ctx, "srv-mod-window", UpdateWindowAtTime, &when, 0)
	require.NoError(t, err)
	require.Equal(t, batch.ID, again.ID)
	require.Equal(t, string(UpdateWindowAtTime), again.Window)
	rows, err = store.ListStagedConfigChanges(ctx, "srv-mod-window", StagedStatusStaged)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, StagedApplyAtTime, rows[0].ApplyMode)

	// An intended transition updates the batch and its config rows together.
	soon := when.Add(time.Hour)
	changed, err := store.SetOpenBatchWindow(ctx, "srv-mod-window", UpdateWindowAtTime, &soon, 0)
	require.NoError(t, err)
	require.True(t, changed.ApplyAt.Equal(soon))
	rows, err = store.ListStagedConfigChanges(ctx, "srv-mod-window", StagedStatusStaged)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].ApplyAt)
	assert.True(t, rows[0].ApplyAt.Equal(soon))
}

func TestStagedWorldLevelWritesRespectActiveWorldBind(t *testing.T) {
	for field, value := range map[string]string{"level": "other", "customServerProperties": "level-name=other"} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			const serverID = "srv-staged-world-bind"
			store := newStagedTestStore(t, serverID)
			change, err := store.StageConfigChange(ctx, serverID, map[string]any{field: value}, StagedApplyOnRestart, "")
			require.NoError(t, err)
			motd, err := store.StageConfigChange(ctx, serverID, map[string]any{"motd": "updated"}, StagedApplyOnRestart, "")
			require.NoError(t, err)

			begin := func(key string) *ServerOperation {
				op, _, err := store.BeginServerOperation(ctx, serverID, "", "", key, "world-bind", key)
				require.NoError(t, err)
				return op
			}
			release := func(op *ServerOperation) {
				require.NoError(t, store.AdvanceServerOperation(ctx, op.ID, ServerOperationPrepared, ServerOperationFailed, "fixture finished"))
			}

			op := begin("apply")
			before, err := store.GetServerConfig(ctx, serverID)
			require.NoError(t, err)
			_, err = store.ApplyStagedConfigChange(ctx, change.ID)
			require.ErrorIs(t, err, ErrActiveOperation)
			after, err := store.GetServerConfig(ctx, serverID)
			require.NoError(t, err)
			require.Equal(t, before, after)
			unapplied, err := store.GetStagedConfigChange(ctx, change.ID)
			require.NoError(t, err)
			require.Equal(t, StagedStatusStaged, unapplied.Status)
			_, err = store.ApplyStagedConfigChange(ctx, motd.ID)
			require.NoError(t, err)
			release(op)
			_, err = store.ApplyStagedConfigChange(ctx, change.ID)
			require.NoError(t, err)

			op = begin("rollback")
			_, err = store.RollbackStagedConfigChange(ctx, change.ID)
			require.ErrorIs(t, err, ErrActiveOperation)
			current, err := store.GetServerConfig(ctx, serverID)
			require.NoError(t, err)
			fields, err := ServerConfigToMap(current)
			require.NoError(t, err)
			require.Equal(t, value, fields[field])
			stillApplied, err := store.GetStagedConfigChange(ctx, change.ID)
			require.NoError(t, err)
			require.Equal(t, StagedStatusApplied, stillApplied.Status)
			_, err = store.RollbackStagedConfigChange(ctx, motd.ID)
			require.NoError(t, err)
			release(op)
			_, err = store.RollbackStagedConfigChange(ctx, change.ID)
			require.NoError(t, err)
		})
	}
}

func stringPtr(s string) *string { return &s }

func TestStagedConfigRollbackRefusesPendingReviewedMod(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	server := &Server{ID: "srv-rollback-gate", Name: "rollback-gate", ModLoader: ModLoaderFabric, MCVersion: "1.21.1"}
	require.NoError(t, store.CreateServer(ctx, server))
	cfg := store.CreateDefaultServerConfig(server.ID)
	cfg.Type = stringPtr("FABRIC")
	cfg.Version = stringPtr("1.21.1")
	require.NoError(t, store.SaveServerConfig(ctx, cfg))

	staged, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"version": "1.21.4"}, UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	_, err = store.ApplyStagedConfigChange(ctx, staged.ID)
	require.NoError(t, err)
	_, err = store.AddStagedContent(ctx, &StagedContentChange{
		ServerID:           server.ID,
		Kind:               StagedKindMod,
		Action:             StagedActionReplace,
		TargetPath:         "mods/mod.jar",
		StagePath:          ".carbon-panel/staged/reviewed/mod.jar",
		FileName:           "mod.jar",
		ExpectedLiveSha512: "deadbeef",
		ApprovedSha512:     strings.Repeat("ab", 64),
		ApprovedProvider:   "modrinth",
		ApprovedProjectID:  "project",
		ApprovedVersionID:  "release",
		ApprovedFilename:   "mod.jar",
	})
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{name: "rollback", run: func() error {
			_, rollbackErr := store.RollbackStagedConfigChange(ctx, staged.ID)
			return rollbackErr
		}},
		{name: "revert", run: func() error {
			_, revertErr := store.RevertStagedConfigApplyToStaged(ctx, staged.ID)
			return revertErr
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, tc.run(), ErrStagedModVersionChange)
			current, err := store.GetServerConfig(ctx, server.ID)
			require.NoError(t, err)
			require.NotNil(t, current.Version)
			require.Equal(t, "1.21.4", *current.Version, "a refused runtime rollback must not change the live target")
			stored, err := store.GetStagedConfigChange(ctx, staged.ID)
			require.NoError(t, err)
			require.Equal(t, StagedStatusApplied, stored.Status)
		})
	}
}
