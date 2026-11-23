// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package staging

import (
	"context"
	"crypto/sha512"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
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

func TestApplyRefusesStagedFileWhenLiveDigestChanged(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "digest", Name: "digest", DataPath: data, Status: storage.StatusDeepSleep}
	require.NoError(t, store.CreateServer(ctx, server))
	live := filepath.Join(data, "mod.jar")
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o644))

	when := time.Now().Add(-time.Minute).UTC()
	batchID := openBatch(t, store, server.ID, storage.UpdateWindowAtTime, &when, 0)
	expected := fmt.Sprintf("%x", sha512.Sum512([]byte("installed")))
	stageFileWithPrecondition(t, store, data, batchID, server.ID, "mod.jar", "candidate", expected)
	require.NoError(t, os.WriteFile(live, []byte("edited-after-staging"), 0o644))

	published := false
	err := Sweep(ctx, store, nil, time.Now(), nil, func(context.Context, *storage.Server, Plan) error {
		published = true
		return nil
	})
	require.ErrorContains(t, err, "changed after staging")
	require.False(t, published)

	got, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "edited-after-staging", string(got), "a stale staged copy must not overwrite the edited live file")
	staged := filepath.Join(data, ".carbon-panel", "staged", server.ID+"-mod.jar", "mod.jar")
	require.FileExists(t, staged, "the refused candidate must remain staged")
	rows, err := store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, storage.StagedStatusStaged, rows[0].Status)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open, "the refused batch must remain open for operator action")
}

func TestApplyBatchRefusesIncompatibleStagedModCandidate(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{
		ID: "candidate-batch", Name: "candidate-batch", DataPath: data,
		MCVersion: "1.21.1", ModLoader: storage.ModLoaderFabric, Status: storage.StatusDeepSleep,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	live := filepath.Join(data, "mods", "mod.jar")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o644))

	when := time.Now().Add(-time.Minute).UTC()
	batchID := openBatch(t, store, server.ID, storage.UpdateWindowAtTime, &when, 0)
	stageReviewedModCandidate(t, store, data, batchID, server, "candidate")
	useModCandidateProvider(t, false)

	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	published := false
	err = ApplyBatch(ctx, store, nil, server, *batch, func(context.Context, *storage.Server, Plan) error {
		published = true
		return nil
	})
	require.ErrorContains(t, err, "not compatible with loader fabric and game version 1.21.1")
	require.False(t, published)

	got, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "installed", string(got), "an incompatible candidate must not replace the live file")
	rows, err := store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, storage.StagedStatusStaged, rows[0].Status)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open, "the refused candidate must remain staged for operator action")
}

func TestApplyBatchPublishesCompatibleStagedModCandidate(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{
		ID: "candidate-compatible", Name: "candidate-compatible", DataPath: data,
		MCVersion: "1.21.1", ModLoader: storage.ModLoaderFabric, Status: storage.StatusDeepSleep,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	live := filepath.Join(data, "mods", "mod.jar")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o644))

	when := time.Now().Add(-time.Minute).UTC()
	batchID := openBatch(t, store, server.ID, storage.UpdateWindowAtTime, &when, 0)
	stageReviewedModCandidate(t, store, data, batchID, server, "candidate")
	useModCandidateProvider(t, true)

	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.NoError(t, ApplyBatch(ctx, store, nil, server, *batch, nil))

	got, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "candidate", string(got))
	rows, err := store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
}

func TestApplyBatchRefusesStagedModWithoutCandidateMetadata(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{
		ID: "candidate-uncovered", Name: "candidate-uncovered", DataPath: data,
		MCVersion: "1.21.1", ModLoader: storage.ModLoaderFabric, Status: storage.StatusDeepSleep,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	live := filepath.Join(data, "mods", "mod.jar")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o644))

	when := time.Now().Add(-time.Minute).UTC()
	batchID := openBatch(t, store, server.ID, storage.UpdateWindowAtTime, &when, 0)
	id := server.ID + "-mod.jar"
	stageRel := ".carbon-panel/staged/" + id + "/mod.jar"
	require.NoError(t, os.MkdirAll(filepath.Join(data, ".carbon-panel", "staged", id), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(data, ".carbon-panel", "staged", id, "mod.jar"), []byte("candidate"), 0o644))
	require.NoError(t, store.DB().Create(&storage.StagedContentChange{
		ID: id, ServerID: server.ID, BatchID: batchID,
		Kind: storage.StagedKindMod, Action: storage.StagedActionReplace,
		TargetPath: "mods/mod.jar", StagePath: stageRel, FileName: "mod.jar",
		ExpectedLiveSha512:   fmt.Sprintf("%x", sha512.Sum512([]byte("installed"))),
		CandidateGameVersion: server.MCVersion,
		CandidateLoader:      "fabric",
		Status:               storage.StagedStatusStaged,
	}).Error)

	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	err = ApplyBatch(ctx, store, nil, server, *batch, nil)
	require.ErrorContains(t, err, "missing its compatibility metadata")
	got, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "installed", string(got))
	rows, err := store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, storage.StagedStatusStaged, rows[0].Status)
}

