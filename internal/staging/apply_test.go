// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package staging

import (
	"context"
	"crypto/sha512"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)
	shortIdle := now.Add(-5 * time.Minute)
	longIdle := now.Add(-11 * time.Minute)

	if got := Decide(now, storage.ServerUpdateBatch{Window: string(storage.UpdateWindowNextRestart)}, PlayerSample{}); got.Due {
		t.Fatal("next restart is not due from the clock")
	}
	if got := Decide(now, storage.ServerUpdateBatch{Window: string(storage.UpdateWindowAtTime), ApplyAt: &future}, PlayerSample{}); got.Due {
		t.Fatal("a future timestamp is not due")
	}
	if got := Decide(now, storage.ServerUpdateBatch{Window: string(storage.UpdateWindowAtTime), ApplyAt: &past}, PlayerSample{}); !got.Due {
		t.Fatal("a missed timestamp fires once")
	}
	if got := Decide(now, storage.ServerUpdateBatch{Window: string(storage.UpdateWindowIdle), IdleMinutes: 10}, PlayerSample{}); got.Due || got.SetIdle {
		t.Fatal("idle does not start without a player provider")
	}
	reset := Decide(now, storage.ServerUpdateBatch{Window: string(storage.UpdateWindowIdle), IdleMinutes: 10, IdleSince: &shortIdle}, PlayerSample{Known: true, Count: 2})
	if reset.Due || !reset.SetIdle || reset.IdleSince != nil {
		t.Fatalf("a player joining clears idle time, got %+v", reset)
	}
	started := Decide(now, storage.ServerUpdateBatch{Window: string(storage.UpdateWindowIdle), IdleMinutes: 10}, PlayerSample{Known: true, Count: 0})
	if started.Due || !started.SetIdle || started.IdleSince == nil {
		t.Fatalf("the first empty reading starts the idle clock, got %+v", started)
	}
	waiting := Decide(now, storage.ServerUpdateBatch{Window: string(storage.UpdateWindowIdle), IdleMinutes: 10, IdleSince: &shortIdle}, PlayerSample{Known: true, Count: 0})
	if waiting.Due {
		t.Fatal("five minutes of a ten minute window is not enough")
	}
	ready := Decide(now, storage.ServerUpdateBatch{Window: string(storage.UpdateWindowIdle), IdleMinutes: 10, IdleSince: &longIdle}, PlayerSample{Known: true, Count: 0})
	if !ready.Due {
		t.Fatal("idle minutes elapsed")
	}
}

func TestPlanFor(t *testing.T) {
	running := PlanFor(storage.StatusRunning, false)
	if !running.SaveAll || !running.Restart || running.Recreate || !running.StartAfter {
		t.Fatalf("running files restart once, got %+v", running)
	}
	runningCfg := PlanFor(storage.StatusRunning, true)
	if !runningCfg.Recreate || runningCfg.Restart || !runningCfg.StartAfter {
		t.Fatalf("running config recreates, got %+v", runningCfg)
	}
	frozen := PlanFor(storage.StatusPaused, false)
	if frozen.SaveAll || !frozen.Restart || !frozen.StartAfter {
		t.Fatalf("freeze gets a real stop and start, got %+v", frozen)
	}
	asleep := PlanFor(storage.StatusDeepSleep, true)
	if !asleep.Recreate || asleep.StartAfter || asleep.Restart {
		t.Fatalf("deep sleep stays stopped, got %+v", asleep)
	}
	filesOnly := PlanFor(storage.StatusDeepSleep, false)
	if filesOnly.TouchesContainer() {
		t.Fatalf("deep sleep file commit does not start the JVM, got %+v", filesOnly)
	}
}

