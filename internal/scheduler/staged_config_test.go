// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/datawrite"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/staging"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStagedStore is an in-memory stagedConfigStore for hook tests.
type fakeStagedStore struct {
	changes          []*storage.StagedConfigChange
	applied          []string
	rolledBack       []string
	failOn           map[string]bool
	publishedBatches map[string]bool
}

type fakeLifecycleDocker struct {
	result        *docker.RecreateContainerResult
	err           error
	starts        []string
	recreateCalls int
	resolveCalls  int
	resolvedID    string
	resolveErr    error
}

func (f *fakeLifecycleDocker) GetContainerStatus(context.Context, string) (storage.ServerStatus, error) {
	return storage.StatusStopped, nil
}

func (f *fakeLifecycleDocker) RecreateContainer(context.Context, string, *storage.Server, *storage.ServerConfig) (*docker.RecreateContainerResult, error) {
	f.recreateCalls++
	return f.result, f.err
}

func (f *fakeLifecycleDocker) ResolveContainer(context.Context, string) (*container.Summary, error) {
	f.resolveCalls++
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	if f.resolvedID == "" {
		return nil, docker.ErrContainerNotResolved
	}
	return &container.Summary{ID: f.resolvedID, State: container.StateRunning}, nil
}

func (f *fakeLifecycleDocker) StartContainer(_ context.Context, id string) error {
	f.starts = append(f.starts, id)
	return nil
}

