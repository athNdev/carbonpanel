// SPDX-License-Identifier: MPL-2.0
// Package serverruntime owns shared server lifecycle policy.
package serverruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/athNdev/carbon-panel/internal/db"
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
