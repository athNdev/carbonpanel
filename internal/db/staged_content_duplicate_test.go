// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func stagedDuplicateRow(id, serverID, target string) *StagedContentChange {
	return &StagedContentChange{
		ID:         id,
		ServerID:   serverID,
		Kind:       StagedKindFile,
		Action:     StagedActionReplace,
		TargetPath: target,
		StagePath:  ".carbon-panel/staged/" + id + "/file.txt",
		FileName:   "file.txt",
	}
}

// TestDuplicateStagedTargetRefusedWithinBatch is the regression for the
// duplicate-target dead end: two staged rows for one normalized target make a
// single live post-image unverifiable, so an interrupted APPLYING snapshot can
// never be restored. Admission must refuse the second row instead.
func TestDuplicateStagedTargetRefusedWithinBatch(t *testing.T) {
	ctx := context.Background()
	store := newStagedTestStore(t, "duplicate-target")

	_, err := store.AddStagedContent(ctx, stagedDuplicateRow("dup-first", "duplicate-target", "config/server.properties"))
	require.NoError(t, err)

	// Case and separator aliases name the same live file.
	_, err = store.AddStagedContent(ctx, stagedDuplicateRow("dup-second", "duplicate-target", "Config\\SERVER.properties"))
	require.ErrorIs(t, err, ErrDuplicateStagedTarget)
	require.Contains(t, err.Error(), "already staged")

	rows, err := store.ListStagedContent(ctx, "duplicate-target")
	require.NoError(t, err)
	require.Len(t, rows, 1, "a refused duplicate must not be committed")
	require.Equal(t, "dup-first", rows[0].ID)

	// A different target still admits normally.
	_, err = store.AddStagedContent(ctx, stagedDuplicateRow("dup-other", "duplicate-target", "config/other.properties"))
	require.NoError(t, err)

	// The reviewed-batch insert path shares the same refusal.
	batch, err := store.OpenUpdateBatch(ctx, "duplicate-target")
	require.NoError(t, err)
	require.NotNil(t, batch)
	err = store.AddStagedContentToBatch(ctx, stagedDuplicateRow("dup-batch", "duplicate-target", "CONFIG/server.properties"),
		batch.ID, UpdateWindowNextRestart, nil, 0)
	require.ErrorIs(t, err, ErrDuplicateStagedTarget)

	rows, err = store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2, "only the two distinct targets remain staged")
}
