// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package health

import (
	"net/http"
	"testing"
	"time"
)

func baseInput() Input {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	return Input{
		StartedAt:      now.Add(-time.Hour),
		CheckedAt:      now,
		StaleAfter:     2 * time.Minute,
		Database:       Observation{State: StateOK},
		Runtime:        Observation{State: StateOK},
		Routes:         Observation{State: StateOK},
		RunningStatus:  "running",
		StartingStatus: "creating",
		Servers: []ServerObservation{
			{ID: "a", Status: "running", UpdatedAt: now.Add(-10 * time.Second)},
			{ID: "b", Status: "stopped", UpdatedAt: now.Add(-10 * time.Second)},
		},
	}
}

func TestSnapshotReportsEveryLayer(t *testing.T) {
	snap := Compute(baseInput())

	if snap.Status != StateOK {
		t.Fatalf("status = %s, want ok: %+v", snap.Status, snap.Components)
	}
	for _, layer := range []Layer{LayerProcess, LayerDatabase, LayerRuntime, LayerGame, LayerRoute} {
		if _, ok := snap.Component(layer); !ok {
			t.Errorf("layer %s missing from the snapshot", layer)
		}
	}
	if snap.Servers.Playable != 1 || snap.Servers.Stopped != 1 || snap.Servers.Total != 2 {
		t.Errorf("servers = %+v, want one playable, one stopped", snap.Servers)
	}
	if snap.UptimeSeconds != 3600 {
		t.Errorf("uptime = %d, want 3600", snap.UptimeSeconds)
	}
}

func TestASingleRuntimeOutageDegradesWithoutFailingTheEndpoint(t *testing.T) {
	in := baseInput()
	in.Runtime = Observation{State: StateUnavailable, Detail: "docker node \"remote-1\" did not answer within 2s"}
	snap := Compute(in)

	if snap.Status != StateUnavailable {
		t.Fatalf("status = %s, want unavailable", snap.Status)
	}
	if got := snap.HTTPStatus(); got != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200: a runtime outage must not restart the API", got)
	}
}

func TestADatabaseOutageFailsTheEndpoint(t *testing.T) {
	in := baseInput()
	in.Database = Observation{State: StateUnavailable, Detail: "database ping failed"}
	snap := Compute(in)

	if got := snap.HTTPStatus(); got != http.StatusServiceUnavailable {
		t.Fatalf("HTTP status = %d, want 503", got)
	}
}

func TestAMissingObservationIsUnknownNotHealthy(t *testing.T) {
	in := baseInput()
	in.Routes = Observation{}
	in.Runtime = Observation{}
	snap := Compute(in)

	if c, _ := snap.Component(LayerRoute); c.State != StateUnknown {
		t.Errorf("route state = %s, want unknown", c.State)
	}
	if c, _ := snap.Component(LayerRuntime); c.State != StateUnknown {
		t.Errorf("runtime state = %s, want unknown", c.State)
	}
	if snap.Status == StateOK {
		t.Error("a snapshot with unobserved layers reported ok")
	}
}

func TestAStaleRunningServerIsNotCountedAsPlayable(t *testing.T) {
	in := baseInput()
	now := in.CheckedAt
	in.Servers = []ServerObservation{
		{ID: "a", Status: "running", UpdatedAt: now.Add(-10 * time.Minute)},
		{ID: "b", Status: "running", UpdatedAt: now.Add(-10 * time.Second)},
	}
	snap := Compute(in)

	if snap.Servers.Playable != 1 {
		t.Errorf("playable = %d, want 1", snap.Servers.Playable)
	}
	if snap.Servers.Stale != 1 {
		t.Errorf("stale = %d, want 1", snap.Servers.Stale)
	}
	c, _ := snap.Component(LayerGame)
	if c.State != StateDegraded {
		t.Errorf("game state = %s, want degraded", c.State)
	}
}

func TestEveryStaleStatusLeavesTheGameLayerUnknown(t *testing.T) {
	in := baseInput()
	in.Servers = []ServerObservation{
		{ID: "a", Status: "running", UpdatedAt: in.CheckedAt.Add(-time.Hour)},
	}
	snap := Compute(in)

	c, _ := snap.Component(LayerGame)
	if c.State != StateUnknown {
		t.Fatalf("game state = %s, want unknown", c.State)
	}
	if snap.Servers.Playable != 0 {
		t.Errorf("playable = %d, want 0", snap.Servers.Playable)
	}
}

func TestAStartingServerIsStartingNotRunning(t *testing.T) {
	in := baseInput()
	in.Servers = []ServerObservation{
		{ID: "a", Status: "creating", UpdatedAt: in.CheckedAt},
	}
	snap := Compute(in)

	if snap.Servers.Starting != 1 || snap.Servers.Playable != 0 {
		t.Fatalf("servers = %+v, want one starting", snap.Servers)
	}
	c, _ := snap.Component(LayerGame)
	if c.State != StateStarting {
		t.Errorf("game state = %s, want starting", c.State)
	}
	if snap.Status != StateOK {
		t.Errorf("overall status = %s, want ok: a starting server is expected work", snap.Status)
	}
}

func TestNoServersIsOK(t *testing.T) {
	in := baseInput()
	in.Servers = nil
	snap := Compute(in)

	c, _ := snap.Component(LayerGame)
	if c.State != StateOK {
		t.Errorf("game state = %s, want ok", c.State)
	}
	if snap.Servers.Total != 0 {
		t.Errorf("total = %d, want 0", snap.Servers.Total)
	}
}

func TestSnapshotIsDeterministic(t *testing.T) {
	in := baseInput()
	in.Runtime = Observation{State: StateDegraded}
	first := Compute(in)
	second := Compute(in)

	if first.Status != second.Status {
		t.Fatalf("status changed between calls: %s then %s", first.Status, second.Status)
	}
	for i := range first.Components {
		if first.Components[i] != second.Components[i] {
			t.Fatalf("component %d changed between calls: %+v then %+v", i, first.Components[i], second.Components[i])
		}
	}
}
