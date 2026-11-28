// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package datawrite coordinates ordinary panel file mutations with a staged
// update publication on the same server data path.
//
// Ordinary writes (editor save, upload, delete, rename, copy, archive creation
// and extraction, remote download) take a shared lease. They may run together,
// but are refused while a staged publication owns the path. A staged
// publication takes the exclusive lease: it waits for ordinary writers to
// drain, refuses new ones, and keeps the lease until publication has committed
// or its rollback has finished. That closes the snapshot-to-commit window
// without putting every file operation in the lifecycle journal.
//
// The module archive-export fence in the separate export lane has the same
// exclusive shape. Integration should merge that fence and this gate so one
// per-server owner covers module archives, staged publication and ordinary
// panel writes; until then each lane keeps its own registry.
package datawrite

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	// ErrExclusiveHeld reports that a staged publication already owns the
	// server data path. Ordinary writers are refused instead of queueing behind
	// a long publication.
	ErrExclusiveHeld = errors.New("a staged update publication owns the server data path")
	// ErrExclusiveBusy reports that another exclusive claim is being acquired
	// or held. A second publisher must not interleave its snapshot and commit.
	ErrExclusiveBusy = errors.New("another exclusive data writer owns the server data path")
)

type gate struct {
	mu        sync.Mutex
	exclusive bool
	draining  bool
	shared    int
	// refs counts the leases and in-flight acquisitions that still hold a
	// pointer to this entry. The entry is removed when the last ref is gone.
	refs    int
	changed chan struct{}
}

var gates = struct {
	sync.Mutex
	byServer map[string]*gate
}{byServer: make(map[string]*gate)}

func gateFor(serverID string) *gate {
	gates.Lock()
	defer gates.Unlock()
	entry := gates.byServer[serverID]
	if entry == nil {
		entry = &gate{changed: make(chan struct{})}
		gates.byServer[serverID] = entry
	}
	entry.refs++
	return entry
}

// dropGateRef releases one hold on the entry and removes it once no lease or
// acquisition can still observe it. Callers must hold no gate.mu when calling.
func dropGateRef(serverID string, entry *gate) {
	if entry == nil {
		return
	}
	gates.Lock()
	defer gates.Unlock()
	if current, ok := gates.byServer[serverID]; !ok || current != entry {
		return
	}
	entry.mu.Lock()
	entry.refs--
	remove := entry.refs == 0 && entry.shared == 0 && !entry.exclusive && !entry.draining
	entry.mu.Unlock()
	if remove {
		delete(gates.byServer, serverID)
	}
}

// Lease is one claim on a server data path. Release is idempotent.
type Lease struct {
	gate      *gate
	serverID  string
	exclusive bool
	once      sync.Once
}

// ServerID is the server whose data path this lease covers.
func (l *Lease) ServerID() string {
	if l == nil {
		return ""
	}
	return l.serverID
}

// Exclusive reports whether this is a staged-publication lease.
func (l *Lease) Exclusive() bool {
	return l != nil && l.exclusive
}

// Release ends the claim and wakes waiters.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		gate := l.gate
		gate.mu.Lock()
		if l.exclusive {
			gate.exclusive = false
		} else if gate.shared > 0 {
			gate.shared--
		}
		close(gate.changed)
		gate.changed = make(chan struct{})
		gate.mu.Unlock()
		dropGateRef(l.serverID, gate)
	})
}

// TryShared admits an ordinary panel file mutation. It fails fast while a
// staged publication holds or is draining the data path, so the caller can
// report a retryable refusal instead of changing a target mid-publication.
func TryShared(serverID string) (*Lease, error) {
	if serverID == "" {
		return nil, errors.New("server data write requires a server ID")
	}
	gate := gateFor(serverID)
	gate.mu.Lock()
	if gate.exclusive || gate.draining {
		gate.mu.Unlock()
		dropGateRef(serverID, gate)
		return nil, fmt.Errorf("%w: %s", ErrExclusiveHeld, serverID)
	}
	gate.shared++
	gate.mu.Unlock()
	return &Lease{gate: gate, serverID: serverID}, nil
}

// AcquireExclusive admits a staged publication. It waits for ordinary writers
// already in flight to finish, refuses new ones while it waits, and then owns
// the path until Release. It fails when another exclusive claim is active or
// already draining, because two publications must not interleave.
func AcquireExclusive(ctx context.Context, serverID string) (*Lease, error) {
	if serverID == "" {
		return nil, errors.New("server data write requires a server ID")
	}
	gate := gateFor(serverID)
	gate.mu.Lock()
	if gate.exclusive || gate.draining {
		gate.mu.Unlock()
		dropGateRef(serverID, gate)
		return nil, fmt.Errorf("%w: %s", ErrExclusiveBusy, serverID)
	}
	if gate.shared == 0 {
		gate.exclusive = true
		gate.mu.Unlock()
		return &Lease{gate: gate, serverID: serverID, exclusive: true}, nil
	}
	// Reserve the drain before releasing the lock, and keep it reserved until
	// ownership is acquired or the wait is canceled. Clearing it between a
	// wake-up and the next loop iteration would let a new shared writer enter
	// after the drain had already completed, forcing the exclusive claim to
	// drain again and potentially starve it.
	gate.draining = true
	for {
		wait := gate.changed
		gate.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			gate.mu.Lock()
			gate.draining = false
			close(gate.changed)
			gate.changed = make(chan struct{})
			gate.mu.Unlock()
			dropGateRef(serverID, gate)
			return nil, ctx.Err()
		}
		gate.mu.Lock()
		if gate.shared == 0 {
			gate.exclusive = true
			gate.draining = false
			gate.mu.Unlock()
			return &Lease{gate: gate, serverID: serverID, exclusive: true}, nil
		}
	}
}