func TestSweepPublishesDueBatchAndLeavesFutureBatch(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "due", Name: "due", DataPath: data, Status: storage.StatusDeepSleep}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, os.WriteFile(filepath.Join(data, "keep.txt"), []byte("old"), 0o644))

	when := time.Now().Add(-time.Minute).UTC()
	batchID := openBatch(t, store, server.ID, storage.UpdateWindowAtTime, &when, 0)
	stageFile(t, store, data, batchID, server.ID, "keep.txt", "new")

	futureServer := &storage.Server{ID: "later", Name: "later", DataPath: t.TempDir(), Status: storage.StatusRunning}
	require.NoError(t, store.CreateServer(ctx, futureServer))
	later := time.Now().Add(time.Hour).UTC()
	futureBatch := openBatch(t, store, futureServer.ID, storage.UpdateWindowAtTime, &later, 0)
	require.NoError(t, os.WriteFile(filepath.Join(futureServer.DataPath, "keep.txt"), []byte("stay"), 0o644))
	stageFile(t, store, futureServer.DataPath, futureBatch, futureServer.ID, "keep.txt", "nope")

	var published []Plan
	err := Sweep(ctx, store, nil, time.Now(), nil, func(_ context.Context, got *storage.Server, plan Plan) error {
		published = append(published, plan)
		require.Equal(t, "due", got.ID)
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, published, "deep sleep does not restart")

	got, err := os.ReadFile(filepath.Join(data, "keep.txt"))
	require.NoError(t, err)
	require.Equal(t, "new", string(got))
	stored, err := store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StatusDeepSleep, stored.Status)
	rows, err := store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)

	untouched, err := os.ReadFile(filepath.Join(futureServer.DataPath, "keep.txt"))
	require.NoError(t, err)
	require.Equal(t, "stay", string(untouched))
	still, err := store.OpenUpdateBatch(ctx, futureServer.ID)
	require.NoError(t, err)
	require.NotNil(t, still)
}

func TestConfigStagingReadsBaselineAfterPriorBatchPublishes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "config-base-after-publish", Name: "config-base-after-publish", DataPath: data, Status: storage.StatusRunning}
	require.NoError(t, store.CreateServer(ctx, server))
	now := time.Now().UTC()
	when := now.Add(-time.Minute)
	first, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "published"}, storage.UpdateWindowAtTime, &when, 0)
	require.NoError(t, err)

	// Pause just before the config producer claims the writer. A due
	// publisher commits and closes the original batch before staging resumes.
	var published bool
	require.NoError(t, store.DB().Callback().Create().Before("gorm:create").Register("fixture:publish_before_staging_claim", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*storage.ServerUpdateBatch); !ok {
			return
		}
		require.NoError(t, Sweep(ctx, store, nil, now, nil, func(context.Context, *storage.Server, Plan) error {
			published = true
			return nil
		}))
	}))
	staged, err := store.StageConfigChange(ctx, server.ID, map[string]any{"motd": "after"}, storage.StagedApplyOnRestart, "")
	require.NoError(t, err)
	require.True(t, published)
	require.NotEqual(t, first.BatchID, staged.BatchID)
	require.JSONEq(t, `{"motd":"published"}`, staged.BasePayload)
	_, err = store.ApplyStagedConfigChange(ctx, staged.ID)
	require.NoError(t, err)
}

func TestSweepRejectsNewConfigAndContentDuringPublication(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "publish-admission", Name: "publish-admission", DataPath: data, Status: storage.StatusRunning}
	require.NoError(t, store.CreateServer(ctx, server))
	now := time.Now().UTC()
	when := now.Add(-time.Minute)
	first, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "first"}, storage.UpdateWindowAtTime, &when, 0)
	require.NoError(t, err)

	var published bool
	addContent := func() (*storage.StagedContentChange, error) {
		return store.AddStagedContent(ctx, &storage.StagedContentChange{
			ServerID: server.ID, Kind: storage.StagedKindFile, Action: storage.StagedActionDelete,
			TargetPath: "untouched.txt",
		})
	}
	addConfig := func() (*storage.StagedConfigChange, error) {
		return store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"difficulty": "hard"}, storage.UpdateWindowAtTime, &when, 0)
	}
	require.NoError(t, os.WriteFile(filepath.Join(data, "untouched.txt"), []byte("original"), 0o600))
	require.NoError(t, Sweep(ctx, store, nil, now, nil, func(_ context.Context, _ *storage.Server, _ Plan) error {
		published = true
		// The publisher is past its config and content snapshot, but still
		// owns the journal and has not closed the batch.
		_, err := addConfig()
		require.ErrorIs(t, err, storage.ErrUpdateBatchConflict)
		_, err = addContent()
		require.ErrorIs(t, err, storage.ErrUpdateBatchConflict)
		return nil
	}))
	require.True(t, published)
	configs, err := store.ListBatchConfig(ctx, first.BatchID)
	require.NoError(t, err)
	require.Empty(t, configs)
	contents, err := store.ListBatchContent(ctx, first.BatchID)
	require.NoError(t, err)
	require.Empty(t, contents)
	old, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, old)
	original, err := os.ReadFile(filepath.Join(data, "untouched.txt"))
	require.NoError(t, err)
	require.Equal(t, "original", string(original))

	retry, err := addConfig()
	require.NoError(t, err)
	require.NotEqual(t, first.BatchID, retry.BatchID)
	content, err := addContent()
	require.NoError(t, err)
	require.Equal(t, retry.BatchID, content.BatchID)
}

