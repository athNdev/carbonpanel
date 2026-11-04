// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package reconciler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// recordingModules is a ModuleReconciler that records every pass.
type recordingModules struct {
	mu      sync.Mutex
	seen    []string
	err     error
	outcome string
}

func (r *recordingModules) ReconcileModule(_ context.Context, moduleID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, moduleID)
	if r.err != nil {
		return "", r.err
	}
	if r.outcome == "" {
		return "in_sync", nil
	}
	return r.outcome, nil
}

func (r *recordingModules) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

// moduleAwareStore adds ListModules to the test store, which is how the sweep
// discovers modules.
type moduleAwareStore struct {
	Store
	modules []*db.Module
}

func (s *moduleAwareStore) ListModules(context.Context) ([]*db.Module, error) {
	return s.modules, nil
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not reached within 2s")
}

func TestModuleEventsScheduleAModulePass(t *testing.T) {
	modules := &recordingModules{}
	r := New(&fakeStore{}, &fakeResolver{}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
	})
	r.SetModuleReconciler(modules)

	events := make(chan ContainerEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx, events)
	defer r.Stop()

	events <- ContainerEvent{ModuleID: "mod-1", ContainerID: "abc"}

	waitFor(t, func() bool { return len(modules.calls()) == 1 })
	if got := modules.calls()[0]; got != "mod-1" {
		t.Fatalf("reconciled %q, want mod-1", got)
	}
}

func TestAStageEventForAModuleIsNotTreatedAsAServer(t *testing.T) {
	modules := &recordingModules{}
	r := New(&fakeStore{}, &fakeResolver{}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
	})
	r.SetModuleReconciler(modules)

	events := make(chan ContainerEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx, events)
	defer r.Stop()

	events <- ContainerEvent{ModuleID: "mod-2", ServerID: ""}

	waitFor(t, func() bool { return len(modules.calls()) == 1 })
	if modules.calls()[0] != "mod-2" {
		t.Fatalf("calls = %v, want the module pass", modules.calls())
	}
}

func TestTheSweepAlsoCoversModulesWithAContainer(t *testing.T) {
	modules := &recordingModules{}
	store := &moduleAwareStore{
		Store: &fakeStore{},
		modules: []*db.Module{
			{ID: "mod-with-container", ContainerID: "abc", Status: db.ModuleStatusRunning},
			{ID: "mod-without-container", ContainerID: "", Status: db.ModuleStatusStopped},
		},
	}
	r := New(store, &fakeResolver{}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
	})
	r.SetModuleReconciler(modules)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx, nil)
	defer r.Stop()

	waitFor(t, func() bool { return len(modules.calls()) == 1 })
	calls := modules.calls()
	if len(calls) != 1 || calls[0] != "mod-with-container" {
		t.Fatalf("calls = %v, want only the module that has a container", calls)
	}
}

func TestModulePassesAreDisabledWithoutAModuleReconciler(t *testing.T) {
	r := New(&fakeStore{}, &fakeResolver{}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
	})
	events := make(chan ContainerEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx, events)
	defer r.Stop()

	// Neither of these may block or panic when the module system is absent.
	events <- ContainerEvent{ModuleID: "mod-3"}
	time.Sleep(80 * time.Millisecond)
}

func TestAModuleFailureIsLoggedAndDoesNotStopTheWorker(t *testing.T) {
	modules := &recordingModules{err: errors.New("daemon unavailable")}
	r := New(&fakeStore{}, &fakeResolver{}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
	})
	r.SetModuleReconciler(modules)

	events := make(chan ContainerEvent, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx, events)
	defer r.Stop()

	events <- ContainerEvent{ModuleID: "mod-4"}
	waitFor(t, func() bool { return len(modules.calls()) == 1 })
	events <- ContainerEvent{ModuleID: "mod-5"}
	waitFor(t, func() bool { return len(modules.calls()) == 2 })

	if got := modules.calls(); got[1] != "mod-5" {
		t.Fatalf("calls = %v, want the worker to keep processing after an error", got)
	}
}

func TestLastSweepAtAdvancesWithASweep(t *testing.T) {
	r := New(&fakeStore{}, &fakeResolver{}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
	})
	if got := r.LastSweepAt(); !got.IsZero() {
		t.Fatalf("LastSweepAt before any sweep = %v, want the zero time", got)
	}

	before := time.Now()
	r.enqueueAll(context.Background())
	after := r.LastSweepAt()

	if after.IsZero() {
		t.Fatal("LastSweepAt is zero after a sweep")
	}
	if after.Before(before.Add(-time.Second)) || after.After(time.Now().Add(time.Second)) {
		t.Fatalf("LastSweepAt = %v, want a time around now", after)
	}
}
