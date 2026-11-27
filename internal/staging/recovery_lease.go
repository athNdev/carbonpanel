// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package staging

import (
	"context"
	"sync"
	"time"

	"github.com/athNdev/carbon-panel/internal/datawrite"
	storage "github.com/athNdev/carbon-panel/internal/db"
)

// recoveryLeaseTimeout bounds the durable phase read that decides whether a
// failed attempt still owes an APPLYING recovery.
const recoveryLeaseTimeout = 5 * time.Second

// retainedRecoveryLeases keeps the exclusive data-write lease of a server whose
// publication attempt ended with an APPLYING recovery snapshot. Ordinary panel
// writes take the shared side of the same gate, so keeping the lease keeps a
// later save from landing before the retry restores the recorded snapshot. The
// retry re-adopts the retained lease instead of acquiring a second one; the
// commit handle then releases it through Finish/Undo as usual.
var retainedRecoveryLeases = struct {
	sync.Mutex
	byServer map[string]*datawrite.Lease
}{byServer: make(map[string]*datawrite.Lease)}

// acquireRecoveryLease re-adopts a lease retained for a pending recovery, or
// takes a fresh exclusive lease. A retained lease still owns the gate, so a
// second AcquireExclusive would be refused as busy.
func acquireRecoveryLease(ctx context.Context, serverID string) (*datawrite.Lease, error) {
	retainedRecoveryLeases.Lock()
	lease := retainedRecoveryLeases.byServer[serverID]
	delete(retainedRecoveryLeases.byServer, serverID)
	retainedRecoveryLeases.Unlock()
	if lease != nil {
		return lease, nil
	}
	return datawrite.AcquireExclusive(ctx, serverID)
}

// retainRecoveryLease keeps an exclusive lease after a failed attempt so
// ordinary writes stay refused until the recorded recovery is resolved.
func retainRecoveryLease(serverID string, lease *datawrite.Lease) {
	if lease == nil {
		return
	}
	retainedRecoveryLeases.Lock()
	previous := retainedRecoveryLeases.byServer[serverID]
	retainedRecoveryLeases.byServer[serverID] = lease
	retainedRecoveryLeases.Unlock()
	if previous != nil && previous != lease {
		previous.Release()
	}
}

// publicationRecoveryPending reports whether the durable record still proves
// live writes may have started. Only the APPLYING phase does; preparing and
// prepared prove no live write began, and dispatched/verified/cleaned are
// resolved by identity rather than by restoring files.
func publicationRecoveryPending(ctx context.Context, store *storage.Store, batchID string) bool {
	if store == nil || batchID == "" {
		return false
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recoveryLeaseTimeout)
	defer cancel()
	record, err := store.GetUpdatePublication(readCtx, batchID)
	return err == nil && record != nil && record.Phase == storage.PublicationApplying
}
