// SPDX-License-Identifier: MPL-2.0

package module

import (
	"context"
	"errors"
	"sync"
)

// ErrModuleLifecycleInFlight reports a module mutation or archive fence that
// already owns the server's module lifecycle.
var ErrModuleLifecycleInFlight = errors.New("module lifecycle operation is in progress")

// ErrModuleArchiveExport reports that a server archive export owns the module
// lifecycle. Mutations must not start a writer while the archive is read.
var ErrModuleArchiveExport = errors.New("a server archive export is in progress")

// moduleGate serializes panel-driven module lifecycle operations for one
// server. An archive fence is exclusive with every mutation; mutations
// serialize with each other but fail fast when an archive is being read.
type moduleGate struct {
	mu       sync.Mutex
	mutation bool
	export   bool
	changed  chan struct{}
}

var moduleGates = struct {
	sync.Mutex
	gates map[string]*moduleGate
}{gates: make(map[string]*moduleGate)}

func moduleGateFor(serverID string) *moduleGate {
	moduleGates.Lock()
	defer moduleGates.Unlock()
	gate := moduleGates.gates[serverID]
	if gate == nil {
		gate = &moduleGate{changed: make(chan struct{})}
		moduleGates.gates[serverID] = gate
	}
	return gate
}

// ModuleTransition is one claim on a server's module lifecycle. Only the
// handle that acquired it can release it; Release is idempotent.
type ModuleTransition struct {
	gate   *moduleGate
	export bool
	once   sync.Once
}

// Release ends the claim and wakes waiters.
func (t *ModuleTransition) Release() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		gate := t.gate
		gate.mu.Lock()
		if t.export {
			gate.export = false
		} else {
			gate.mutation = false
		}
		close(gate.changed)
		gate.changed = make(chan struct{})
		gate.mu.Unlock()
	})
}

// beginServerModuleMutation waits for another module mutation to finish. It
// refuses immediately while an archive export owns the server: an export may
// hold the fence for a long time and a module mutation must not queue behind
// it only to start a writer when the archive is complete.
func beginServerModuleMutation(ctx context.Context, serverID string) (*ModuleTransition, error) {
	if serverID == "" {
		return nil, errors.New("module lifecycle transition requires a server ID")
	}
	gate := moduleGateFor(serverID)
	for {
		gate.mu.Lock()
		if gate.export {
			gate.mu.Unlock()
			return nil, ErrModuleArchiveExport
		}
		if !gate.mutation {
			gate.mutation = true
			gate.mu.Unlock()
			return &ModuleTransition{gate: gate}, nil
		}
		wait := gate.changed
		gate.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// TryBeginServerModuleArchive claims the exclusive stopped-export fence. It
// fails rather than waiting when any module mutation is already in flight, so
// an export never reads while a module create, start, update or delete could
// still change the effective writer state.
func TryBeginServerModuleArchive(serverID string) (*ModuleTransition, error) {
	if serverID == "" {
		return nil, errors.New("module archive fence requires a server ID")
	}
	gate := moduleGateFor(serverID)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.mutation || gate.export {
		return nil, ErrModuleLifecycleInFlight
	}
	gate.export = true
	return &ModuleTransition{gate: gate, export: true}, nil
}
