// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/athNdev/carbon-panel/internal/datawrite"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModpackUpdateRefusedStageKeepsFetchedUpdateDiscoverable is the
// regression for resetting the clone cache to FETCH_HEAD before staging. A
// refused stage then left HEAD equal to the fetched commit, so the next run
// reported "no updates" and lost the fetched update. The fetched commit is now
// persisted until its stage (or sync) completes.
func TestModpackUpdateRefusedStageKeepsFetchedUpdateDiscoverable(t *testing.T) {
	remoteRepo := createGitRepo(t)
	t.Cleanup(func() { _ = os.RemoveAll(remoteRepo) })

	serverDir, err := os.MkdirTemp("", "server-data-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(serverDir) })

	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	server := &storage.Server{
		ID:       uuid.New().String(),
		Name:     "modpack-pending",
		DataPath: serverDir,
		Status:   storage.StatusRunning,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	s := NewScheduler(store, nil, nil, nil, nil, logger.New())

	origClone, origFetch := cloneFn, fetchFn
	t.Cleanup(func() { cloneFn, fetchFn = origClone, origFetch })
	cloneFn = func(ctx context.Context, _ string, _ string, dir string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", "clone", "-b", "main", "--", remoteRepo, dir)
		return cmd.CombinedOutput()
	}
	fetchFn = func(ctx context.Context, dir, branch string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "origin", "--", branch)
		return cmd.CombinedOutput()
	}

	cfg := ModpackUpdateTaskConfig{
		GitURL:         "https://example.invalid/modpack.git",
		Branch:         "main",
		StageIntoBatch: true,
	}
	cfgJSON, err := json.Marshal(cfg)
	require.NoError(t, err)
	task := &storage.ScheduledTask{
		ID:       uuid.New().String(),
		ServerID: server.ID,
		Name:     "git-modpack-pending",
		TaskType: storage.TaskTypeModpackUpdate,
		Config:   string(cfgJSON),
	}

	out, err := s.executeModpackUpdateTask(ctx, server, task)
	require.NoError(t, err)
	require.Contains(t, out, "staged into the open batch")

	// The first update was published/closed before the next scheduled run, as
	// a real apply would close the batch.
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.NoError(t, store.CloseUpdateBatch(ctx, batch.ID, storage.UpdateBatchApplied))

	// Advance the remote repository.
	require.NoError(t, os.WriteFile(filepath.Join(remoteRepo, "config", "modpack.json"), []byte(`{"version": "1.1.0"}`), 0644))
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "Bump modpack version to 1.1.0"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = remoteRepo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		require.NoError(t, cmd.Run(), "git %v", args)
	}

	// A competing exclusive owner refuses the stage after the fetch and reset.
	lease, err := datawrite.AcquireExclusive(ctx, server.ID)
	require.NoError(t, err)
	_, err = s.executeModpackUpdateTask(ctx, server, task)
	require.ErrorContains(t, err, "stage modpack into batch")
	lease.Release()

	// The next run must still discover the fetched update instead of reporting
	// a false no-update.
	out, err = s.executeModpackUpdateTask(ctx, server, task)
	require.NoError(t, err)
	assert.NotContains(t, out, "is up to date", "a refused stage must not look like no updates")
	assert.Contains(t, out, "staged into the open batch")

	batch, err = store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	rows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, storage.StagedKindModpack, rows[0].Kind)
	body, err := os.ReadFile(filepath.Join(serverDir, filepath.FromSlash(rows[0].StagePath), "config", "modpack.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"version": "1.1.0"}`, string(body), "the retried stage must carry the fetched update")
}
