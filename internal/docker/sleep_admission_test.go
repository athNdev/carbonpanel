// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/athNdev/carbon-panel/internal/db"
)

// recordingAdmissionGate is a test double for the shared in-process start gate.
type recordingAdmissionGate struct {
	mu       sync.Mutex
	acquired int
	released int
	active   bool
	err      error
}

func (g *recordingAdmissionGate) Acquire(_ context.Context, _ string) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return nil, g.err
	}
	g.acquired++
	g.active = true
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.released++
		g.active = false
	}, nil
}

func (g *recordingAdmissionGate) isActive() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active
}

func (g *recordingAdmissionGate) counts() (int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.acquired, g.released
}

// gateCheckingStore fails the test if status persistence happens without the
// admission gate still being held.
type gateCheckingStore struct {
	SleeperStore
	gate *recordingAdmissionGate
	bad  bool
}

func (s *gateCheckingStore) UpdateServer(ctx context.Context, server *db.Server) error {
	if !s.gate.isActive() {
		s.bad = true
	}
	return s.SleeperStore.UpdateServer(ctx, server)
}

func deepSleepAdmissionHarness() (*mockSleepController, *mockFreezerStore, *DeepSleepManager) {
	controller, _, store := newDeepSleepHarness()
	store.servers["srv-sleep"].Status = db.StatusDeepSleep
	controller.status["cnt-sleep"] = db.StatusStopped
	return controller, store, NewDeepSleepManager(controller, store, nil, nil, nil)
}

// TestDeepSleepWakeRefusesAdmissionBeforeStart proves a refusal reaches the
// caller before the container is started or status is changed.
func TestDeepSleepWakeRefusesAdmissionBeforeStart(t *testing.T) {
	controller, store, mgr := deepSleepAdmissionHarness()
	mgr.SetAdmissionGate(&recordingAdmissionGate{err: errors.New("node admission busy")})

	err := mgr.WakeServer(context.Background(), "srv-sleep")
	if err == nil || !strings.Contains(err.Error(), "node admission busy") {
		t.Fatalf("WakeServer error = %v, want admission refusal", err)
	}
	controller.mu.Lock()
	starts := len(controller.starts)
	controller.mu.Unlock()
	if starts != 0 {
		t.Fatalf("refused wake started the container %d times", starts)
	}
	got, err := store.GetServer(context.Background(), "srv-sleep")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != db.StatusDeepSleep || got.LastStarted != nil {
		t.Fatalf("refused wake changed server = %+v", got)
	}
}

// TestDeepSleepWakeHoldsAdmissionThroughStartAndPersistence proves the gate is
// held at StartContainer and at status persistence, then released exactly once.
func TestDeepSleepWakeHoldsAdmissionThroughStartAndPersistence(t *testing.T) {
	controller, store, mgr := deepSleepAdmissionHarness()
	gate := &recordingAdmissionGate{}
	checkingStore := &gateCheckingStore{SleeperStore: store, gate: gate}
	mgr = NewDeepSleepManager(controller, checkingStore, nil, nil, nil)
	mgr.SetAdmissionGate(gate)

	controller.startHook = func(containerID string) {
		if !gate.isActive() {
			t.Error("StartContainer ran without the admission gate")
		}
		// The readiness loop reads this through GetContainerStatus.
		controller.status[containerID] = db.StatusRunning
	}

	if err := mgr.WakeServer(context.Background(), "srv-sleep"); err != nil {
		t.Fatalf("WakeServer: %v", err)
	}
	if checkingStore.bad {
		t.Fatal("status persistence ran without the admission gate")
	}
	acquired, released := gate.counts()
	if acquired != 1 || released != 1 {
		t.Fatalf("gate acquired=%d released=%d, want 1/1", acquired, released)
	}
	if gate.isActive() {
		t.Fatal("gate still held after wake finished")
	}
	got, err := store.GetServer(context.Background(), "srv-sleep")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != db.StatusRunning || got.LastStarted == nil {
		t.Fatalf("wake state = %+v, want running with LastStarted", got)
	}
}
