// SPDX-License-Identifier: MPL-2.0

package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// AddStagedContentToBatch shares AddStagedContent's admission rules and always
// stores the row as staged, so a reviewed-batch caller cannot insert an
// orphaned or unvalidated row.
func TestAddStagedContentToBatchEnforcesAdmission(t *testing.T) {
	ctx := context.Background()
	const serverID = "srv-batch-admission"
	store := newStagedTestStore(t, serverID)
	batch, err := store.PrepareReviewedContentBatch(ctx, serverID, UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)

	valid := &StagedContentChange{
		ID: uuid.NewString(), ServerID: serverID, Kind: StagedKindMod, Action: StagedActionReplace,
		TargetPath: "mods/a.jar", StagePath: ".carbon-panel/staged/a/a.jar", FileName: "a.jar",
		Status: StagedStatusDiscarded,
	}
	require.NoError(t, store.AddStagedContentToBatch(ctx, valid, batch.ID, UpdateWindowNextRestart, nil, 0))
	stored, err := store.GetStagedContent(ctx, valid.ID)
	require.NoError(t, err)
	require.Equal(t, StagedStatusStaged, stored.Status, "a caller-supplied terminal status must not be persisted")

	for name, row := range map[string]*StagedContentChange{
		"nil row": nil,
		"missing server": {
			ID: uuid.NewString(), Kind: StagedKindMod, Action: StagedActionReplace,
			TargetPath: "mods/b.jar", StagePath: ".carbon-panel/staged/b/b.jar",
		},
		"missing target": {
			ID: uuid.NewString(), ServerID: serverID, Kind: StagedKindMod, Action: StagedActionReplace,
			StagePath: ".carbon-panel/staged/b/b.jar",
		},
		"missing stage for non-delete": {
			ID: uuid.NewString(), ServerID: serverID, Kind: StagedKindMod, Action: StagedActionReplace,
			TargetPath: "mods/b.jar",
		},
		"unknown kind": {
			ID: uuid.NewString(), ServerID: serverID, Kind: "script", Action: StagedActionReplace,
			TargetPath: "scripts/b.py", StagePath: ".carbon-panel/staged/b/b.py",
		},
		"unknown action": {
			ID: uuid.NewString(), ServerID: serverID, Kind: StagedKindMod, Action: "overwrite",
			TargetPath: "mods/b.jar", StagePath: ".carbon-panel/staged/b/b.jar",
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, store.AddStagedContentToBatch(ctx, row, batch.ID, UpdateWindowNextRestart, nil, 0))
		})
	}

	rows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1, "refused rows must not be persisted")
	require.Equal(t, valid.ID, rows[0].ID)
}
