// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package staging

import (
	"context"
	"testing"

	"github.com/athNdev/carbon-panel/internal/datawrite"
	"github.com/stretchr/testify/require"
)

// TestPublicationRecoveryPendingFailsClosedOnReadError is the regression for
// the recovery lease failing open. A failed durable phase read cannot prove the
// phase is not APPLYING; releasing the exclusive lease then would let an
// ordinary save land under a snapshot a retry may still restore.
func TestPublicationRecoveryPendingFailsClosedOnReadError(t *testing.T) {
	ctx := context.Background()
	store, _ := newStageStore(t)

	applying, err := store.BeginUpdatePublication(ctx, "lease-applying", "server-applying", "baseline")
	require.NoError(t, err)
	require.NoError(t, store.RecordUpdatePublicationSnapshot(ctx, applying.BatchID, applying.Attempt, "[]"))
	require.NoError(t, store.RecordUpdatePublicationApplying(ctx, applying.BatchID, applying.Attempt))
	require.True(t, publicationRecoveryPending(ctx, store, applying.BatchID), "APPLYING still owes a restore")

	prepared, err := store.BeginUpdatePublication(ctx, "lease-prepared", "server-prepared", "baseline")
	require.NoError(t, err)
	require.NoError(t, store.RecordUpdatePublicationSnapshot(ctx, prepared.BatchID, prepared.Attempt, "[]"))
	require.False(t, publicationRecoveryPending(ctx, store, prepared.BatchID),
		"prepared proves no live write began, so the lease may be released")

	require.NoError(t, store.DB().Exec(`ALTER TABLE server_update_publications RENAME TO server_update_publications_offline`).Error)
	t.Cleanup(func() {
		_ = store.DB().Exec(`ALTER TABLE server_update_publications_offline RENAME TO server_update_publications`).Error
	})
	require.True(t, publicationRecoveryPending(ctx, store, applying.BatchID),
		"an unreadable phase record must retain the lease instead of failing open")
}

// TestRetainedRecoveryLeaseKeepsOrdinaryWritesOut proves the retained lease is
// the actual write exclusion the fail-closed decision protects.
func TestRetainedRecoveryLeaseKeepsOrdinaryWritesOut(t *testing.T) {
	ctx := context.Background()
	const serverID = "lease-retained"
	lease, err := datawrite.AcquireExclusive(ctx, serverID)
	require.NoError(t, err)
	retainRecoveryLease(serverID, lease)
	t.Cleanup(func() { clearRetainedRecoveryLease(serverID) })

	shared, err := datawrite.TryShared(serverID)
	require.Error(t, err, "a retained recovery lease must refuse ordinary writes")
	require.Nil(t, shared)

	clearRetainedRecoveryLease(serverID)
	shared, err = datawrite.TryShared(serverID)
	require.NoError(t, err, "resolving the recovery must release the exclusion")
	require.NotNil(t, shared)
	shared.Release()
}
