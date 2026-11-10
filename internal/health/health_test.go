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
			{ID: "a", Status: "running", UpdatedAt: now.Add(-10 * time.Second), Game: Probe{Source: SourceSLP, ObservedAt: now.Add(-5 * time.Second), OK: true}},
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
		{ID: "b", Status: "running", UpdatedAt: now.Add(-10 * time.Second), Game: Probe{Source: SourceSLP, ObservedAt: now.Add(-5 * time.Second), OK: true}},
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

func TestAFreshObservationSweepKeepsUnchangedRowsFresh(t *testing.T) {
	// A server whose status has not changed is never rewritten, so its own
	// update time is old. The sweep is the observation, and it is recent.
	in := baseInput()
	in.Servers = []ServerObservation{
		{ID: "a", Status: "running", UpdatedAt: in.CheckedAt.Add(-6 * time.Hour), Game: Probe{Source: SourceSLP, ObservedAt: in.CheckedAt.Add(-5 * time.Second), OK: true}},
	}
	in.ObservedAt = in.CheckedAt.Add(-10 * time.Second)

	snap := Compute(in)
	if snap.Servers.Stale != 0 {
		t.Fatalf("stale = %d, want 0 while the sweep is fresh", snap.Servers.Stale)
	}
	if snap.Servers.Playable != 1 {
		t.Fatalf("playable = %d, want 1", snap.Servers.Playable)
	}
	if c, _ := snap.Component(LayerGame); c.State != StateOK {
		t.Errorf("game state = %s, want ok", c.State)
	}
}

func TestAnOldSweepStillTrustsARecentlyWrittenRow(t *testing.T) {
	// A write is evidence too: something observed this server ten seconds ago,
	// even though the sweep loop has not run since. Only a row that is old by
	// every measure is stale.
	in := baseInput()
	in.Servers = []ServerObservation{
		{ID: "a", Status: "running", UpdatedAt: in.CheckedAt.Add(-10 * time.Second), Game: Probe{Source: SourceSLP, ObservedAt: in.CheckedAt.Add(-5 * time.Second), OK: true}},
	}
	in.ObservedAt = in.CheckedAt.Add(-10 * time.Minute)

	snap := Compute(in)
	if snap.Servers.Stale != 0 {
		t.Fatalf("stale = %d, want 0 for a freshly written row", snap.Servers.Stale)
	}
	if snap.Servers.Playable != 1 {
		t.Fatalf("playable = %d, want 1", snap.Servers.Playable)
	}
}

func TestAnOldSweepAndOldRowsLeaveTheGameLayerUnknown(t *testing.T) {
	in := baseInput()
	in.Servers = []ServerObservation{
		{ID: "a", Status: "running", UpdatedAt: in.CheckedAt.Add(-10 * time.Minute)},
	}
	in.ObservedAt = in.CheckedAt.Add(-10 * time.Minute)

	snap := Compute(in)
	if snap.Servers.Stale != 1 {
		t.Fatalf("stale = %d, want the row stale when nothing observed it", snap.Servers.Stale)
	}
	if c, _ := snap.Component(LayerGame); c.State != StateUnknown {
		t.Errorf("game state = %s, want unknown", c.State)
	}
}

