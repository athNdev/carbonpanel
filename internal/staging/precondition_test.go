// SPDX-License-Identifier: MPL-2.0
package staging

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func digest512(data string) string {
	sum := sha512.Sum512([]byte(data))
	return hex.EncodeToString(sum[:])
}

func TestPublishRefusesDelayedLiveDriftAndPreservesStagedBytes(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "drift-publish", Name: "drift-publish", DataPath: data, Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	setReviewedRuntimeTarget(t, store, server.ID)

	live := filepath.Join(data, "mods", "mod.jar")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o600))

	expected := digest512("installed")
	row, err := store.AddStagedContent(ctx, &storage.StagedContentChange{
		ID:                 uuid.NewString(),
		ServerID:           server.ID,
		Kind:               storage.StagedKindMod,
		Action:             storage.StagedActionReplace,
		TargetPath:         "mods/mod.jar",
		StagePath:          ".carbon-panel/staged/candidate/mod.jar",
		FileName:           "mod.jar",
		ExpectedLiveSha512: expected,
		ApprovedSha512:     digest512("candidate"),
		ApprovedProvider:   "modrinth",
		ApprovedProjectID:  "project",
		ApprovedVersionID:  "release",
		ApprovedFilename:   "mod.jar",
	})
	require.NoError(t, err)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, batch.ID, row.BatchID)

	acc, err := nodestorage.NewLocal(data)
	require.NoError(t, err)
	defer func() { _ = acc.Close() }()
	require.NoError(t, acc.MkdirAll(".carbon-panel/staged/candidate"))
	require.NoError(t, acc.WriteFile(row.StagePath, []byte("candidate")))

	// A local edit after review must block the delayed publish.
	require.NoError(t, os.WriteFile(live, []byte("locally-edited"), 0o600))
	rows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	_, err = commitStreamingFiles(ctx, acc, rows, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "changed after staging")

	current, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "locally-edited", string(current), "the local edit must survive")
	staged, err := acc.ReadFile(row.StagePath)
	require.NoError(t, err)
	require.Equal(t, "candidate", string(staged), "the candidate bytes must remain for recovery")
}

type editOnBackupAccessor struct {
	nodestorage.Accessor
	edit func() error
}

func (a editOnBackupAccessor) BeginWrite(ctx context.Context, path string, limit int64) (storageworker.AtomicWriter, error) {
	if filepath.Ext(path) == ".data" {
		if err := a.edit(); err != nil {
			return nil, err
		}
	}
	return a.Accessor.BeginWrite(ctx, path, limit)
}

func TestReviewedHashCoversTheActualUndoSnapshot(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	acc, err := nodestorage.NewLocal(data)
	require.NoError(t, err)
	defer func() { _ = acc.Close() }()
	const live, staged = "mods/mod.jar", ".carbon-panel/staged/a/update.jar"
	require.NoError(t, acc.WriteFile(live, []byte("installed")))
	require.NoError(t, acc.WriteFile(staged, []byte("candidate")))
	rows := []storage.StagedContentChange{{
		BatchID: uuid.NewString(), Kind: storage.StagedKindMod, Action: storage.StagedActionReplace,
		TargetPath: live, StagePath: staged, ExpectedLiveSha512: digest512("installed"),
		ApprovedSha512:   digest512("candidate"),
		ApprovedProvider: "modrinth", ApprovedProjectID: "project",
		ApprovedVersionID: "release", ApprovedFilename: "mod.jar",
	}}
	wrapped := editOnBackupAccessor{Accessor: acc, edit: func() error {
		// Modify the already-open inode while the undo write is beginning.
		return os.WriteFile(filepath.Join(data, filepath.FromSlash(live)), []byte("modified!"), 0o600)
	}}
	_, err = commitStreamingFiles(ctx, wrapped, rows, nil)
	require.ErrorContains(t, err, "changed after staging")
	got, err := acc.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "modified!", string(got))
	got, err = acc.ReadFile(staged)
	require.NoError(t, err)
	require.Equal(t, "candidate", string(got))
}

