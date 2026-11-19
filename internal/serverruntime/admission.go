// SPDX-License-Identifier: MPL-2.0
// Package serverruntime owns shared server lifecycle policy.
package serverruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
)

type AdmissionKind int

const (
	InvalidNode AdmissionKind = iota
	Unavailable
	CapacityExceeded
)

// AdmissionError describes a refusal independently of its RPC presentation.
type AdmissionError struct {
	Kind  AdmissionKind
	Cause error
}

// AdmissionCoordinator serializes same-node start admission across lifecycle
// entrypoints in one controller process. It is an in-process gate, not a
// distributed lease: persisted running/starting counts remain the source of
// truth at the next check, and a crash releases the gate. See ADR 0014.
type AdmissionCoordinator struct {
	mu   sync.Mutex
	keys map[string]*nodeAdmission
}

// nodeAdmission is a cancellable keyed mutex. Waiters are counted in refs so a
// key is only removed when no holder or waiter remains.
type nodeAdmission struct {
	token chan struct{}
	refs  int
}

// NewAdmissionCoordinator constructs an empty, instance-owned coordinator.
func NewAdmissionCoordinator() *AdmissionCoordinator {
	return &AdmissionCoordinator{keys: make(map[string]*nodeAdmission)}
}

// CanonicalNodeKey folds legacy local aliases and case differences into one
// key so alias-based and canonical callers contend on the same node. Callers
// that must dedupe gate keys (for example a migration's two nodes) use this to
// avoid acquiring the same key twice and self-deadlocking.
func CanonicalNodeKey(nodeID string) string { return canonicalNodeKey(nodeID) }

// canonicalNodeKey folds legacy local aliases and case differences into one
// key so alias-based and canonical callers contend on the same node.
func canonicalNodeKey(nodeID string) string {
	id := strings.ToLower(strings.TrimSpace(nodeID))
	if id == "" || nodestorage.IsLegacyLocalNodeID(id) {
		return "local"
	}
	return id
}

// Acquire claims the per-node admission gate before a capacity check and holds
// it until the returned release is called. Cancellation never enters the gate;
// release is idempotent and different nodes proceed in parallel.
func (c *AdmissionCoordinator) Acquire(ctx context.Context, nodeID string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, &AdmissionError{Kind: Unavailable, Cause: errors.New("admission coordinator is not configured")}
	}
	key := canonicalNodeKey(nodeID)

	c.mu.Lock()
	entry := c.keys[key]
	if entry == nil {
		entry = &nodeAdmission{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		c.keys[key] = entry
	}
	entry.refs++
	c.mu.Unlock()

	select {
	case <-entry.token:
	case <-ctx.Done():
		c.releaseEntry(key, entry)
		return nil, ctx.Err()
	}
	// Both channels can be ready at once; select may choose the token even
	// though cancellation is already observable. Never return a successful
	// gate to a cancelled operation.
	if err := ctx.Err(); err != nil {
		entry.token <- struct{}{}
		c.releaseEntry(key, entry)
		return nil, err
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			entry.token <- struct{}{}
			c.releaseEntry(key, entry)
		})
	}, nil
}

func (c *AdmissionCoordinator) releaseEntry(key string, entry *nodeAdmission) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry.refs--
	// A zero count means no holder and no waiter can still reference this
	// entry; removing it cannot create a second concurrent gate.
	if entry.refs == 0 && c.keys[key] == entry {
		delete(c.keys, key)
	}
}

func (e *AdmissionError) Error() string { return e.Cause.Error() }
func (e *AdmissionError) Unwrap() error { return e.Cause }

// CheckStartCapacity applies the same persisted active-memory accounting for
// every entrypoint. It checks admission; it does not reserve capacity or mutate
// server state. Same-node concurrent reservations remain a separate policy.
func CheckStartCapacity(ctx context.Context, store *db.Store, server *db.Server, hostMemory func(context.Context, string) (int64, error)) error {
	if server == nil {
		return errors.New("server is nil")
	}
	if store == nil || hostMemory == nil {
		return &AdmissionError{Unavailable, errors.New("cannot verify node memory capacity without a store and Docker client")}
	}
	node, err := store.GetNode(ctx, server.NodeID)
	if err != nil {
		return &AdmissionError{InvalidNode, fmt.Errorf("failed to load target node capacity: %w", err)}
	}
	hostMemoryMB, err := hostMemory(ctx, server.NodeID)
	if err != nil {
		return &AdmissionError{Unavailable, fmt.Errorf("cannot verify memory capacity on node %s: %w", node.Name, err)}
	}
	capacityMB := hostMemoryMB
	if node.MaxMemoryMB > 0 && node.MaxMemoryMB < capacityMB {
		capacityMB = node.MaxMemoryMB
	}
	if int64(server.Memory) > capacityMB {
		return &AdmissionError{CapacityExceeded, fmt.Errorf("node %s lacks memory capacity (server requires %d MB > limit %d MB)", node.Name, server.Memory, capacityMB)}
	}
	runningMemoryMB, err := store.GetNodeRunningMemoryMB(ctx, server.NodeID)
	if err != nil {
		return &AdmissionError{Unavailable, fmt.Errorf("cannot verify running memory on node %s: %w", node.Name, err)}
	}
	additionalMB := int64(server.Memory)
	if server.Status == db.StatusRunning || server.Status == db.StatusStarting {
		additionalMB = 0
	}
	if runningMemoryMB+additionalMB > capacityMB {
		return &AdmissionError{CapacityExceeded, fmt.Errorf("node %s lacks memory capacity to run server (running %d MB + requested %d MB > limit %d MB)", node.Name, runningMemoryMB, additionalMB, capacityMB)}
	}
	return nil
}