func TestSweepReevaluatesAWindowRetargetedBeforeJournalAcquisition(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "publish-retarget", Name: "publish-retarget", DataPath: data, Status: storage.StatusRunning}
	require.NoError(t, store.CreateServer(ctx, server))
	now := time.Now().UTC()
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	first, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "first"}, storage.UpdateWindowAtTime, &past, 0)
	require.NoError(t, err)
	var published, retargeted bool
	require.NoError(t, Sweep(ctx, store, nil, now, func(string) (int, bool) {
		// Sweep has read the old window, but the writer has not acquired the
		// operation journal yet. This config retarget wins admission.
		_, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"difficulty": "hard"}, storage.UpdateWindowAtTime, &future, 0)
		require.NoError(t, err)
		retargeted = true
		return 0, false
	}, func(_ context.Context, _ *storage.Server, _ Plan) error {
		published = true
		return nil
	}))
	require.True(t, retargeted)
	require.False(t, published)
	configs, err := store.ListBatchConfig(ctx, first.BatchID)
	require.NoError(t, err)
	require.Len(t, configs, 2)
	for _, row := range configs {
		require.True(t, row.ApplyAt.Equal(future))
	}
}

func TestApplyRollsBackWhenPublishFails(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "run", Name: "run", DataPath: data, Status: storage.StatusRunning, ContainerID: "c1"}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, os.WriteFile(filepath.Join(data, "keep.txt"), []byte("old"), 0o644))
	when := time.Now().Add(-time.Minute).UTC()
	batchID := openBatch(t, store, server.ID, storage.UpdateWindowAtTime, &when, 0)
	stageFile(t, store, data, batchID, server.ID, "keep.txt", "new")

	err := Sweep(ctx, store, nil, time.Now(), nil, func(context.Context, *storage.Server, Plan) error {
		return os.ErrClosed
	})
	require.Error(t, err)
	got, readErr := os.ReadFile(filepath.Join(data, "keep.txt"))
	require.NoError(t, readErr)
	require.Equal(t, "old", string(got))
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	rows, err := store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestApplyStopsBeforeRestartWhenConfigMoved(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "moved", Name: "moved", DataPath: data, Status: storage.StatusRunning, ContainerID: "c1"}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.DB().Create(&storage.ServerConfig{ID: server.ID, ServerID: server.ID}).Error)
	when := time.Now().Add(-time.Minute).UTC()
	_, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "staged"}, storage.UpdateWindowAtTime, &when, 0)
	require.NoError(t, err)
	require.NoError(t, store.DB().Model(&storage.ServerConfig{}).Where("server_id = ?", server.ID).Update("motd", "someone-else").Error)
	require.NoError(t, os.WriteFile(filepath.Join(data, "keep.txt"), []byte("old"), 0o644))
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	stageFile(t, store, data, batch.ID, server.ID, "keep.txt", "new")

	published := false
	err = Sweep(ctx, store, nil, time.Now(), nil, func(context.Context, *storage.Server, Plan) error {
		published = true
		return nil
	})
	require.ErrorIs(t, err, storage.ErrStagedConfigMoved)
	require.False(t, published)
	got, readErr := os.ReadFile(filepath.Join(data, "keep.txt"))
	require.NoError(t, readErr)
	require.Equal(t, "old", string(got))
}