func TestCommitNextRestartFilesRefusesIncompatibleStagedModCandidate(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{
		ID: "candidate-next", Name: "candidate-next", DataPath: data,
		MCVersion: "1.21.1", ModLoader: storage.ModLoaderFabric, Status: storage.StatusRunning,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	live := filepath.Join(data, "mods", "mod.jar")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o644))

	batchID := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageReviewedModCandidate(t, store, data, batchID, server, "candidate")
	useModCandidateProvider(t, false)

	undo, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.ErrorContains(t, err, "not compatible with loader fabric and game version 1.21.1")
	require.Nil(t, undo)

	got, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "installed", string(got), "an incompatible candidate must not replace the live file")
	rows, err := store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, storage.StagedStatusStaged, rows[0].Status)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open, "the refused candidate must remain staged for operator action")
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

func stageReviewedModCandidate(t *testing.T, store *storage.Store, dataRoot, batchID string, server *storage.Server, candidateBody string) {
	t.Helper()
	ctx := context.Background()
	id := server.ID + "-mod.jar"
	stageRel := ".carbon-panel/staged/" + id + "/mod.jar"
	require.NoError(t, os.MkdirAll(filepath.Join(dataRoot, ".carbon-panel", "staged", id), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataRoot, ".carbon-panel", "staged", id, "mod.jar"), []byte(candidateBody), 0o644))
	acc, err := nodestorage.NewLocal(dataRoot)
	require.NoError(t, err)
	defer func() { _ = acc.Close() }()
	require.NoError(t, WriteModCandidateMetadata(ctx, acc, stageRel, ModCandidateMetadata{
		Provider:    "modrinth",
		VersionID:   "release",
		ProjectID:   "project",
		SHA512:      fmt.Sprintf("%x", sha512.Sum512([]byte(candidateBody))),
		Filename:    "mod.jar",
		Loader:      "fabric",
		GameVersion: server.MCVersion,
	}))
	require.NoError(t, store.DB().Create(&storage.StagedContentChange{
		ID:                 id,
		ServerID:           server.ID,
		BatchID:            batchID,
		Kind:               storage.StagedKindMod,
		Action:             storage.StagedActionReplace,
		TargetPath:         filepath.ToSlash(filepath.Join("mods", "mod.jar")),
		StagePath:          stageRel,
		FileName:           "mod.jar",
		ExpectedLiveSha512:   fmt.Sprintf("%x", sha512.Sum512([]byte("installed"))),
		CandidateGameVersion: server.MCVersion,
		CandidateLoader:      "fabric",
		Status:               storage.StagedStatusStaged,
	}).Error)
}

func useModCandidateProvider(t *testing.T, compatible bool) {
	t.Helper()
	candidateHash := fmt.Sprintf("%x", sha512.Sum512([]byte("candidate")))
	version := modrinth.Version{
		ID: "release", ProjectID: "project", VersionType: "release", Status: "listed",
		Loaders: []string{"fabric"}, GameVersions: []string{"1.21.1"},
		Files: []modrinth.File{{Filename: "mod.jar", Primary: true, Hashes: modrinth.Hashes{SHA512: candidateHash}}},
	}
	if !compatible {
		version.Loaders = []string{"forge"}
	}
	project := modrinth.ProjectDetails{ID: "project", ServerSide: "required"}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version/release":
			_ = json.NewEncoder(w).Encode(version)
		case "/projects":
			_ = json.NewEncoder(w).Encode([]modrinth.ProjectDetails{project})
		default:
			http.NotFound(w, r)
		}
	}))
	oldBaseURL := modrinth.BaseURL
	modrinth.BaseURL = provider.URL
	t.Cleanup(func() {
		modrinth.BaseURL = oldBaseURL
		provider.Close()
	})
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
	stageFileWithPrecondition(t, store, dataRoot, batchID, serverID, name, body, "")
}

func stageFileWithPrecondition(t *testing.T, store *storage.Store, dataRoot, batchID, serverID, name, body, expectedLiveSha512 string) {
	t.Helper()
	id := serverID + "-" + name
	stageRel := ".carbon-panel/staged/" + id + "/" + name
	require.NoError(t, os.MkdirAll(filepath.Join(dataRoot, ".carbon-panel", "staged", id), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataRoot, ".carbon-panel", "staged", id, name), []byte(body), 0o644))
	require.NoError(t, store.DB().Create(&storage.StagedContentChange{
		ID:                 id,
		ServerID:           serverID,
		BatchID:            batchID,
		Kind:               storage.StagedKindFile,
		Action:             storage.StagedActionInstall,
		TargetPath:         name,
		StagePath:          stageRel,
		FileName:           name,
		ExpectedLiveSha512: expectedLiveSha512,
		Status:             storage.StagedStatusStaged,
	}).Error)
}
