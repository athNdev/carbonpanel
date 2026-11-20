// SPDX-License-Identifier: MPL-2.0
package staging

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
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
	_, err = commitStreamingFiles(ctx, acc, rows)
	require.Error(t, err)
	require.Contains(t, err.Error(), "changed since review")

	current, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "locally-edited", string(current), "the local edit must survive")
	staged, err := acc.ReadFile(row.StagePath)
	require.NoError(t, err)
	require.Equal(t, "candidate", string(staged), "the candidate bytes must remain for recovery")
}

func TestPublishAcceptsUnchangedLiveFile(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "drift-ok", Name: "drift-ok", DataPath: data, Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
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
	_, err = commitStreamingFiles(ctx, acc, rows)
	require.NoError(t, err)
	current, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "candidate", string(current))
}