func TestDueModBatchPublishesWithoutApplyingFutureCronItem(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "due-mod", Name: "due-mod", DataPath: data, Status: storage.StatusRunning}
	require.NoError(t, store.CreateServer(ctx, server))
	cfg := store.CreateDefaultServerConfig(server.ID)
	require.NoError(t, store.SaveServerConfig(ctx, cfg))
	setReviewedRuntimeTarget(t, store, server.ID)

	// Pre-existing mixed data on the same server: a future cron config was
	// staged first, then the batch was retargeted to a due mod update window.
	cron, err := store.StageConfigChange(ctx, server.ID,
		map[string]any{"motd": "nightly"}, storage.StagedApplyScheduled, "0 3 * * *")
	require.NoError(t, err)
	when := time.Now().Add(-time.Minute).UTC()
	_, err = store.SetOpenBatchWindow(ctx, server.ID, storage.UpdateWindowAtTime, &when, 0)
	require.NoError(t, err)

	live := filepath.Join(data, "mods", "mod.jar")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o600))
	err = store.DB().WithContext(ctx).Create(&storage.StagedContentChange{
		ID: uuid.NewString(), ServerID: server.ID, Kind: storage.StagedKindMod,
		BatchID: cron.BatchID, Status: storage.StagedStatusStaged,
		Action: storage.StagedActionReplace, TargetPath: "mods/mod.jar",
		StagePath:              ".carbon-panel/staged/candidate/mod.jar",
		FileName:               "mod.jar",
		ExpectedLiveSha512:     digest512("installed"),
		ReviewedRuntimeLoader:  "fabric",
		ReviewedRuntimeVersion: "1.21.1",
		ApprovedSha512:         digest512("candidate"),
		ApprovedProvider:       "modrinth",
		ApprovedProjectID:      "project",
		ApprovedVersionID:      "release",
		ApprovedFilename:       "mod.jar",
	}).Error
	require.NoError(t, err)
	cron, err = store.GetStagedConfigChange(ctx, cron.ID)
	require.NoError(t, err)
	acc, err := nodestorage.NewLocal(data)
	require.NoError(t, err)
	require.NoError(t, acc.MkdirAll(".carbon-panel/staged/candidate"))
	require.NoError(t, acc.WriteFile(".carbon-panel/staged/candidate/mod.jar", []byte("candidate")))
	require.NoError(t, acc.Close())

	var publishes int
	require.NoError(t, Sweep(ctx, store, nil, time.Now(), nil,
		func(context.Context, *storage.Server, Plan) error { publishes++; return nil }))

	published, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "candidate", string(published), "the due mod update must publish")
	require.GreaterOrEqual(t, publishes, 1)

	staged, err := store.GetStagedConfigChange(ctx, cron.ID)
	require.NoError(t, err)
	require.Equal(t, cron, staged)
	require.Equal(t, storage.StagedStatusStaged, staged.Status, "cron item must not be applied early")
	require.Equal(t, storage.StagedApplyScheduled, staged.ApplyMode)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch, "the cron row keeps its batch and schedule")
	require.Equal(t, string(storage.UpdateWindowNextRestart), batch.Window)
	current, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.NotEqual(t, "nightly", valueOf(current.MOTD))

	// The row remains applicable through the existing staged-config path.
	// The fixture calls the store directly; this PR wires no new cron poller.
	_, err = store.ApplyStagedConfigChange(ctx, cron.ID)
	require.NoError(t, err)
	current, err = store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, "nightly", valueOf(current.MOTD))
}

