// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/staging"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/require"
)

func newRuntimeTargetFixture(t *testing.T, id string) (*storage.Store, *storage.Server, *ModService, *ConfigService) {
	t.Helper()
	store, server, um := newContentStageFixture(t, id, storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))
	modSvc := NewModService(store, nil, um, logger.New())
	modSvc.SetModUpdateLookup(newPreviewFixture())
	modSvc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("next")})
	configSvc := NewConfigService(store, nil, nil, nil, logger.New())
	return store, server, modSvc, configSvc
}

func TestDirectRuntimeConfigChangeRefusedWhileReviewedModStaged(t *testing.T) {
	for _, change := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "version", key: "version", value: "1.21.4"},
		{name: "loader type", key: "type", value: "forge"},
	} {
		t.Run(change.name, func(t *testing.T) {
			ctx := context.Background()
			store, server, modSvc, configSvc := newRuntimeTargetFixture(t, "runtime-direct-"+strings.ReplaceAll(change.name, " ", "-"))
			res, err := modSvc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
			require.NoError(t, err)
			require.Equal(t, "staged", res.Msg.GetStagedUpdates()[0].GetOutcome())

			_, err = configSvc.UpdateServerConfig(ctx, connect.NewRequest(&v1.UpdateServerConfigRequest{
				ServerId: server.ID, Updates: map[string]string{change.key: change.value},
			}))
			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			require.Contains(t, err.Error(), "staged reviewed mod")
			require.Contains(t, err.Error(), "review")

			live, err := os.ReadFile(filepath.Join(server.DataPath, "mods", "mod.jar"))
			require.NoError(t, err)
			require.Equal(t, "installed", string(live), "a refused runtime-target save must preserve live bytes")
			cfg, err := store.GetServerConfig(ctx, server.ID)
			require.NoError(t, err)
			require.Equal(t, "1.21.1", *cfg.Version)
			require.True(t, strings.EqualFold("fabric", *cfg.Type))
		})
	}
}

// A direct runtime change that lands after the candidate preview but before the
// staged row is admitted must not be published against the stale target.
func TestDirectRuntimeChangeDuringReviewedModStagingIsRefused(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "runtime-direct-during-stage", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	live := filepath.Join(mods, "mod.jar")
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o600))

	configSvc := NewConfigService(store, nil, nil, nil, logger.New())
	var directErr error
	fetcher := &modUpdateFetchByFileFixture{
		data: map[string][]byte{"mod-2.jar": []byte("next")},
		onFetch: func(string) {
			_, directErr = configSvc.UpdateServerConfig(ctx, connect.NewRequest(&v1.UpdateServerConfigRequest{
				ServerId: server.ID, Updates: map[string]string{"version": "1.21.4"},
			}))
		},
	}
	modSvc := NewModService(store, nil, um, logger.New())
	modSvc.SetModUpdateLookup(newPreviewFixture())
	modSvc.SetModCandidateFetcher(fetcher)

	_, err := modSvc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, directErr, "the direct save is allowed before a reviewed row exists")
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "%v", err)
	require.Contains(t, err.Error(), "runtime target changed")
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, batch, "a refused admission must not leave an open batch")
	body, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "installed", string(body))
}

func TestReviewedModPublicationSucceedsWithUnchangedRuntimeTarget(t *testing.T) {
	ctx := context.Background()
	store, server, modSvc, _ := newRuntimeTargetFixture(t, "runtime-publish-unchanged")
	res, err := modSvc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Equal(t, "staged", res.Msg.GetStagedUpdates()[0].GetOutcome())

	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	rows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "fabric", rows[0].ReviewedRuntimeLoader)
	require.Equal(t, "1.21.1", rows[0].ReviewedRuntimeVersion)

	commit, err := staging.CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, commit)
	commit.Finish()

	live, err := os.ReadFile(filepath.Join(server.DataPath, "mods", "mod.jar"))
	require.NoError(t, err)
	require.Equal(t, "next", string(live), "an unchanged target publishes the reviewed candidate")
}

func TestNonRuntimeConfigChangeAllowedWithStagedReviewedMod(t *testing.T) {
	for _, change := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "motd", key: "motd", value: "hello"},
		{name: "memory", key: "memory", value: "2048"},
	} {
		t.Run(change.name, func(t *testing.T) {
			ctx := context.Background()
			store, server, modSvc, configSvc := newRuntimeTargetFixture(t, "runtime-nonruntime-"+change.name)
			res, err := modSvc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
			require.NoError(t, err)
			require.Equal(t, "staged", res.Msg.GetStagedUpdates()[0].GetOutcome())

			_, err = configSvc.UpdateServerConfig(ctx, connect.NewRequest(&v1.UpdateServerConfigRequest{
				ServerId: server.ID, Updates: map[string]string{change.key: change.value},
			}))
			require.NoError(t, err, "a non-runtime config change must not invalidate the reviewed mod")

			commit, err := staging.CommitNextRestartFiles(ctx, store, server, nil)
			require.NoError(t, err)
			require.NotNil(t, commit)
			commit.Finish()
			live, err := os.ReadFile(filepath.Join(server.DataPath, "mods", "mod.jar"))
			require.NoError(t, err)
			require.Equal(t, "next", string(live))
		})
	}
}

func TestPublicationRefusesRuntimeTargetMovedOutOfBand(t *testing.T) {
	ctx := context.Background()
	store, server, modSvc, _ := newRuntimeTargetFixture(t, "runtime-publish-moved")
	res, err := modSvc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Equal(t, "staged", res.Msg.GetStagedUpdates()[0].GetOutcome())

	// Simulate a writer that bypasses SaveServerConfig's admission guard. The
	// durable row target must still stop publication before any live write.
	require.NoError(t, store.DB().Model(&storage.ServerConfig{}).
		Where("server_id = ?", server.ID).Update("version", "1.21.4").Error)

	commit, err := staging.CommitNextRestartFiles(ctx, store, server, nil)
	require.ErrorIs(t, err, storage.ErrStagedModRuntimeTargetChanged)
	require.Nil(t, commit)
	live, err := os.ReadFile(filepath.Join(server.DataPath, "mods", "mod.jar"))
	require.NoError(t, err)
	require.Equal(t, "installed", string(live), "a moved target must preserve live bytes")
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	rows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, storage.StagedStatusStaged, rows[0].Status)
}