func TestWithoutASweepTheRowUpdateTimeDecides(t *testing.T) {
	in := baseInput()
	in.Servers = []ServerObservation{
		{ID: "a", Status: "running", UpdatedAt: in.CheckedAt.Add(-10 * time.Second), Game: Probe{Source: SourceSLP, ObservedAt: in.CheckedAt.Add(-5 * time.Second), OK: true}},
	}
	// ObservedAt unset: the fallback has to keep working.

	if snap := Compute(in); snap.Servers.Stale != 0 || snap.Servers.Playable != 1 {
		t.Fatalf("servers = %+v, want one playable and none stale", snap.Servers)
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

func TestStoredRunningWithoutJoinEvidenceIsNotPlayable(t *testing.T) {
	in := baseInput()
	in.Servers = []ServerObservation{
		{ID: "a", Status: "running", UpdatedAt: in.CheckedAt.Add(-10 * time.Second)},
	}
	snap := Compute(in)

	if snap.Servers.Playable != 0 {
		t.Errorf("playable = %d, want 0: stored running is not a join observation", snap.Servers.Playable)
	}
	if snap.Servers.Unobserved != 1 {
		t.Errorf("unobserved = %d, want 1", snap.Servers.Unobserved)
	}
	if snap.Servers.Stopped != 0 {
		t.Errorf("stopped = %d, want 0: unknown must not become stopped", snap.Servers.Stopped)
	}
	if c, _ := snap.Component(LayerGame); c.State != StateUnknown {
		t.Errorf("game state = %s, want unknown", c.State)
	}
}

func TestFailedSLPIsUnjoinableNotPlayable(t *testing.T) {
	in := baseInput()
	in.Servers = []ServerObservation{
		{ID: "a", Status: "running", UpdatedAt: in.CheckedAt, Game: Probe{Source: SourceSLP, ObservedAt: in.CheckedAt.Add(-time.Second), OK: false}},
	}
	snap := Compute(in)

	if snap.Servers.Playable != 0 || snap.Servers.Unjoinable != 1 {
		t.Fatalf("servers = %+v, want one unjoinable", snap.Servers)
	}
	if c, _ := snap.Component(LayerGame); c.State != StateDegraded {
		t.Errorf("game state = %s, want degraded", c.State)
	}
}

func TestFreshRCONCountsAsPlayable(t *testing.T) {
	in := baseInput()
	in.Servers = []ServerObservation{
		{ID: "a", Status: "running", UpdatedAt: in.CheckedAt, Game: Probe{Source: SourceRCON, ObservedAt: in.CheckedAt.Add(-time.Second), OK: true}},
	}
	snap := Compute(in)
	if snap.Servers.Playable != 1 {
		t.Fatalf("playable = %d, want 1", snap.Servers.Playable)
	}
	if c, _ := snap.Component(LayerGame); c.Source != SourceRCON {
		t.Errorf("game source = %s, want rcon", c.Source)
	}
}

func TestStaleSLPDoesNotCountAsPlayable(t *testing.T) {
	in := baseInput()
	in.Servers = []ServerObservation{
		{ID: "a", Status: "running", UpdatedAt: in.CheckedAt, Game: Probe{Source: SourceSLP, ObservedAt: in.CheckedAt.Add(-10 * time.Minute), OK: true}},
	}
	snap := Compute(in)
	if snap.Servers.Playable != 0 || snap.Servers.Unobserved != 1 {
		t.Fatalf("servers = %+v, want unobserved not playable", snap.Servers)
	}
}

func TestStartingPastDeadlineIsUnavailableNotPlayable(t *testing.T) {
	in := baseInput()
	in.StartupAfter = 3 * time.Minute
	in.Servers = []ServerObservation{
		{ID: "a", Status: "starting", UpdatedAt: in.CheckedAt, StartedAt: in.CheckedAt.Add(-10 * time.Minute)},
	}
	snap := Compute(in)
	if snap.Servers.Starting != 0 || snap.Servers.Playable != 0 || snap.Servers.Stopped != 0 {
		t.Fatalf("servers = %+v, want timed out, not starting/playable/stopped", snap.Servers)
	}
	if snap.Servers.TimedOut != 1 {
		t.Fatalf("timedOut = %d, want 1", snap.Servers.TimedOut)
	}
	c, _ := snap.Component(LayerGame)
	if c.State != StateUnavailable {
		t.Errorf("game state = %s, want unavailable", c.State)
	}
	if c.Detail == "" {
		t.Error("timed-out start needs a reason")
	}
	if snap.HTTPStatus() != http.StatusOK {
		t.Errorf("HTTP status = %d, want 200", snap.HTTPStatus())
	}
}

func TestCreatingPastDeadlineUsesUpdatedAtWhenStartIsUnknown(t *testing.T) {
	in := baseInput()
	in.ObservedAt = in.CheckedAt.Add(-10 * time.Second)
	in.Servers = []ServerObservation{
		{ID: "a", Status: "creating", UpdatedAt: in.CheckedAt.Add(-10 * time.Minute)},
	}
	snap := Compute(in)
	if snap.Servers.TimedOut != 1 || snap.Servers.Starting != 0 {
		t.Fatalf("servers = %+v, want timed out creating", snap.Servers)
	}
}

func TestOneDownNodeDegradesRuntimeWithoutFailingTheEndpoint(t *testing.T) {
	in := baseInput()
	in.Runtime = Observation{Source: SourceDockerPing, ObservedAt: in.CheckedAt}
	in.Nodes = []NodeObservation{{State: StateOK}, {State: StateUnavailable}}
	snap := Compute(in)

	c, _ := snap.Component(LayerRuntime)
	if c.State != StateDegraded {
		t.Fatalf("runtime state = %s, want degraded", c.State)
	}
	if c.Source != SourceDockerPing {
		t.Errorf("runtime source = %s, want docker-ping", c.Source)
	}
	if snap.HTTPStatus() != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200", snap.HTTPStatus())
	}
}

func TestAllDownNodesLeaveRuntimeUnavailableAndAPIReady(t *testing.T) {
	in := baseInput()
	in.Runtime = Observation{}
	in.Nodes = []NodeObservation{{State: StateUnavailable}, {State: StateUnavailable}}
	snap := Compute(in)

	c, _ := snap.Component(LayerRuntime)
	if c.State != StateUnavailable {
		t.Fatalf("runtime state = %s, want unavailable", c.State)
	}
	if snap.HTTPStatus() != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200", snap.HTTPStatus())
	}
}

