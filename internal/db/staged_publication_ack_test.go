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

// TestMarkUpdatePublicationVerifiedRequiresOpenBatchAcknowledgement pins the
// acknowledgement contract: verifying a dispatch must close exactly one open,
// unacknowledged batch row. A config-only batch that was already closed cannot
// be acknowledged, so the verified phase must roll back with the transaction.
func TestMarkUpdatePublicationVerifiedRequiresOpenBatchAcknowledgement(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	server := &Server{ID: "ack-config-only", Name: "ack-config-only"}
	require.NoError(t, store.CreateServer(ctx, server))
	staged, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "only"},
		UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	require.NotEmpty(t, staged.BatchID)

	record, err := store.BeginUpdatePublication(ctx, staged.BatchID, server.ID, "baseline")
	require.NoError(t, err)
	require.NoError(t, store.RecordUpdatePublicationSnapshot(ctx, staged.BatchID, record.Attempt, "[]"))
	require.NoError(t, store.RecordUpdatePublicationDispatch(ctx, staged.BatchID, record.Attempt, "[]", "[]"))

	// The batch can no longer be acknowledged: it is no longer open. The
	// publication update must therefore roll back instead of reporting a
	// verified runtime over a batch that stayed staged.
	require.NoError(t, store.CloseUpdateBatch(ctx, staged.BatchID, UpdateBatchApplied))

	err = store.MarkUpdatePublicationVerified(ctx, staged.BatchID, record.Attempt, "replacement")
	require.ErrorIs(t, err, ErrPublicationTransition)

	got, err := store.GetUpdatePublication(ctx, staged.BatchID)
	require.NoError(t, err)
	require.Equal(t, PublicationDispatched, got.Phase)
	require.Empty(t, got.VerifiedContainerID)
	require.Nil(t, got.VerifiedAt)
}