func TestApplyBatchKeepsConfigRetryableAfterPublishFailure(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "retry", Name: "retry", DataPath: data, Status: storage.StatusRunning, ContainerID: "c1"}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.DB().Create(&storage.ServerConfig{ID: server.ID, ServerID: server.ID}).Error)
	require.NoError(t, os.WriteFile(filepath.Join(data, "keep.txt"), []byte("old"), 0o644))
	when := time.Now().Add(-time.Minute).UTC()
	_, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "new"}, storage.UpdateWindowAtTime, &when, 0)
	require.NoError(t, err)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	stageFile(t, store, data, batch.ID, server.ID, "keep.txt", "new")

	err = Sweep(ctx, store, nil, time.Now(), nil, func(context.Context, *storage.Server, Plan) error {
		return os.ErrClosed
	})
	require.Error(t, err)

	body, readErr := os.ReadFile(filepath.Join(data, "keep.txt"))
	require.NoError(t, readErr)
	require.Equal(t, "old", string(body))
	cfg, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	if cfg.MOTD != nil {
		require.NotEqual(t, "new", *cfg.MOTD)
	}
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	configs, err := store.ListBatchConfig(ctx, open.ID)
	require.NoError(t, err)
	require.Len(t, configs, 1)
	require.Equal(t, storage.StagedStatusStaged, configs[0].Status)
	rows, err := store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	require.NoError(t, Sweep(ctx, store, nil, time.Now(), nil, func(context.Context, *storage.Server, Plan) error {
		return nil
	}))
	cfg, err = store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, cfg.MOTD)
	require.Equal(t, "new", *cfg.MOTD)
	body, err = os.ReadFile(filepath.Join(data, "keep.txt"))
	require.NoError(t, err)
	require.Equal(t, "new", string(body))
	open, err = store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
}

func TestSweepRetryAfterPartialSuccessDoesNotRepublish(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "partial", Name: "partial", DataPath: data, Status: storage.StatusRunning, ContainerID: "c1"}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.DB().Create(&storage.ServerConfig{ID: server.ID, ServerID: server.ID}).Error)
	require.NoError(t, os.WriteFile(filepath.Join(data, "keep.txt"), []byte("old"), 0o644))
	when := time.Now().Add(-time.Minute).UTC()
	_, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "new"}, storage.UpdateWindowAtTime, &when, 0)
	require.NoError(t, err)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	stageFile(t, store, data, batch.ID, server.ID, "keep.txt", "new")
	stagedPath := filepath.Join(data, ".carbon-panel", "staged", server.ID+"-keep.txt", "keep.txt")
	require.FileExists(t, stagedPath)

	// Publication succeeds, but the first attempt's final bookkeeping fails,
	// leaving the batch open after a completed restart/recreate.
	closeFailures := 1
	require.NoError(t, store.DB().Callback().Update().Before("gorm:update").Register("oss282-close-once", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Model.(*storage.ServerUpdateBatch); !ok || closeFailures == 0 {
			return
		}
		closeFailures--
		_ = tx.AddError(errors.New("injected batch close failure"))
	}))

	publishes := 0
	publish := func(context.Context, *storage.Server, Plan) error {
		publishes++
		return nil
	}
	require.Error(t, Sweep(ctx, store, nil, time.Now(), nil, publish))
	require.Equal(t, 1, publishes)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open, "failed finalization leaves the batch open")

	// The real retry entry point must finish bookkeeping without publishing again.
	require.NoError(t, Sweep(ctx, store, nil, time.Now(), nil, publish))
	require.Equal(t, 1, publishes, "completed publication must not be repeated")

	cfg, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, cfg.MOTD)
	require.Equal(t, "new", *cfg.MOTD)
	body, err := os.ReadFile(filepath.Join(data, "keep.txt"))
	require.NoError(t, err)
	require.Equal(t, "new", string(body))
	require.FileExists(t, stagedPath, "staged bytes survive partial success and retry")
	open, err = store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
	require.NoDirExists(t, filepath.Join(data, ".carbon-panel", "undo", batch.ID))
}