func (f *fakeStagedStore) ListStagedConfigChanges(_ context.Context, serverID string, status storage.StagedStatus) ([]*storage.StagedConfigChange, error) {
	var out []*storage.StagedConfigChange
	for _, c := range f.changes {
		if c.ServerID == serverID && (status == "" || c.Status == status) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStagedStore) ListPendingRestartStagedConfigs(_ context.Context, serverID string, lastStarted *time.Time) ([]*storage.StagedConfigChange, error) {
	var out []*storage.StagedConfigChange
	for _, c := range f.changes {
		if c.ServerID != serverID || c.ApplyMode != storage.StagedApplyOnRestart || c.Status != storage.StagedStatusApplied || c.AppliedAt == nil {
			continue
		}
		if lastStarted != nil && !c.AppliedAt.After(*lastStarted) {
			continue
		}
		if c.BatchID != "" && f.publishedBatches[c.BatchID] {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeStagedStore) ApplyStagedConfigChange(_ context.Context, id string) (*storage.StagedConfigChange, error) {
	if f.failOn[id] {
		return nil, fmt.Errorf("boom")
	}
	for _, c := range f.changes {
		if c.ID == id {
			c.Status = storage.StagedStatusApplied
			f.applied = append(f.applied, id)
			return c, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (f *fakeStagedStore) RollbackStagedConfigChange(_ context.Context, id string) (*storage.StagedConfigChange, error) {
	for _, c := range f.changes {
		if c.ID == id {
			c.Status = storage.StagedStatusRolledBack
			f.rolledBack = append(f.rolledBack, id)
			return c, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func TestApplyOnRestartStagedConfigs_OnlyOnRestart(t *testing.T) {
	ctx := context.Background()
	store := &fakeStagedStore{changes: []*storage.StagedConfigChange{
		{ID: "a", ServerID: "srv-1", ApplyMode: storage.StagedApplyOnRestart, Status: storage.StagedStatusStaged},
		{ID: "b", ServerID: "srv-1", ApplyMode: storage.StagedApplyScheduled, Status: storage.StagedStatusStaged},
		{ID: "e", ServerID: "srv-1", ApplyMode: storage.StagedApplyAtTime, Status: storage.StagedStatusStaged},
		{ID: "f", ServerID: "srv-1", ApplyMode: storage.StagedApplyIdle, Status: storage.StagedStatusStaged},
		{ID: "c", ServerID: "srv-1", ApplyMode: storage.StagedApplyOnRestart, Status: storage.StagedStatusApplied},
		{ID: "d", ServerID: "srv-2", ApplyMode: storage.StagedApplyOnRestart, Status: storage.StagedStatusStaged},
	}}

	n, err := ApplyOnRestartStagedConfigs(ctx, store, "srv-1")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"a"}, store.applied)
}

func TestApplyDueStagedConfigsDoesNotMerge(t *testing.T) {
	(&Scheduler{}).applyDueStagedConfigs(context.Background())
}

// TestApplyDueStagedConfigsLeavesCronRowsPending proves the scheduler has no
// production cron consumer for retained legacy scheduled-config rows. They stay
// staged for manual apply/discard or explicit operator conversion instead of
// being merged silently on a tick.
func TestApplyDueStagedConfigsLeavesCronRowsPending(t *testing.T) {
	ctx := context.Background()
	s, store := newTriggerTestScheduler(t)
	server := &storage.Server{ID: "srv-cron-pending", Name: "cron-pending", Status: storage.StatusStopped}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatalf("create server: %v", err)
	}
	if err := store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)); err != nil {
		t.Fatalf("save config: %v", err)
	}
	cron, err := store.StageConfigChange(ctx, server.ID, map[string]any{"motd": "nightly"}, storage.StagedApplyScheduled, "0 3 * * *")
	if err != nil {
		t.Fatalf("stage cron config: %v", err)
	}

	for i := 0; i < 2; i++ {
		s.applyDueStagedConfigs(ctx)
	}
	staged, err := store.GetStagedConfigChange(ctx, cron.ID)
	if err != nil {
		t.Fatalf("reload cron config: %v", err)
	}
	assert.Equal(t, storage.StagedStatusStaged, staged.Status)
	assert.Equal(t, storage.StagedApplyScheduled, staged.ApplyMode)
	cfg, err := store.GetServerConfig(ctx, server.ID)
	if err != nil {
		t.Fatalf("reload server config: %v", err)
	}
	if cfg.MOTD != nil {
		assert.NotEqual(t, "nightly", *cfg.MOTD, "a cron row must not be merged by a scheduler tick")
	}
}

func TestApplyOnRestartStagedConfigs_StopsOnError(t *testing.T) {
	ctx := context.Background()
	store := &fakeStagedStore{
		changes: []*storage.StagedConfigChange{
			{ID: "a", ServerID: "srv-1", ApplyMode: storage.StagedApplyOnRestart, Status: storage.StagedStatusStaged},
			{ID: "b", ServerID: "srv-1", ApplyMode: storage.StagedApplyOnRestart, Status: storage.StagedStatusStaged},
		},
		failOn: map[string]bool{"a": true},
	}

	n, err := ApplyOnRestartStagedConfigs(ctx, store, "srv-1")
	assert.Error(t, err)
	assert.Equal(t, 0, n)
}

func TestHasPendingRestartStagedConfigRecreate(t *testing.T) {
	ctx := context.Background()
	lastStarted := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	appliedAfterStart := lastStarted.Add(time.Second)
	store := &fakeStagedStore{changes: []*storage.StagedConfigChange{
		{ID: "pending", ServerID: "srv-1", ApplyMode: storage.StagedApplyOnRestart, Status: storage.StagedStatusApplied, AppliedAt: &appliedAfterStart},
		{ID: "scheduled", ServerID: "srv-1", ApplyMode: storage.StagedApplyScheduled, Status: storage.StagedStatusApplied, AppliedAt: &appliedAfterStart},
		{ID: "old", ServerID: "srv-1", ApplyMode: storage.StagedApplyOnRestart, Status: storage.StagedStatusApplied, AppliedAt: &lastStarted},
	}}
	pending, err := HasPendingRestartStagedConfigRecreate(ctx, store, "srv-1", &lastStarted)
	require.NoError(t, err)
	assert.True(t, pending)
	pending, err = HasPendingRestartStagedConfigRecreate(ctx, store, "srv-1", nil)
	require.NoError(t, err)
	assert.True(t, pending)
	store.changes = store.changes[1:]
	pending, err = HasPendingRestartStagedConfigRecreate(ctx, store, "srv-1", &lastStarted)
	require.NoError(t, err)
	assert.False(t, pending)
}

func TestHasPendingRestartStagedConfigRecreateUsesAcknowledgementEvidence(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{ID: "srv-ack-evidence", Name: "srv-ack-evidence", DataPath: t.TempDir()}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	cfg, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "ack"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	_, err = store.ApplyStagedConfigChange(ctx, cfg.ID)
	require.NoError(t, err)
	require.NoError(t, store.MarkUpdateBatchPublished(ctx, cfg.BatchID))
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.NotNil(t, batch.PublishedAt)

	// An applied row older than the acknowledgement was part of it.
	before := batch.PublishedAt.Add(-time.Second)
	require.NoError(t, store.DB().Model(&storage.StagedConfigChange{}).
		Where("id = ?", cfg.ID).Update("applied_at", before).Error)
	pending, err := HasPendingRestartStagedConfigRecreate(ctx, store, server.ID, nil)
	require.NoError(t, err)
	require.False(t, pending)

	// A row applied after the acknowledgement was not covered by it.
	after := batch.PublishedAt.Add(time.Second)
	require.NoError(t, store.DB().Model(&storage.StagedConfigChange{}).
		Where("id = ?", cfg.ID).Update("applied_at", after).Error)
	pending, err = HasPendingRestartStagedConfigRecreate(ctx, store, server.ID, nil)
	require.NoError(t, err)
	require.True(t, pending)
}

func TestRecreateForPendingStagedConfigPersistsPartialContainerID(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{
		ID: "srv-1", Name: "srv-1", DataPath: t.TempDir(),
		ContainerID: "old-id", Status: storage.StatusRunning,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	cfg, err := store.StageConfigChange(ctx, server.ID, map[string]any{"motd": "staged"},
		storage.StagedApplyOnRestart, "")
	require.NoError(t, err)
	client := &fakeLifecycleDocker{
		result: &docker.RecreateContainerResult{NewContainerID: "replacement-id", WasRunning: true},
		err:    fmt.Errorf("replacement start failed"),
	}

	handled, err := recreateForPendingStagedConfig(ctx, store, client, nil, server, true, true, nil)
	require.True(t, handled)
	require.ErrorContains(t, err, "retained")
	stored, err := store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	assert.Equal(t, "replacement-id", stored.ContainerID)
	assert.Equal(t, storage.StatusError, stored.Status)
	assert.Nil(t, stored.LastStarted, "failed recreate must leave staged recovery pending")
	assert.Empty(t, client.starts)
	record, err := store.GetStagedConfigChange(ctx, cfg.ID)
	require.NoError(t, err)
	assert.Equal(t, storage.StagedStatusApplied, record.Status,
		"a failed replacement may contain the staged config, so it is retained")
}

func TestRecreatePreflightFailureRestagesStagedConfig(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{
		ID: "srv-1", Name: "srv-1", DataPath: t.TempDir(),
		ContainerID: "old-id", Status: storage.StatusRunning,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	cfg, err := store.StageConfigChange(ctx, server.ID, map[string]any{"motd": "staged"},
		storage.StagedApplyOnRestart, "")
	require.NoError(t, err)
	client := &fakeLifecycleDocker{
		result: &docker.RecreateContainerResult{NewContainerID: "old-id"},
		err:    fmt.Errorf("%w: pull failed", docker.ErrRecreatePreflight),
	}

	handled, err := recreateForPendingStagedConfig(ctx, store, client, nil, server, true, true, nil)
	require.True(t, handled)
	require.Error(t, err)
	assert.Empty(t, client.starts)
	record, err := store.GetStagedConfigChange(ctx, cfg.ID)
	require.NoError(t, err)
	assert.Equal(t, storage.StagedStatusStaged, record.Status,
		"a typed preflight proves the old container was not mutated")
}

func TestPrepareDeepSleepWakeRecreatesWithoutStartingOrAdvancingLastStarted(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{
		ID: "srv-1", Name: "srv-1", DataPath: t.TempDir(),
		ContainerID: "old-id", Status: storage.StatusDeepSleep,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	cfg, err := store.StageConfigChange(ctx, server.ID, map[string]any{"motd": "staged"},
		storage.StagedApplyOnRestart, "")
	require.NoError(t, err)
	client := &fakeLifecycleDocker{result: &docker.RecreateContainerResult{NewContainerID: "replacement-id"}}

	handled, err := recreateForPendingStagedConfig(ctx, store, client, nil, server, false, false, nil)
	require.NoError(t, err)
	require.True(t, handled)
	assert.Equal(t, "replacement-id", server.ContainerID)
	stored, err := store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	assert.Equal(t, "replacement-id", stored.ContainerID)
	assert.Empty(t, client.starts, "DeepSleepManager owns the actual start")
	assert.Nil(t, stored.LastStarted, "wake preparation must not mark a start before Docker is ready")
	record, err := store.GetStagedConfigChange(ctx, cfg.ID)
	require.NoError(t, err)
	assert.Equal(t, storage.StagedStatusApplied, record.Status)
}

func TestFreshWakePreflightCompensatesWithoutPriorEvidence(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{
		ID: "wake-fresh", Name: "wake-fresh", DataPath: t.TempDir(),
		Status: storage.StatusDeepSleep, ContainerID: "old",
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	cfg, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "fresh"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)

	client := &fakeLifecycleDocker{
		result: &docker.RecreateContainerResult{NewContainerID: "old"},
		err:    fmt.Errorf("%w: image pull unavailable", docker.ErrRecreatePreflight),
	}
	handled, err := prepareStagedLifecycle(ctx, store, client, nil, server, false, false, nil, nil)
	require.True(t, handled)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "retained")

	reverted, err := store.GetStagedConfigChange(ctx, cfg.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, reverted.Status,
		"a typed preflight with no prior applied evidence proves compensation is safe")
	live, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, live.MOTD)
}

func TestConsecutiveWakePreparationRetainsAppliedSnapshotWithoutStatus(t *testing.T) {
	for _, status := range []storage.ServerStatus{storage.StatusDeepSleep, storage.StatusStopped} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			store := setupTestStore(t)
			t.Cleanup(func() { _ = store.Close() })
			server := &storage.Server{
				ID: "wake-prior", Name: "wake-prior", DataPath: t.TempDir(),
				Status: storage.StatusDeepSleep, ContainerID: "old",
			}
			require.NoError(t, store.CreateServer(ctx, server))
			require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
			cfg, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "applied"},
				storage.UpdateWindowNextRestart, nil, 0)
			require.NoError(t, err)

			// First wake: prepare applies the snapshot and recreates the stopped
			// container, then the real WakeCompletion reports that Docker started
			// but the RUNNING persistence failed and a verified stop restored
			// DEEPSLEEP.
			var first *staging.RestartPublication
			client := &fakeLifecycleDocker{result: &docker.RecreateContainerResult{NewContainerID: "replacement"}}
			handled, err := prepareStagedLifecycle(ctx, store, client, nil, server, false, false, nil,
				func(p *staging.RestartPublication) { first = p })
			require.NoError(t, err)
			require.True(t, handled)
			require.NotNil(t, first)
			require.Equal(t, "replacement", server.ContainerID)
			require.NoError(t, store.UpdateServerStatus(ctx, server.ID, storage.StatusDeepSleep))
			server.Status = storage.StatusDeepSleep
			// Use the production completion callback rather than assigning
			// RuntimeAttempted directly in the test.
			completion := stagedWakeCompletion(ctx, store, first)
			require.ErrorContains(t, completion(true, errors.New("failed to update server status to running")), "retained")

			applied, err := store.GetStagedConfigChange(ctx, cfg.ID)
			require.NoError(t, err)
			require.Equal(t, storage.StagedStatusApplied, applied.Status)
			live, err := store.GetServerConfig(ctx, server.ID)
			require.NoError(t, err)
			require.NotNil(t, live.MOTD)
			require.Equal(t, "applied", *live.MOTD)

			// The reconciler can move a retained ERROR to STOPPED; the next wake
			// must not read that mutable status as proof that the prior replacement
			// never embedded the snapshot.
			require.NoError(t, store.UpdateServerStatus(ctx, server.ID, status))
			stored, err := store.GetServer(ctx, server.ID)
			require.NoError(t, err)
			stored.LastStarted = nil
			require.Nil(t, stored.LastStarted)

			preflight := &fakeLifecycleDocker{
				result: &docker.RecreateContainerResult{NewContainerID: "replacement"},
				err:    fmt.Errorf("%w: image pull unavailable", docker.ErrRecreatePreflight),
			}
			handled, err = prepareStagedLifecycle(ctx, store, preflight, nil, stored, false, false, nil, nil)
			require.True(t, handled)
			require.Error(t, err)
			require.Empty(t, preflight.starts)

			retained, err := store.GetStagedConfigChange(ctx, cfg.ID)
			require.NoError(t, err)
			require.Equal(t, storage.StagedStatusApplied, retained.Status,
				"a new preflight cannot roll back an applied snapshot while status is %s", status)
			live, err = store.GetServerConfig(ctx, server.ID)
			require.NoError(t, err)
			require.NotNil(t, live.MOTD)
			require.Equal(t, "applied", *live.MOTD)
			lease, err := datawrite.TryShared(server.ID)
			require.NoError(t, err)
			lease.Release()
		})
	}
}

