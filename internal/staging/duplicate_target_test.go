// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package staging

import (
	"context"
	"path/filepath"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/stretchr/testify/require"
)

// TestLegacyDuplicateTargetRefusedBeforeApplying is the recovery boundary for
// rows older data may already have admitted twice. Publication refuses the
// duplicate before any live write, so it cannot record an APPLYING snapshot
// whose two entries one live post-image can never satisfy.
func TestLegacyDuplicateTargetRefusedBeforeApplying(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "legacy-duplicate", Name: "legacy-duplicate", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "first")

	require.NoError(t, store.DB().Create(&storage.StagedContentChange{
		ID:         "legacy-duplicate-row",
		ServerID:   server.ID,
		BatchID:    batch,
		Kind:       storage.StagedKindFile,
		Action:     storage.StagedActionInstall,
		TargetPath: "FILE.TXT",
		StagePath:  ".carbon-panel/staged/legacy-duplicate-row/file.txt",
		FileName:   "file.txt",
		Status:     storage.StagedStatusStaged,
	}).Error)

	_, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.ErrorContains(t, err, "more than one row for target")
	require.NoFileExists(t, filepath.Join(data, "file.txt"), "the refusal must precede every live write")

	record, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.NotEqual(t, storage.PublicationApplying, record.Phase,
		"an unverifiable duplicate must not record APPLYING")
}
