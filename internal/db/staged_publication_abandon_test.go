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

// TestAbandonUpdatePublicationRefusesDispatched pins the method contract: only
// an attempt that never recorded a runtime dispatch may be abandoned through
// this generic transition. A dispatched attempt is unverified and must be
// settled by identity, or released by the explicit typed-preflight method.
func TestAbandonUpdatePublicationRefusesDispatched(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	record, err := store.BeginUpdatePublication(ctx, "abandon-dispatched", "server-1", "baseline")
	require.NoError(t, err)
	require.NoError(t, store.RecordUpdatePublicationSnapshot(ctx, record.BatchID, record.Attempt, "[]"))
	require.NoError(t, store.RecordUpdatePublicationDispatch(ctx, record.BatchID, record.Attempt, "[]", "[]"))

	err = store.AbandonUpdatePublication(ctx, record.BatchID, record.Attempt)
	require.ErrorIs(t, err, ErrPublicationTransition)

	got, err := store.GetUpdatePublication(ctx, record.BatchID)
	require.NoError(t, err)
	require.Equal(t, PublicationDispatched, got.Phase)
	require.Empty(t, got.ExpectedContainerID)

	// The explicit typed-preflight transition is the only way to release a
	// dispatched attempt without identity verification.
	require.NoError(t, store.AbandonPreflightDispatch(ctx, record.BatchID, record.Attempt))
	got, err = store.GetUpdatePublication(ctx, record.BatchID)
	require.NoError(t, err)
	require.Equal(t, PublicationAbandoned, got.Phase)

	// A preparation attempt that never dispatched still abandons, and the
	// second call is idempotent.
	prepared, err := store.BeginUpdatePublication(ctx, "abandon-prepared", "server-1", "")
	require.NoError(t, err)
	require.NoError(t, store.RecordUpdatePublicationSnapshot(ctx, prepared.BatchID, prepared.Attempt, "[]"))
	require.NoError(t, store.AbandonUpdatePublication(ctx, prepared.BatchID, prepared.Attempt))
	require.NoError(t, store.AbandonUpdatePublication(ctx, prepared.BatchID, prepared.Attempt))
	got, err = store.GetUpdatePublication(ctx, prepared.BatchID)
	require.NoError(t, err)
	require.Equal(t, PublicationAbandoned, got.Phase)
}