func TestApplyBatchReconcilesExistingRolledBackConfig(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "reconcile", Name: "reconcile", DataPath: data, Status: storage.StatusDeepSleep}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.DB().Create(&storage.ServerConfig{ID: server.ID, ServerID: server.ID}).Error)
	when := time.Now().Add(-time.Minute).UTC()
	staged, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "new"}, storage.UpdateWindowAtTime, &when, 0)
	require.NoError(t, err)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.NoError(t, store.DB().Model(&storage.StagedConfigChange{}).Where("id = ?", staged.ID).
		Updates(map[string]any{"status": string(storage.StagedStatusRolledBack), "rolled_back_at": time.Now()}).Error)

	require.NoError(t, Sweep(ctx, store, nil, time.Now(), nil, func(context.Context, *storage.Server, Plan) error { return nil }))
	cfg, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, cfg.MOTD)
	require.Equal(t, "new", *cfg.MOTD)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
}

func TestIdleClockSurvivesAndDoesNotFireEarly(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "idle", Name: "idle", DataPath: data, Status: storage.StatusPaused, ContainerID: "c1"}
	require.NoError(t, store.CreateServer(ctx, server))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowIdle, nil, 10)
	stageFile(t, store, data, batch, server.ID, "keep.txt", "later")

	now := time.Now().UTC()
	require.NoError(t, Sweep(ctx, store, nil, now, func(string) (int, bool) { return 0, true }, nil))
	stored, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.IdleSince)
	require.NoFileExists(t, filepath.Join(data, "keep.txt"))

	require.NoError(t, Sweep(ctx, store, nil, now.Add(9*time.Minute), func(string) (int, bool) { return 0, true }, func(context.Context, *storage.Server, Plan) error {
		t.Fatal("idle window fired early")
		return nil
	}))

	var plan Plan
	require.NoError(t, Sweep(ctx, store, nil, now.Add(10*time.Minute), func(string) (int, bool) { return 0, true }, func(_ context.Context, _ *storage.Server, got Plan) error {
		plan = got
		return nil
	}))
	require.True(t, plan.Restart)
	require.True(t, plan.StartAfter)
	require.False(t, plan.SaveAll)
	body, err := os.ReadFile(filepath.Join(data, "keep.txt"))
	require.NoError(t, err)
	require.Equal(t, "later", string(body))
}

func TestNextRestartContentIsNotAppliedByTheSweep(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "next", Name: "next", DataPath: data, Status: storage.StatusRunning, ContainerID: "c1"}
	require.NoError(t, store.CreateServer(ctx, server))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "mod.jar", "bytes")

	require.NoError(t, Sweep(ctx, store, nil, time.Now(), nil, func(context.Context, *storage.Server, Plan) error {
		t.Fatal("sweep published a next-restart batch")
		return nil
	}))
	require.NoFileExists(t, filepath.Join(data, "mod.jar"))

	undo, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, undo)
	body, err := os.ReadFile(filepath.Join(data, "mod.jar"))
	require.NoError(t, err)
	require.Equal(t, "bytes", string(body))
	undo()
	require.NoFileExists(t, filepath.Join(data, "mod.jar"))
	rows, err := store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func newStageStore(t *testing.T) (*storage.Store, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.NewSQLiteStore(&config.Config{
		Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "panel.sqlite")},
	})
	require.NoError(t, err)
	require.NoError(t, store.Migrate())
	t.Cleanup(func() { _ = store.Close() })
	return store, dir
}

func openBatch(t *testing.T, store *storage.Store, serverID string, window storage.UpdateWindow, at *time.Time, idle int) string {
	t.Helper()
	batch, err := store.AddStagedContent(context.Background(), &storage.StagedContentChange{
		ServerID:   serverID,
		Kind:       storage.StagedKindFile,
		Action:     storage.StagedActionInstall,
		TargetPath: "placeholder",
		StagePath:  ".carbon-panel/staged/placeholder/placeholder",
		FileName:   "placeholder",
	})
	require.NoError(t, err)
	require.NoError(t, store.DB().Where("id = ?", batch.ID).Delete(&storage.StagedContentChange{}).Error)
	require.NoError(t, store.DB().Model(&storage.ServerUpdateBatch{}).Where("id = ?", batch.BatchID).Updates(map[string]any{
		"window":       string(window),
		"apply_at":     at,
		"idle_minutes": idle,
	}).Error)
	return batch.BatchID
}

