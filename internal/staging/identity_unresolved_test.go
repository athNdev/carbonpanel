// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package staging

import (
	"context"
	"errors"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/stretchr/testify/require"
)

// TestIdentityWriteFailureMarksUnresolvedAndRetryAdoptsReplacement is the
// regression for a created replacement whose identity write fails after
// dispatch. Previously the phase stayed dispatched with an empty expected
// identity, so every retry failed the identity check with no path. The write is
// retried; when it still fails the record is marked identity-write-failure and a
// retry may adopt the canonical container that dispatch created.
func TestIdentityWriteFailureMarksUnresolvedAndRetryAdoptsReplacement(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{
		ID: "identity-write-failure", Name: "identity-write-failure",
		DataPath: data, ContainerID: "baseline", Status: storage.StatusRunning,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "new")

	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	publication := &RestartPublication{Commit: commit, Configs: applied, RuntimeAttempted: true}
	require.NoError(t, publication.BeginDispatch(ctx))

	require.NoError(t, store.DB().Exec(
		`CREATE TRIGGER fail_replacement_identity BEFORE UPDATE OF expected_container_id ON server_update_publications
		 BEGIN SELECT RAISE(ABORT, 'replacement identity unavailable'); END`).Error)
	recordErr := publication.RecordReplacement(ctx, "created-replacement")
	require.ErrorContains(t, recordErr, "failed to record replacement identity")
	require.True(t, publication.RuntimeIdentityUnresolved, "a lost identity must not be compensated as a failed runtime")

	record, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationIdentityUnresolved, record.Phase)
	require.Empty(t, record.ExpectedContainerID)
	require.Equal(t, "baseline", record.BaselineContainerID)

	// The failure is escalated and sealed, not silently acknowledged.
	require.ErrorContains(t, publication.Resolve(ctx, store, errors.New("identity write failed")), "adopt and verify")
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.NotNil(t, open.SealedAt)
	require.Nil(t, open.PublishedAt)

	require.NoError(t, store.DB().Exec(`DROP TRIGGER fail_replacement_identity`).Error)
	retry, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, retry)
	t.Cleanup(retry.Finish)
	require.True(t, retry.NeedsIdentityVerification())

	// The baseline container is not a replacement, so it stays sealed.
	verified, err := retry.VerifyExpectedReplacement(ctx, "baseline")
	require.NoError(t, err)
	require.False(t, verified)
	record, err = store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationIdentityUnresolved, record.Phase)

	// The canonical container created by the dispatch is adopted and verified.
	verified, err = retry.VerifyExpectedReplacement(ctx, "created-replacement")
	require.NoError(t, err)
	require.True(t, verified)
	record, err = store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationVerified, record.Phase)
	require.Equal(t, "created-replacement", record.ExpectedContainerID)
	require.Equal(t, "created-replacement", record.VerifiedContainerID)
}
