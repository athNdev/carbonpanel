// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package staging

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/stretchr/testify/require"
)

func preflightPublication(t *testing.T, serverID string) (*storage.Store, *storage.Server, string, *RestartPublication) {
	t.Helper()
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{
		ID: serverID, Name: serverID, DataPath: data,
		ContainerID: "baseline", Status: storage.StatusRunning,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "new")
	commit, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	applied, err := commit.ApplyConfigs(ctx, store)
	require.NoError(t, err)
	publication := &RestartPublication{Commit: commit, Configs: applied, RuntimeAttempted: true, RuntimePreflight: true}
	require.NoError(t, publication.BeginDispatch(ctx))
	return store, server, batch, publication
}

func requireReopenedByPreflight(t *testing.T, store *storage.Store, server *storage.Server, batch string, publication *RestartPublication) {
	t.Helper()
	ctx := context.Background()
	// A typed preflight proves no container mutation began, so a dispatch whose
	// identity write failed is abandoned and compensated instead of sealed.
	require.NoError(t, publication.Resolve(ctx, store, errors.New("preflight failed")))
	record, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationAbandoned, record.Phase)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.Nil(t, open.SealedAt, "a compensated preflight must reopen the batch")
	require.Nil(t, open.PublishedAt)
	require.NoFileExists(t, filepath.Join(server.DataPath, "file.txt"))

	retry, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, retry, "the retry must be able to reopen the batch")
	t.Cleanup(retry.Finish)
}

// TestPreflightIdentityWriteFailureIsRetryable simulates the old caller order:
// a typed preflight recorded a replacement identity, that write failed, and the
// unresolved marker write succeeded. Recovery must treat the preflight proof as
// authoritative and abandon the empty-identity dispatch.
func TestPreflightIdentityWriteFailureIsRetryable(t *testing.T) {
	ctx := context.Background()
	store, server, batch, publication := preflightPublication(t, "preflight-identity-write")
	require.NoError(t, store.DB().Exec(
		`CREATE TRIGGER fail_replacement_identity BEFORE UPDATE OF expected_container_id ON server_update_publications
		 BEGIN SELECT RAISE(ABORT, 'replacement identity unavailable'); END`).Error)
	t.Cleanup(func() { _ = store.DB().Exec(`DROP TRIGGER fail_replacement_identity`).Error })

	recordErr := publication.RecordReplacement(ctx, "created-replacement")
	require.ErrorContains(t, recordErr, "failed to record replacement identity")
	require.True(t, publication.RuntimeIdentityUnresolved)
	record, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationIdentityUnresolved, record.Phase)
	require.Empty(t, record.ExpectedContainerID)
	require.NoError(t, store.DB().Exec(`DROP TRIGGER fail_replacement_identity`).Error)

	requireReopenedByPreflight(t, store, server, batch, publication)
}

// TestPreflightMarkerWriteFailureIsRetryable covers the worse old state: both
// the replacement identity and the identity-unresolved marker writes failed, so
// the record stays dispatched with an empty expected identity. The preflight
// proof still releases it without a replacement to verify.
func TestPreflightMarkerWriteFailureIsRetryable(t *testing.T) {
	ctx := context.Background()
	store, server, batch, publication := preflightPublication(t, "preflight-marker-write")
	require.NoError(t, store.DB().Exec(
		`CREATE TRIGGER fail_identity_writes BEFORE UPDATE OF expected_container_id, phase ON server_update_publications
		 BEGIN SELECT RAISE(ABORT, 'identity storage unavailable'); END`).Error)
	t.Cleanup(func() { _ = store.DB().Exec(`DROP TRIGGER fail_identity_writes`).Error })

	recordErr := publication.RecordReplacement(ctx, "created-replacement")
	require.ErrorContains(t, recordErr, "failed to record replacement identity")
	require.True(t, publication.RuntimeIdentityUnresolved)
	record, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationDispatched, record.Phase)
	require.Empty(t, record.ExpectedContainerID)
	require.NoError(t, store.DB().Exec(`DROP TRIGGER fail_identity_writes`).Error)

	requireReopenedByPreflight(t, store, server, batch, publication)
}
