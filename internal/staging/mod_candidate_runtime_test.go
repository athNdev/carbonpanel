// SPDX-License-Identifier: MPL-2.0
package staging

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/stretchr/testify/require"
)

func TestReviewedNextRestartRefusesLegacyMixedRuntimeBatch(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{
		ID: "legacy-runtime-mix", Name: "legacy-runtime-mix", DataPath: data,
		MCVersion: "1.21.1", ModLoader: storage.ModLoaderFabric, Status: storage.StatusStopped,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	cfg, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"version": "1.21.4"}, storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	live := filepath.Join(data, "mods", "mod.jar")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o644))
	// This helper writes a reviewed row directly, bypassing current admission.
	stageReviewedModRow(t, store, data, cfg.BatchID, server, "candidate")

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.ErrorIs(t, err, storage.ErrStagedModVersionChange)
	require.Nil(t, commit)
	body, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "installed", string(body))
	rows, err := store.ListBatchContent(ctx, cfg.BatchID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}