func TestAcknowledgedMixedRetryDoesNotRecreate(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	started := time.Now().Add(-time.Hour).Truncate(time.Second)
	server := &storage.Server{
		ID: "srv-mixed-retry", Name: "srv-mixed-retry", DataPath: t.TempDir(),
		ContainerID: "old-id", Status: storage.StatusRunning, LastStarted: &started,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	_, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "mixed"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	stagePath := ".carbon-panel/staged/mixed/mixed.txt"
	require.NoError(t, os.MkdirAll(filepath.Join(server.DataPath, filepath.FromSlash(".carbon-panel/staged/mixed")), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(server.DataPath, filepath.FromSlash(stagePath)), []byte("mixed"), 0o644))
	_, err = store.AddStagedContent(ctx, &storage.StagedContentChange{
		ServerID: server.ID, Kind: storage.StagedKindFile, Action: storage.StagedActionInstall,
		TargetPath: "mixed.txt", StagePath: stagePath, FileName: "mixed.txt",
	})
	require.NoError(t, err)

	// PersistStarted fails after the acknowledged publication, leaving an old
	// LastStarted and both content and config applied in the published batch.
	require.NoError(t, store.DB().Exec(
		`CREATE TRIGGER fail_starting BEFORE UPDATE OF status ON servers
		 WHEN NEW.status = 'starting' BEGIN SELECT RAISE(ABORT, 'status save unavailable'); END`).Error)
	first := &fakeLifecycleDocker{result: &docker.RecreateContainerResult{NewContainerID: "replacement", WasRunning: true}}
	handled, err := recreateForPendingStagedConfig(ctx, store, first, nil, server, true, true, nil)
	require.True(t, handled)
	require.ErrorContains(t, err, "status could not be saved")
	require.Equal(t, 1, first.recreateCalls)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.NotNil(t, batch.PublishedAt)

	require.NoError(t, store.DB().Exec(`DROP TRIGGER fail_starting`).Error)
	stored, err := store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, "replacement", stored.ContainerID)
	retry := &fakeLifecycleDocker{result: &docker.RecreateContainerResult{NewContainerID: "unexpected"}}
	handled, err = recreateForPendingStagedConfig(ctx, store, retry, nil, stored, true, true, nil)
	require.NoError(t, err)
	require.False(t, handled, "an acknowledged batch owes cleanup, not another container")
	require.Zero(t, retry.recreateCalls)
	batch, err = store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, batch)
	body, err := os.ReadFile(filepath.Join(server.DataPath, "mixed.txt"))
	require.NoError(t, err)
	require.Equal(t, "mixed", string(body))
}

