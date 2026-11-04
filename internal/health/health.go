// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package health separates the layers a deployment acts on: the process is
// alive, the database answers, a Docker runtime is reachable, a server is
// starting, and a server is playable. A layer that cannot be observed is
// reported as unknown rather than healthy, and a degraded runtime never makes
// the API look unavailable.
package health

import (
	"fmt"
	"net/http"
	"sort"
	"time"
)

// State is the outcome of one observation. It is deliberately smaller than
// "healthy/unhealthy": unknown and starting are real answers and must not be
// rounded to one of the two.
type State string

const (
	// StateOK means the layer answered and is operating normally.
	StateOK State = "ok"
	// StateStarting means the layer is doing expected work and is not ready yet.
	StateStarting State = "starting"
	// StateDegraded means the layer answered but one part of it is impaired.
	StateDegraded State = "degraded"
	// StateUnavailable means the layer was asked and failed.
	StateUnavailable State = "unavailable"
	// StateUnknown means the layer could not be observed, or its last
	// observation is too old to trust.
	StateUnknown State = "unknown"
)

// Layer names the part of the system an observation belongs to.
type Layer string

const (
	LayerProcess  Layer = "process"
	LayerDatabase Layer = "database"
	LayerRuntime  Layer = "runtime"
	LayerGame     Layer = "game"
	LayerRoute    Layer = "route"
)

// Observation is one layer's state plus why.
type Observation struct {
	State  State  `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// ServerObservation is one server's stored status and the age of that status.
// Age is used to decide whether the stored value is still evidence.
type ServerObservation struct {
	ID        string
	Status    string
	UpdatedAt time.Time
}

// Input is everything a snapshot is computed from. It carries no names or
// hostnames: the snapshot is an unauthenticated aggregate.
type Input struct {
	StartedAt  time.Time
	CheckedAt  time.Time
	StaleAfter time.Duration
	Database   Observation
	Runtime    Observation
	Routes     Observation
	Servers    []ServerObservation
	// RunningStatus is the stored status that means the container is up and
	// the game loop should be running.
	RunningStatus string
	// StartingStatus is the stored status used while the panel is preparing or
	// booting a server.
	StartingStatus string
	// ObservedAt is when the last full observation sweep completed. When it is
	// recent, every stored status was re-read at that moment, so a row that has
	// not been rewritten is still fresh. Without it a status can only be judged
	// by its own update time, which never moves while the status is unchanged.
	ObservedAt time.Time
}

// GameSummary is the aggregate of the server records.
type GameSummary struct {
	// Playable is the number of servers whose running status is fresh.
	Playable int `json:"playable"`
	// Starting is the number of servers in a starting state.
	Starting int `json:"starting"`
	// Stopped is the number of servers with a fresh non-running, non-starting
	// status.
	Stopped int `json:"stopped"`
	// Stale is the number of servers whose stored status is too old to trust.
	Stale int `json:"stale"`
	// Total is every server the panel knows about.
	Total int `json:"total"`
}

// Component is one line of a snapshot.
type Component struct {
	Layer  Layer  `json:"layer"`
	State  State  `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// Snapshot is the layered answer. Overall is the worst layer that should page
// someone; a degraded runtime or one stale server does not make the API
// unavailable.
type Snapshot struct {
	Status        State       `json:"status"`
	UptimeSeconds int64       `json:"uptimeSeconds"`
	CheckedAt     time.Time   `json:"checkedAt"`
	Components    []Component `json:"components"`
	Servers       GameSummary `json:"servers"`
}

// Compute assembles the layered picture from the collected input.
func Compute(in Input) Snapshot {
	checkedAt := in.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}
	uptime := int64(0)
	if !in.StartedAt.IsZero() {
		uptime = int64(checkedAt.Sub(in.StartedAt).Seconds())
		if uptime < 0 {
			uptime = 0
		}
	}

	game, servers := summariseServers(in)

	components := []Component{
		{Layer: LayerProcess, State: StateOK, Detail: "the API process is serving"},
		{Layer: LayerDatabase, State: in.Database.State, Detail: in.Database.Detail},
		{Layer: LayerRuntime, State: in.Runtime.State, Detail: in.Runtime.Detail},
		{Layer: LayerGame, State: game.State, Detail: game.Detail},
		{Layer: LayerRoute, State: in.Routes.State, Detail: in.Routes.Detail},
	}
	for i := range components {
		if components[i].State == "" {
			// An unset observation is not evidence of health.
			components[i].State = StateUnknown
			components[i].Detail = "not observed"
		}
	}

	return Snapshot{
		Status:        overall(components),
		UptimeSeconds: uptime,
		CheckedAt:     checkedAt,
		Components:    components,
		Servers:       servers,
	}
}

