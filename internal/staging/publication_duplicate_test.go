// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package staging

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/stretchr/testify/require"
)

// TestRecordedRecoveryRefusesLegacyDuplicateTarget is the old-data boundary:
// a snapshot recorded before expanded-target admission may contain two entries
// for one live path. Recovery refuses it before restoring or restaging anything
// instead of leaving a snapshot one live post-image can never satisfy.
func TestRecordedRecoveryRefusesLegacyDuplicateTarget(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	acc, err := nodestorage.NewLocal(data)
	require.NoError(t, err)
	t.Cleanup(func() { _ = acc.Close() })

	snapshot, err := json.Marshal([]undoEntry{
		{RowID: "row-a", Target: "config/shared.txt", Action: storage.StagedActionReplace, Source: "stage-a"},
		{RowID: "row-b", Target: "CONFIG\\SHARED.TXT", Action: storage.StagedActionReplace, Source: "stage-b"},
	})
	require.NoError(t, err)
	record := &storage.ServerUpdatePublication{
		BatchID:  "legacy-recorded-duplicate",
		Snapshot: string(snapshot),
	}
	owned := []storage.StagedContentChange{
		{ID: "row-a", BatchID: record.BatchID, Kind: storage.StagedKindFile, Action: storage.StagedActionReplace, TargetPath: "config/shared.txt", StagePath: "stage-a", Status: storage.StagedStatusApplied},
		{ID: "row-b", BatchID: record.BatchID, Kind: storage.StagedKindFile, Action: storage.StagedActionReplace, TargetPath: "config/shared.txt", StagePath: "stage-b", Status: storage.StagedStatusApplied},
	}
	err = restoreRecordedRecovery(ctx, acc, store, record, filepath.Join(undoRoot, record.BatchID), owned)
	require.ErrorContains(t, err, "more than one recovery entry for target")
	require.NoFileExists(t, filepath.Join(data, "config", "shared.txt"))
}