func valueOf(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestStaleCronBaseCannotBlockDueModPublication(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "stale-cron", Name: "stale-cron", DataPath: data, Status: storage.StatusRunning}
	require.NoError(t, store.CreateServer(ctx, server))
	cfg := store.CreateDefaultServerConfig(server.ID)
	require.NoError(t, store.SaveServerConfig(ctx, cfg))
	setReviewedRuntimeTarget(t, store, server.ID)
	cron, err := store.StageConfigChange(ctx, server.ID,
		map[string]any{"motd": "nightly"}, storage.StagedApplyScheduled, "0 3 * * *")
	require.NoError(t, err)

	rolledBackCron, err := store.StageConfigChange(ctx, server.ID,
		map[string]any{"difficulty": "hard"}, storage.StagedApplyScheduled, "30 3 * * *")
	require.NoError(t, err)
	_, err = store.ApplyStagedConfigChange(ctx, rolledBackCron.ID)
	require.NoError(t, err)
	rolledBackCron, err = store.RollbackStagedConfigChange(ctx, rolledBackCron.ID)
	require.NoError(t, err)
	rolledBackCron, err = store.GetStagedConfigChange(ctx, rolledBackCron.ID)
	require.NoError(t, err)

	// The live base moved after the cron row was staged: its base is stale.
	cfg, err = store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	changed := "changed-since-cron-stage"
	cfg.MOTD = &changed
	require.NoError(t, store.SaveServerConfig(ctx, cfg))

	when := time.Now().Add(-time.Minute).UTC()
	_, err = store.SetOpenBatchWindow(ctx, server.ID, storage.UpdateWindowAtTime, &when, 0)
	require.NoError(t, err)
	live := filepath.Join(data, "mods", "mod.jar")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o600))
	err = store.DB().WithContext(ctx).Create(&storage.StagedContentChange{
		ID: uuid.NewString(), ServerID: server.ID, Kind: storage.StagedKindMod,
		BatchID: cron.BatchID, Status: storage.StagedStatusStaged,
		Action: storage.StagedActionReplace, TargetPath: "mods/mod.jar",
		StagePath:              ".carbon-panel/staged/candidate/mod.jar",
		FileName:               "mod.jar",
		ExpectedLiveSha512:     digest512("installed"),
		ReviewedRuntimeLoader:  "fabric",
		ReviewedRuntimeVersion: "1.21.1",
		ApprovedSha512:         digest512("candidate"),
		ApprovedProvider:       "modrinth",
		ApprovedProjectID:      "project",
		ApprovedVersionID:      "release",
		ApprovedFilename:       "mod.jar",
	}).Error
	require.NoError(t, err)
	cron, err = store.GetStagedConfigChange(ctx, cron.ID)
	require.NoError(t, err)
	acc, err := nodestorage.NewLocal(data)
	require.NoError(t, err)
	require.NoError(t, acc.MkdirAll(".carbon-panel/staged/candidate"))
	require.NoError(t, acc.WriteFile(".carbon-panel/staged/candidate/mod.jar", []byte("candidate")))
	require.NoError(t, acc.Close())

	require.NoError(t, Sweep(ctx, store, nil, time.Now(), nil,
		func(context.Context, *storage.Server, Plan) error { return nil }))
	published, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "candidate", string(published), "a stale cron base must not block the due mod batch")
	staged, err := store.GetStagedConfigChange(ctx, cron.ID)
	require.NoError(t, err)
	require.Equal(t, cron, staged)
	require.Equal(t, storage.StagedStatusStaged, staged.Status)
	stillRolledBack, err := store.GetStagedConfigChange(ctx, rolledBackCron.ID)
	require.NoError(t, err)
	require.Equal(t, rolledBackCron, stillRolledBack)

	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, string(storage.UpdateWindowNextRestart), batch.Window)
}

func TestPublishAcceptsUnchangedLiveFile(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "drift-ok", Name: "drift-ok", DataPath: data, Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	setReviewedRuntimeTarget(t, store, server.ID)
	live := filepath.Join(data, "mods", "mod.jar")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o600))

	_, err := store.AddStagedContent(ctx, &storage.StagedContentChange{
		ID:                 uuid.NewString(),
		ServerID:           server.ID,
		Kind:               storage.StagedKindMod,
		Action:             storage.StagedActionReplace,
		TargetPath:         "mods/mod.jar",
		StagePath:          ".carbon-panel/staged/candidate/mod.jar",
		FileName:           "mod.jar",
		ExpectedLiveSha512: digest512("installed"),
		ApprovedSha512:     digest512("candidate"),
		ApprovedProvider:   "modrinth",
		ApprovedProjectID:  "project",
		ApprovedVersionID:  "release",
		ApprovedFilename:   "mod.jar",
	})
	require.NoError(t, err)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	acc, err := nodestorage.NewLocal(data)
	require.NoError(t, err)
	defer func() { _ = acc.Close() }()
	require.NoError(t, acc.MkdirAll(".carbon-panel/staged/candidate"))
	require.NoError(t, acc.WriteFile(".carbon-panel/staged/candidate/mod.jar", []byte("candidate")))
	rows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	_, err = commitStreamingFiles(ctx, acc, rows, nil)
	require.NoError(t, err)
	current, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "candidate", string(current))
}