// summariseServers classifies the server records into the game layer.
func summariseServers(in Input) (Component, GameSummary) {
	summary := GameSummary{Total: len(in.Servers)}
	staleAfter := in.StaleAfter
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	checkedAt := in.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}

	sweepIsFresh := !in.ObservedAt.IsZero() && checkedAt.Sub(in.ObservedAt) <= staleAfter
	for _, s := range in.Servers {
		if !sweepIsFresh && !s.UpdatedAt.IsZero() && checkedAt.Sub(s.UpdatedAt) > staleAfter {
			summary.Stale++
			continue
		}
		switch s.Status {
		case in.RunningStatus:
			summary.Playable++
		case in.StartingStatus:
			summary.Starting++
		default:
			summary.Stopped++
		}
	}

	switch {
	case summary.Total == 0:
		return Component{Layer: LayerGame, State: StateOK, Detail: "no servers are registered"}, summary
	case summary.Stale == summary.Total:
		return Component{
			Layer:  LayerGame,
			State:  StateUnknown,
			Detail: fmt.Sprintf("no server status was observed within %s", staleAfter),
		}, summary
	case summary.Stale > 0:
		return Component{
			Layer:  LayerGame,
			State:  StateDegraded,
			Detail: fmt.Sprintf("%d of %d server statuses are older than %s", summary.Stale, summary.Total, staleAfter),
		}, summary
	case summary.Starting > 0:
		return Component{
			Layer:  LayerGame,
			State:  StateStarting,
			Detail: fmt.Sprintf("%d of %d servers are starting", summary.Starting, summary.Total),
		}, summary
	default:
		return Component{
			Layer:  LayerGame,
			State:  StateOK,
			Detail: fmt.Sprintf("%d of %d servers are running", summary.Playable, summary.Total),
		}, summary
	}
}

// overall folds the layers into the status a deployment should act on.
func overall(components []Component) State {
	worst := StateOK
	for _, c := range components {
		if severity(c.State) > severity(worst) {
			worst = c.State
		}
	}
	return worst
}

// severity orders states from best to worst. Unknown sits with degraded: it is
// a missing observation, not a proven outage.
func severity(s State) int {
	switch s {
	case StateOK, StateStarting:
		return 0
	case StateUnknown, StateDegraded:
		return 1
	case StateUnavailable:
		return 2
	default:
		return 1
	}
}

// HTTPStatus maps a snapshot to a status code. Only an unavailable core layer
// (the process itself or the database) makes the endpoint fail: a deployment
// must not restart the API because a remote Docker node is down.
func (s Snapshot) HTTPStatus() int {
	for _, c := range s.Components {
		if c.Layer == LayerDatabase && c.State == StateUnavailable {
			return http.StatusServiceUnavailable
		}
	}
	return http.StatusOK
}

// Component returns the named layer's component.
func (s Snapshot) Component(layer Layer) (Component, bool) {
	for _, c := range s.Components {
		if c.Layer == layer {
			return c, true
		}
	}
	return Component{}, false
}

// SortedStates returns the distinct states in a stable order, which makes the
// snapshot easy to assert in tests and to read in logs.
func (s Snapshot) SortedStates() []State {
	seen := map[State]struct{}{}
	var out []State
	for _, c := range s.Components {
		if _, ok := seen[c.State]; ok {
			continue
		}
		seen[c.State] = struct{}{}
		out = append(out, c.State)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
