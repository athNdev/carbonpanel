// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/datawrite"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/staging"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func installedModID(serverID, fileName string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(serverID+fileName)).String()
}

func TestDirectModWritesAreRefusedDuringStagedPublication(t *testing.T) {
	ctx := context.Background()
	store, server, _ := newContentStageFixture(t, "mod-write-gate", storage.StatusStopped)
	modsDir := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(modsDir, 0o700))
	live := filepath.Join(modsDir, "mod.jar")
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o600))

	svc := NewModService(store, nil, nil, logger.New())
	modID := installedModID(server.ID, "mod.jar")

	fence, err := datawrite.AcquireExclusive(ctx, server.ID)
	require.NoError(t, err)
	t.Cleanup(fence.Release)

	_, err = svc.UpdateMod(ctx, connect.NewRequest(&v1.UpdateModRequest{
		ServerId: server.ID, ModId: modID, Enabled: boolPtr(false),
	}))
	require.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	require.FileExists(t, live, "a refused toggle must not move the live mod")

	_, err = svc.DeleteMod(ctx, connect.NewRequest(&v1.DeleteModRequest{
		ServerId: server.ID, ModId: modID,
	}))
	require.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	require.FileExists(t, live, "a refused delete must not remove the live mod")

	fence.Release()

	_, err = svc.UpdateMod(ctx, connect.NewRequest(&v1.UpdateModRequest{
		ServerId: server.ID, ModId: modID, Enabled: boolPtr(false),
	}))
	require.NoError(t, err)
	require.NoFileExists(t, live)
	require.FileExists(t, filepath.Join(modsDir+"_disabled", "mod.jar"))

	_, err = svc.DeleteMod(ctx, connect.NewRequest(&v1.DeleteModRequest{
		ServerId: server.ID, ModId: modID,
	}))
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(modsDir+"_disabled", "mod.jar"))
}

func TestStagedContentAdmissionRefusesAnExclusivePublication(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "staged-content-gate", storage.StatusStopped)
	live := filepath.Join(server.DataPath, "mods", "mod.jar")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o600))
	mods := NewModService(store, nil, um, logger.New())
	mods.SetModUpdateLookup(newPreviewFixture())
	mods.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("next")})

	fence, err := datawrite.AcquireExclusive(ctx, server.ID)
	require.NoError(t, err)
	t.Cleanup(fence.Release)
	_, err = stageContentBytes(ctx, store, server, nil, storage.StagedKindFile, storage.StagedActionReplace,
		"mods/mod.jar", "mod.jar", []byte("ordinary"))
	require.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	_, err = mods.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, batch)
	require.Empty(t, stagedFiles(t, server.DataPath))

	fence.Release()
	res, err := mods.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Equal(t, "staged", res.Msg.GetStagedUpdates()[0].GetOutcome())
}

func TestModWritesAreRefusedDuringNextRestartCompensation(t *testing.T) {
	ctx := context.Background()
	store, server, _ := newContentStageFixture(t, "mod-next-restart-gate", storage.StatusStopped)
	live := filepath.Join(server.DataPath, "mods", "mod.jar")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o600))

	id := "mod-next-restart"
	stagePath := ".carbon-panel/staged/" + id + "/mod.jar"
	require.NoError(t, os.MkdirAll(filepath.Join(server.DataPath, filepath.FromSlash(filepath.Dir(stagePath))), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(server.DataPath, filepath.FromSlash(stagePath)), []byte("candidate"), 0o600))
	_, err := store.AddStagedContent(ctx, &storage.StagedContentChange{
		ID: id, ServerID: server.ID,
		Kind: storage.StagedKindFile, Action: storage.StagedActionReplace,
		TargetPath: "mods/mod.jar", StagePath: stagePath, FileName: "mod.jar",
	})
	require.NoError(t, err)

	commit, err := staging.CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, commit)
	t.Cleanup(commit.Finish)
	body, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "candidate", string(body))

	svc := NewModService(store, nil, nil, logger.New())
	modID := installedModID(server.ID, "mod.jar")
	_, err = svc.UpdateMod(ctx, connect.NewRequest(&v1.UpdateModRequest{
		ServerId: server.ID, ModId: modID, Enabled: boolPtr(false),
	}))
	require.Equal(t, connect.CodeAborted, connect.CodeOf(err), "the next-restart commit owns the data path until compensation resolves")
	require.FileExists(t, live)

	require.NoError(t, commit.Undo())
	body, err = os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "installed", string(body), "compensation restores the pre-publication bytes")

	_, err = svc.UpdateMod(ctx, connect.NewRequest(&v1.UpdateModRequest{
		ServerId: server.ID, ModId: modID, Enabled: boolPtr(false),
	}))
	require.NoError(t, err, "compensation releases the data path after restoring the live tree")
	require.FileExists(t, filepath.Join(server.DataPath, "mods_disabled", "mod.jar"))
}