func stageFile(t *testing.T, store *storage.Store, dataRoot, batchID, serverID, name, body string) {
	t.Helper()
	id := serverID + "-" + name
	stageRel := ".carbon-panel/staged/" + id + "/" + name
	require.NoError(t, os.MkdirAll(filepath.Join(dataRoot, ".carbon-panel", "staged", id), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataRoot, ".carbon-panel", "staged", id, name), []byte(body), 0o644))
	require.NoError(t, store.DB().Create(&storage.StagedContentChange{
		ID:         id,
		ServerID:   serverID,
		BatchID:    batchID,
		Kind:       storage.StagedKindFile,
		Action:     storage.StagedActionInstall,
		TargetPath: name,
		StagePath:  stageRel,
		FileName:   name,
		Status:     storage.StagedStatusStaged,
	}).Error)
}

func stageReviewedModRow(t *testing.T, store *storage.Store, dataRoot, batchID string, server *storage.Server, candidateBody string) string {
	t.Helper()
	id := server.ID + "-reviewed"
	stageRel := ".carbon-panel/staged/" + id + "/mod.jar"
	require.NoError(t, os.MkdirAll(filepath.Join(dataRoot, ".carbon-panel", "staged", id), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataRoot, filepath.FromSlash(stageRel)), []byte(candidateBody), 0o644))
	require.NoError(t, store.DB().Create(&storage.StagedContentChange{
		ID:                 id,
		ServerID:           server.ID,
		BatchID:            batchID,
		Kind:               storage.StagedKindMod,
		Action:             storage.StagedActionReplace,
		TargetPath:         "mods/mod.jar",
		StagePath:          stageRel,
		FileName:           "mod.jar",
		ExpectedLiveSha512: modDigest("installed"),
		ApprovedSha512:     modDigest(candidateBody),
		ApprovedProvider:   "modrinth",
		ApprovedProjectID:  "project",
		ApprovedVersionID:  "release",
		ApprovedFilename:   "mod.jar",
		Status:             storage.StagedStatusStaged,
	}).Error)
	return stageRel
}

func modDigest(body string) string {
	return fmt.Sprintf("%x", sha512.Sum512([]byte(body)))
}

func TestPublicationRefusesLegacyReviewedRowWithoutDurableApproval(t *testing.T) {
	for _, publication := range []struct {
		name   string
		window storage.UpdateWindow
		status storage.ServerStatus
		apply  func(context.Context, *testing.T, *storage.Store, *storage.Server) error
	}{
		{
			name:   "timed batch",
			window: storage.UpdateWindowAtTime,
			status: storage.StatusDeepSleep,
			apply: func(ctx context.Context, t *testing.T, store *storage.Store, server *storage.Server) error {
				batch, err := store.OpenUpdateBatch(ctx, server.ID)
				require.NoError(t, err)
				require.NotNil(t, batch)
				return ApplyBatch(ctx, store, nil, server, *batch, nil)
			},
		},
		{
			name:   "next-restart batch",
			window: storage.UpdateWindowNextRestart,
			status: storage.StatusRunning,
			apply: func(ctx context.Context, t *testing.T, store *storage.Store, server *storage.Server) error {
				_, err := CommitNextRestartFiles(ctx, store, server, nil)
				return err
			},
		},
	} {
		t.Run(publication.name, func(t *testing.T) {
			ctx := context.Background()
			store, data := newStageStore(t)
			server := &storage.Server{
				ID: "legacy-approval", Name: "legacy-approval", DataPath: data,
				MCVersion: "1.21.1", ModLoader: storage.ModLoaderFabric, Status: publication.status,
			}
			require.NoError(t, store.CreateServer(ctx, server))
			live := filepath.Join(data, "mods", "mod.jar")
			require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
			require.NoError(t, os.WriteFile(live, []byte("installed"), 0o644))

			var at *time.Time
			if publication.window == storage.UpdateWindowAtTime {
				when := time.Now().Add(-time.Minute).UTC()
				at = &when
			}
			batchID := openBatch(t, store, server.ID, publication.window, at, 0)
			stageReviewedModRow(t, store, data, batchID, server, "candidate")
			// Simulate a row recorded before durable approval admission. Its
			// metadata still looks valid, which must not be used as approval.
			require.NoError(t, store.DB().Model(&storage.StagedContentChange{}).
				Where("server_id = ?", server.ID).Update("approved_sha512", "").Error)

			err := publication.apply(ctx, t, store, server)
			require.ErrorIs(t, err, storage.ErrStagedModApprovalMissing)
			require.Contains(t, err.Error(), "re-review and re-stage")

			got, readErr := os.ReadFile(live)
			require.NoError(t, readErr)
			require.Equal(t, "installed", string(got), "a legacy row must not publish without durable approval")
			rows, err := store.ListStagedContent(ctx, server.ID)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, storage.StagedStatusStaged, rows[0].Status)
			open, err := store.OpenUpdateBatch(ctx, server.ID)
			require.NoError(t, err)
			require.NotNil(t, open, "the refused row stays staged for explicit re-review")
		})
	}
}