func TestComputeLayers(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		mutate     func(*Input)
		playable   int
		unobserved int
		unjoinable int
		timedOut   int
		starting   int
		stopped    int
		game       State
		runtime    State
		http       int
	}{
		{
			name: "mixed playable and unobserved degrades",
			mutate: func(in *Input) {
				in.Servers = []ServerObservation{
					{ID: "a", Status: "running", UpdatedAt: now, Game: Probe{Source: SourceSLP, ObservedAt: now, OK: true}},
					{ID: "b", Status: "running", UpdatedAt: now},
				}
			},
			playable:   1,
			unobserved: 1,
			game:       StateDegraded,
			runtime:    StateOK,
			http:       http.StatusOK,
		},
		{
			name: "starting within deadline",
			mutate: func(in *Input) {
				in.Servers = []ServerObservation{
					{ID: "a", Status: "starting", UpdatedAt: now, StartedAt: now.Add(-30 * time.Second)},
				}
			},
			starting: 1,
			game:     StateStarting,
			runtime:  StateOK,
			http:     http.StatusOK,
		},
		{
			name: "database still fails the probe",
			mutate: func(in *Input) {
				in.Database = Observation{State: StateUnavailable, Source: SourceSQLite, ObservedAt: now}
			},
			playable: 1,
			stopped:  1,
			game:     StateOK,
			runtime:  StateOK,
			http:     http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := baseInput()
			tt.mutate(&in)
			snap := Compute(in)
			if snap.Servers.Playable != tt.playable || snap.Servers.Unobserved != tt.unobserved || snap.Servers.Unjoinable != tt.unjoinable || snap.Servers.TimedOut != tt.timedOut || snap.Servers.Starting != tt.starting || snap.Servers.Stopped != tt.stopped {
				t.Fatalf("servers = %+v", snap.Servers)
			}
			if c, _ := snap.Component(LayerGame); c.State != tt.game {
				t.Errorf("game = %s, want %s", c.State, tt.game)
			}
			if c, _ := snap.Component(LayerRuntime); c.State != tt.runtime {
				t.Errorf("runtime = %s, want %s", c.State, tt.runtime)
			}
			if snap.HTTPStatus() != tt.http {
				t.Errorf("HTTP = %d, want %d", snap.HTTPStatus(), tt.http)
			}
		})
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