func TestSchedulerWakePathsRejectOvercommittedNode(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateNode(ctx, &storage.Node{
		ID: "small-node", Name: "Small Node", Host: "tcp://127.0.0.1:2375",
		Enabled: true, Status: storage.NodeStatusOnline, IsLocal: true,
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	server := &storage.Server{
		ID: "oversized-wake", Name: "Oversized Wake", NodeID: "small-node",
		Port: 25565, Memory: 6144, ContainerID: "container-id", Status: storage.StatusDeepSleep,
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	dockerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_ping":
			w.Header().Set("Api-Version", "1.45")
			w.Header().Set("Ostype", "linux")
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/info"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"MemTotal":4294967296}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer dockerAPI.Close()
	dockerClient, err := docker.NewClient("tcp://"+strings.TrimPrefix(dockerAPI.URL, "http://"), logger.New())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = dockerClient.Close() }()
	s := NewScheduler(store, dockerClient, nil, nil, nil, logger.New())

	if _, err := s.PrepareDeepSleepWake(ctx, server); err == nil || !strings.Contains(err.Error(), "lacks memory capacity") {
		t.Fatalf("PrepareDeepSleepWake error = %v, want memory capacity rejection", err)
	}
	if err := s.StartServerForBoot(ctx, server); err == nil || !strings.Contains(err.Error(), "lacks memory capacity") {
		t.Fatalf("StartServerForBoot error = %v, want memory capacity rejection", err)
	}
}

func TestRecreateForPendingStagedConfigRetainsStartFailure(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{
		ID: "srv-1", Name: "srv-1", DataPath: t.TempDir(),
		ContainerID: "old-id", Status: storage.StatusStopped,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	cfg, err := store.StageConfigChange(ctx, server.ID, map[string]any{"motd": "staged"},
		storage.StagedApplyOnRestart, "")
	require.NoError(t, err)
	client := &fakeLifecycleDocker{
		result: &docker.RecreateContainerResult{NewContainerID: "replacement-id-2", WasRunning: false},
	}
	// Make StartContainer fail
	failClient := &failingStartDocker{fakeLifecycleDocker: client, failOn: "replacement-id-2"}

	handled, err := recreateForPendingStagedConfig(ctx, store, failClient, nil, server, true, true, nil)
	require.True(t, handled)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start recreated server")
	assert.Contains(t, err.Error(), "retained")
	record, err := store.GetStagedConfigChange(ctx, cfg.ID)
	require.NoError(t, err)
	assert.Equal(t, storage.StagedStatusApplied, record.Status,
		"the stopped replacement already contains the staged config")
}

type failingStartDocker struct {
	*fakeLifecycleDocker
	failOn string
}

func (f *failingStartDocker) StartContainer(ctx context.Context, id string) error {
	if id == f.failOn {
		return fmt.Errorf("start failed for container %s", id)
	}
	return f.fakeLifecycleDocker.StartContainer(ctx, id)
}

func TestWakePreparationRetainsOwnershipUntilStartOutcome(t *testing.T) {
	ctx := context.Background()
	_, store := newTriggerTestScheduler(t)
	server := &storage.Server{ID: "wake-staged", Name: "wake-staged", DataPath: t.TempDir(), Status: storage.StatusDeepSleep, ContainerID: "old"}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	path := ".carbon-panel/staged/file/wake.txt"
	live := filepath.Join(server.DataPath, "wake.txt")
	staged := filepath.Join(server.DataPath, filepath.FromSlash(path))
	require.NoError(t, os.MkdirAll(filepath.Dir(staged), 0700))
	require.NoError(t, os.WriteFile(staged, []byte("candidate"), 0600))
	row, err := store.AddStagedContent(ctx, &storage.StagedContentChange{ServerID: server.ID, Kind: storage.StagedKindFile, Action: storage.StagedActionInstall, TargetPath: "wake.txt", StagePath: path, FileName: "wake.txt"})
	require.NoError(t, err)
	client := &fakeLifecycleDocker{result: &docker.RecreateContainerResult{NewContainerID: "replacement"}}
	var owner *staging.RestartPublication
	handled, err := prepareStagedLifecycle(ctx, store, client, nil, server, false, false, nil, func(p *staging.RestartPublication) { owner = p })
	require.NoError(t, err)
	require.True(t, handled)
	require.NotNil(t, owner)
	t.Cleanup(owner.Commit.Finish)
	require.FileExists(t, live)
	_, err = datawrite.TryShared(server.ID)
	require.ErrorIs(t, err, datawrite.ErrExclusiveHeld)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.True(t, owner.RuntimeAttempted, "a successful recreate mutated the runtime before the wake start")
	// Simulate a wake that failed before Start: the completion must not erase
	// the recreate mutation, or the stopped replacement outlives the rollback.
	require.ErrorContains(t, owner.Resolve(ctx, store, errors.New("wake start failed")), "retained")
	require.FileExists(t, live, "the stopped replacement already contains the staged file")
	current, err := store.GetStagedContent(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusApplied, current.Status)
}

// openNextRestartBatch creates an open, empty batch in the next-restart window
// so a test can attach a publication record without staged rows.
func openNextRestartBatch(t *testing.T, store *storage.Store, serverID string) string {
	t.Helper()
	placeholder, err := store.AddStagedContent(context.Background(), &storage.StagedContentChange{
		ServerID: serverID, Kind: storage.StagedKindFile, Action: storage.StagedActionInstall,
		TargetPath: "placeholder", StagePath: ".carbon-panel/staged/placeholder/placeholder", FileName: "placeholder",
	})
	require.NoError(t, err)
	require.NoError(t, store.DB().Where("id = ?", placeholder.ID).Delete(&storage.StagedContentChange{}).Error)
	require.NoError(t, store.DB().Model(&storage.ServerUpdateBatch{}).Where("id = ?", placeholder.BatchID).
		Update("window", string(storage.UpdateWindowNextRestart)).Error)
	return placeholder.BatchID
}

// TestDispatchedFileOnlyWakeVerifiesRecordedReplacement is the OSS-424/OSS-425
// regression for a wake that skipped identity verification. A file-only
// publication records its dispatch but owes no new container, so the wake path
// must still verify the recorded replacement instead of returning "no work";
// otherwise the deferred Resolve rejects the unverified dispatch and the server
// cannot wake through this path.
func TestDispatchedFileOnlyWakeVerifiesRecordedReplacement(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{
		ID: "wake-dispatched", Name: "wake-dispatched", DataPath: t.TempDir(),
		Status: storage.StatusDeepSleep, ContainerID: "replacement",
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch := openNextRestartBatch(t, store, server.ID)

	record, err := store.BeginUpdatePublication(ctx, batch, server.ID, "old")
	require.NoError(t, err)
	require.NoError(t, store.RecordUpdatePublicationSnapshot(ctx, batch, record.Attempt, "[]"))
	require.NoError(t, store.RecordUpdatePublicationDispatch(ctx, batch, record.Attempt, "[]", "[]"))
	require.NoError(t, store.RecordUpdatePublicationReplacement(ctx, batch, record.Attempt, "replacement"))

	client := &fakeLifecycleDocker{resolvedID: "replacement"}
	handled, err := prepareStagedLifecycle(ctx, store, client, nil, server, false, false, nil, nil)
	require.NoError(t, err, "an unverified file-only dispatch must be resolved by identity, not rejected")
	require.False(t, handled, "the caller's normal wake continues on the verified replacement")
	require.NotZero(t, client.resolveCalls, "the wake must reconcile the tracked identity")
	require.Zero(t, client.recreateCalls, "identity verification must not recreate the container")
	verified, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Contains(t, []storage.PublicationPhase{storage.PublicationVerified, storage.PublicationCleaned}, verified.Phase,
		"the recorded replacement identity was positively accepted")
	require.Equal(t, "replacement", verified.VerifiedContainerID)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open, "the verified file-only publication owes cleanup only")
}

// TestWakeEmptyBatchTakesNoWorkFastPath keeps the fast path for a batch with
// neither pending work nor an unverified dispatch: the wake must not touch
// Docker or leave a publication seal behind.
func TestWakeEmptyBatchTakesNoWorkFastPath(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{
		ID: "wake-noop", Name: "wake-noop", DataPath: t.TempDir(),
		Status: storage.StatusDeepSleep, ContainerID: "old",
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	openNextRestartBatch(t, store, server.ID)

	client := &fakeLifecycleDocker{resolvedID: "old"}
	var owner *staging.RestartPublication
	handled, err := prepareStagedLifecycle(ctx, store, client, nil, server, false, false, nil,
		func(p *staging.RestartPublication) { owner = p })
	require.NoError(t, err)
	require.False(t, handled)
	require.Nil(t, owner)
	require.Zero(t, client.resolveCalls)
	require.Zero(t, client.recreateCalls)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.Nil(t, open.SealedAt, "a no-work wake must not leave a publication seal")
}
