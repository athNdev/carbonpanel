// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"crypto/sha512"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/require"
)

type modUpdateFetchFixture struct {
	data    []byte
	outcome string
	err     error
	calls   int
}

func (f *modUpdateFetchFixture) Fetch(context.Context, *storage.ServerConfig, string, string, string, string, string) ([]byte, string, error) {
	f.calls++
	return f.data, f.outcome, f.err
}

func installedModDigest(data string) string {
	return fmt.Sprintf("%x", sha512.Sum512([]byte(data)))
}

func modUpdateRequest(serverID, installedHash string) *v1.UpdateModRequest {
	return &v1.UpdateModRequest{
		ServerId: serverID,
		UpdateSelections: []*v1.ModUpdateSelection{{
			ModId:              "reviewed-mod",
			FileName:           "mod.jar",
			InstalledSha512:    installedHash,
			CandidateVersionId: "update",
			CandidateSha512:    installedModDigest("next"),
			CandidateFilename:  "mod-2.jar",
		}},
		UpdateWindow: v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART,
	}
}

func TestStageReviewedModUpdateKeepsLiveBytesAndStagesExactCandidate(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-update-apply", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	live := filepath.Join(mods, "mod.jar")
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o600))

	fetcher := &modUpdateFetchFixture{data: []byte("next")}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModCandidateFetcher(fetcher)

	res, err := svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetStagedUpdates(), 1)
	require.Equal(t, "staged", res.Msg.GetStagedUpdates()[0].GetOutcome())
	require.NotEmpty(t, res.Msg.GetStagedUpdates()[0].GetStagedId())
	require.NotEmpty(t, res.Msg.GetUpdateBatchId())
	require.Equal(t, 1, fetcher.calls)

	after, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "installed", string(after), "manual apply must not write the live mods directory")

	staged, err := os.ReadFile(filepath.Join(server.DataPath, ".carbon-panel", "staged",
		res.Msg.GetStagedUpdates()[0].GetStagedId(), "mod.jar"))
	require.NoError(t, err)
	require.Equal(t, "next", string(staged), "staged bytes must be the checked candidate")

	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, string(storage.UpdateWindowNextRestart), batch.Window)

	rows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, storage.StagedKindMod, rows[0].Kind)
	require.Equal(t, storage.StagedActionReplace, rows[0].Action)
	require.Equal(t, filepath.ToSlash(filepath.Join("mods", "mod.jar")), rows[0].TargetPath)
}

func TestStageReviewedModUpdateRefusesInstalledDrift(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-update-drift", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("different"), 0o600))

	fetcher := &modUpdateFetchFixture{data: []byte("next")}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModCandidateFetcher(fetcher)

	res, err := svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetStagedUpdates(), 1)
	require.Equal(t, "refused_drift", res.Msg.GetStagedUpdates()[0].GetOutcome())
	require.Empty(t, res.Msg.GetUpdateBatchId())
	require.Zero(t, fetcher.calls, "drifted bytes must be refused before any provider download")
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, batch)
}

func TestStageReviewedModUpdateRefusesChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-update-checksum", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	fetcher := &modUpdateFetchFixture{err: errors.New("candidate bytes do not match the reviewed SHA-512"), outcome: "refused_checksum"}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModCandidateFetcher(fetcher)

	res, err := svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Equal(t, "refused_checksum", res.Msg.GetStagedUpdates()[0].GetOutcome())
	require.Empty(t, res.Msg.GetUpdateBatchId())
	require.Empty(t, stagedFiles(t, server.DataPath), "a failed candidate must leave no staged bytes")
}

func TestScheduledModUpdatePolicyStagesOnceAndHonorsExclusions(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-policy", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	lookup := newPreviewFixture()
	fetcher := &modUpdateFetchFixture{data: []byte("next")}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(lookup)
	svc.SetModCandidateFetcher(fetcher)

	staged, refused, err := svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{})
	require.NoError(t, err)
	require.Zero(t, staged, "a disabled policy must not stage")
	require.Zero(t, refused)

	staged, refused, err = svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{
		Enabled:          true,
		ExcludedProjects: []string{"project"},
	})
	require.NoError(t, err)
	require.Zero(t, staged, "an excluded project must not be staged")
	require.Equal(t, 1, refused)
	require.Zero(t, fetcher.calls)

	staged, refused, err = svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{Enabled: true})
	require.NoError(t, err)
	require.Equal(t, 1, staged)
	require.Zero(t, refused)
	require.Equal(t, 1, fetcher.calls)

	staged, refused, err = svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{Enabled: true})
	require.NoError(t, err)
	require.Zero(t, staged, "a second run must deduplicate the already staged file")
	require.Equal(t, 1, refused)
	require.Equal(t, 1, fetcher.calls, "deduplication must happen before any provider download")

	live, err := os.ReadFile(filepath.Join(mods, "mod.jar"))
	require.NoError(t, err)
	require.Equal(t, "installed", string(live), "an automatic policy must not write the live tree")
}

func TestStageReviewedModUpdateRequiresKnownServerAndValidWindow(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-update-window", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	svc := NewModService(store, nil, um, logger.New())
	svc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("next")})

	_, err := svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest("missing-server", installedModDigest("installed"))))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	atTime := modUpdateRequest(server.ID, installedModDigest("installed"))
	atTime.UpdateWindow = v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_AT_TIME
	_, err = svc.UpdateMod(ctx, connect.NewRequest(atTime))
	require.Error(t, err, "at_time requires an apply time")

	idle := modUpdateRequest(server.ID, installedModDigest("installed"))
	idle.UpdateWindow = v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_IDLE
	_, err = svc.UpdateMod(ctx, connect.NewRequest(idle))
	require.Error(t, err, "idle requires positive idle minutes")
}