func TestPublicationRefusesStagedPayloadDivergence(t *testing.T) {
	for _, publication := range []struct {
		name   string
		window storage.UpdateWindow
		status storage.ServerStatus
		apply  func(context.Context, *testing.T, *storage.Store, *storage.Server) error
	}{
		{
			name:   "timed batch",
			window: storage.UpdateWindowAtTime,
			status: storage.StatusDeepSleep,
			apply: func(ctx context.Context, t *testing.T, store *storage.Store, server *storage.Server) error {
				batch, err := store.OpenUpdateBatch(ctx, server.ID)
				require.NoError(t, err)
				require.NotNil(t, batch)
				return ApplyBatch(ctx, store, nil, server, *batch, nil)
			},
		},
		{
			name:   "next-restart batch",
			window: storage.UpdateWindowNextRestart,
			status: storage.StatusRunning,
			apply: func(ctx context.Context, t *testing.T, store *storage.Store, server *storage.Server) error {
				_, err := CommitNextRestartFiles(ctx, store, server, nil)
				return err
			},
		},
	} {
		t.Run(publication.name, func(t *testing.T) {
			ctx := context.Background()
			store, data := newStageStore(t)
			server := &storage.Server{
				ID: "divergence-" + publication.name, Name: "divergence", DataPath: data,
				MCVersion: "1.21.1", ModLoader: storage.ModLoaderFabric, Status: publication.status,
			}
			require.NoError(t, store.CreateServer(ctx, server))
			live := filepath.Join(data, "mods", "mod.jar")
			require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
			require.NoError(t, os.WriteFile(live, []byte("installed"), 0o644))

			var at *time.Time
			if publication.window == storage.UpdateWindowAtTime {
				when := time.Now().Add(-time.Minute).UTC()
				at = &when
			}
			batchID := openBatch(t, store, server.ID, publication.window, at, 0)
			stageRel := stageReviewedModRow(t, store, data, batchID, server, "candidate")
			require.NoError(t, os.WriteFile(filepath.Join(data, filepath.FromSlash(stageRel)), []byte("rewritten-payload"), 0o644))

			err := publication.apply(ctx, t, store, server)
			require.Error(t, err)
			require.Contains(t, err.Error(), "durable approved SHA-512")
			got, readErr := os.ReadFile(live)
			require.NoError(t, readErr)
			require.Equal(t, "installed", string(got), "divergent staged bytes must not reach the live tree")
			rows, listErr := store.ListStagedContent(ctx, server.ID)
			require.NoError(t, listErr)
			require.Len(t, rows, 1)
			require.Equal(t, storage.StagedStatusStaged, rows[0].Status)
		})
	}
}

