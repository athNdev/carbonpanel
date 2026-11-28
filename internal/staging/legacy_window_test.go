// SPDX-License-Identifier: MPL-2.0
package staging

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A legacy batch stored the config row's apply mode as its window. Restart
// selection must still recognize "on_restart" and publish its content and
// config instead of silently treating it as another window.
func TestLegacyOnRestartBatchPublishesOnRestart(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{
		ID: "legacy-on-restart", Name: "legacy-on-restart", DataPath: data,
		MCVersion: "1.21.1", ModLoader: storage.ModLoaderFabric, Status: storage.StatusRunning,
	}
	require.NoError(t, store.CreateServer(ctx, server))

	cfg, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "legacy"}, storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	require.NotEmpty(t, cfg.BatchID)
	stageFile(t, store, data, cfg.BatchID, server.ID, "legacy.jar", "candidate")
	// The schema older versions wrote. The config row's apply mode is already
	// on_restart; only the batch window kept the old spelling.
	require.NoError(t, store.DB().Model(&storage.ServerUpdateBatch{}).Where("id = ?", cfg.BatchID).
		Update("window", string(storage.StagedApplyOnRestart)).Error)

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, commit)
	require.Equal(t, 1, commit.ConfigSnapshot(), "the legacy batch config must join the restart")
	body, err := os.ReadFile(filepath.Join(data, "legacy.jar"))
	require.NoError(t, err)
	require.Equal(t, "candidate", string(body))

	_, err = commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	live, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, live.MOTD)
	require.Equal(t, "legacy", *live.MOTD)
	commit.Finish()
}

func legacyRuntimeConfigRow(t *testing.T, store *storage.Store, serverID string) *storage.StagedConfigChange {
	t.Helper()
	row := &storage.StagedConfigChange{
		ID:        uuid.NewString(),
		ServerID:  serverID,
		BatchID:   "",
		Payload:   `{"version":"1.21.4"}`,
		ApplyMode: storage.StagedApplyOnRestart,
		Status:    storage.StagedStatusStaged,
		CreatedAt: time.Now(),
	}
	require.True(t, json.Valid([]byte(row.Payload)), "legacy fixture payload must be valid JSON, not an escaped raw string: %s", row.Payload)
	require.NoError(t, store.DB().Create(row).Error)
	return row
}

// A legacy unbatched runtime config is part of the same restart as an owned
// reviewed batch. Publication must refuse it before any live jar write,
// whichever row was written first.
func TestLegacyUnbatchedRuntimeConfigBlocksReviewedPublication(t *testing.T) {
	for _, order := range []string{"content-then-config", "config-then-content"} {
		t.Run(order, func(t *testing.T) {
			ctx := context.Background()
			store, data := newStageStore(t)
			server := &storage.Server{
				ID: "legacy-runtime-" + order, Name: "legacy-runtime", DataPath: data,
				MCVersion: "1.21.1", ModLoader: storage.ModLoaderFabric, Status: storage.StatusRunning,
			}
			require.NoError(t, store.CreateServer(ctx, server))
			live := filepath.Join(data, "mods", "mod.jar")
			require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
			require.NoError(t, os.WriteFile(live, []byte("installed"), 0o644))

			batchID := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
			if order == "content-then-config" {
				stageReviewedModRow(t, store, data, batchID, server, "candidate")
				legacyRuntimeConfigRow(t, store, server.ID)
			} else {
				legacyRuntimeConfigRow(t, store, server.ID)
				stageReviewedModRow(t, store, data, batchID, server, "candidate")
			}

			commit, err := CommitNextRestartFiles(ctx, store, server, nil)
			require.ErrorIs(t, err, storage.ErrStagedModVersionChange)
			require.Nil(t, commit)
			body, err := os.ReadFile(live)
			require.NoError(t, err)
			require.Equal(t, "installed", string(body), "the live jar must not be written before the conflict is refused")
			rows, err := store.ListBatchContent(ctx, batchID)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, storage.StagedStatusStaged, rows[0].Status)
		})
	}
}