func TestApplyBatchPublishesUnreviewedModUpload(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "upload-mod", Name: "upload-mod", DataPath: data, Status: storage.StatusDeepSleep}
	require.NoError(t, store.CreateServer(ctx, server))

	when := time.Now().Add(-time.Minute).UTC()
	batchID := openBatch(t, store, server.ID, storage.UpdateWindowAtTime, &when, 0)
	stageFileInBatch(t, store, data, batchID, server.ID, storage.StagedKindMod, storage.StagedActionInstall, "mods/upload.jar", "upload.jar", []byte("uploaded-bytes"))
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.NoError(t, ApplyBatch(ctx, store, nil, server, *batch, nil))

	got, err := os.ReadFile(filepath.Join(data, "mods", "upload.jar"))
	require.NoError(t, err)
	require.Equal(t, "uploaded-bytes", string(got))
	rows, err := store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func stageFileInBatch(t *testing.T, store *storage.Store, dataRoot, batchID, serverID, kind, action, target, name string, body []byte) {
	t.Helper()
	id := serverID + "-" + name
	stageRel := ".carbon-panel/staged/" + id + "/" + name
	require.NoError(t, os.MkdirAll(filepath.Join(dataRoot, ".carbon-panel", "staged", id), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataRoot, ".carbon-panel", "staged", id, name), body, 0o644))
	require.NoError(t, store.DB().Create(&storage.StagedContentChange{
		ID:         id,
		ServerID:   serverID,
		BatchID:    batchID,
		Kind:       kind,
		Action:     action,
		TargetPath: target,
		StagePath:  stageRel,
		FileName:   name,
		Status:     storage.StagedStatusStaged,
	}).Error)
}

func TestPublicationRefusesReviewedRowWithoutDurableReleaseIdentity(t *testing.T) {
	for _, publication := range []struct {
		name   string
		window storage.UpdateWindow
		status storage.ServerStatus
		apply  func(context.Context, *testing.T, *storage.Store, *storage.Server) error
	}{
		{
			name:   "timed batch",
			window: storage.UpdateWindowAtTime,
			status: storage.StatusDeepSleep,
			apply: func(ctx context.Context, t *testing.T, store *storage.Store, server *storage.Server) error {
				batch, err := store.OpenUpdateBatch(ctx, server.ID)
				require.NoError(t, err)
				require.NotNil(t, batch)
				return ApplyBatch(ctx, store, nil, server, *batch, nil)
			},
		},
		{
			name:   "next-restart batch",
			window: storage.UpdateWindowNextRestart,
			status: storage.StatusRunning,
			apply: func(ctx context.Context, t *testing.T, store *storage.Store, server *storage.Server) error {
				_, err := CommitNextRestartFiles(ctx, store, server, nil)
				return err
			},
		},
	} {
		t.Run(publication.name, func(t *testing.T) {
			ctx := context.Background()
			store, data := newStageStore(t)
			server := &storage.Server{
				ID: "legacy-identity", Name: "legacy-identity", DataPath: data, Status: publication.status,
			}
			require.NoError(t, store.CreateServer(ctx, server))
			live := filepath.Join(data, "mods", "mod.jar")
			require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
			require.NoError(t, os.WriteFile(live, []byte("installed"), 0o644))

			var at *time.Time
			if publication.window == storage.UpdateWindowAtTime {
				when := time.Now().Add(-time.Minute).UTC()
				at = &when
			}
			batchID := openBatch(t, store, server.ID, publication.window, at, 0)
			stageReviewedModRow(t, store, data, batchID, server, "candidate")
			require.NoError(t, store.DB().Model(&storage.StagedContentChange{}).
				Where("server_id = ?", server.ID).Updates(map[string]any{
				"approved_provider": "", "approved_project_id": "",
				"approved_version_id": "", "approved_filename": "",
			}).Error)

			err := publication.apply(ctx, t, store, server)
			require.ErrorIs(t, err, storage.ErrStagedModApprovalIdentityMissing)
			require.Contains(t, err.Error(), "re-review and re-stage")

			got, readErr := os.ReadFile(live)
			require.NoError(t, readErr)
			require.Equal(t, "installed", string(got))
		})
	}
}
