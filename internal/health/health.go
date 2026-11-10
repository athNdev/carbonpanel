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

const (
	// SourceProcess is the API process itself.
	SourceProcess = "process"
	// SourceSQLite is a database ping.
	SourceSQLite = "sqlite"
	// SourceDockerPing is a Docker node probe.
	SourceDockerPing = "docker-ping"
	// SourceSLP is a Minecraft server-list ping.
	SourceSLP = "slp"
	// SourceRCON is an RCON command that already returned.
	SourceRCON = "rcon"
	// SourceReconciler is the reconciler's last full sweep.
	SourceReconciler = "reconciler-sweep"
	// SourceProxy is the TCP proxy manager.
	SourceProxy = "proxy"
)

// DefaultStaleAfter is how long a stored status or join probe remains evidence.
const DefaultStaleAfter = 2 * time.Minute

// DefaultStartupAfter is how long a server may stay starting before the
// game layer reports it as past the deadline. It matches the SLP boot gate.
const DefaultStartupAfter = 3 * time.Minute

// Observation is one layer's state plus why.
type Observation struct {
	State      State     `json:"state"`
	Detail     string    `json:"detail,omitempty"`
	Source     string    `json:"source,omitempty"`
	ObservedAt time.Time `json:"observedAt,omitempty"`
}

// Probe is a bounded observation of joinability. A zero ObservedAt means the
// probe never ran: that is unknown, not a failed join.
type Probe struct {
	Source     string
	ObservedAt time.Time
	OK         bool
}

// NodeObservation is one Docker node's probe result. It carries no hostname.
type NodeObservation struct {
	State State
}

// ServerObservation is one server's stored status, optional join evidence, and
// when that server began starting. Age is used to decide whether the stored
// value is still evidence.
type ServerObservation struct {
	ID        string
	Status    string
	UpdatedAt time.Time
	StartedAt time.Time
	Game      Probe
}

// Input is everything a snapshot is computed from. It carries no names or
// hostnames: the snapshot is an unauthenticated aggregate.
type Input struct {
	StartedAt  time.Time
	CheckedAt  time.Time
	StaleAfter time.Duration
	// StartupAfter is how long a server may remain starting. Zero uses
	// DefaultStartupAfter.
	StartupAfter time.Duration
	Database     Observation
	Runtime      Observation
	Routes       Observation
	// Nodes, when non-empty, is folded into the runtime layer. A missing
	// Runtime observation is then filled from these probes so one down node
	// degrades runtime without failing the API.
	Nodes   []NodeObservation
	Servers []ServerObservation
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
	// Playable is the number of running servers with a fresh successful SLP or
	// RCON observation. Stored StatusRunning alone is not playable.
	Playable int `json:"playable"`
	// Starting is the number of servers still within the startup deadline.
	Starting int `json:"starting"`
	// Stopped is the number of servers with a fresh non-running, non-starting
	// status. Unobserved running servers are never counted here.
	Stopped int `json:"stopped"`
	// Stale is the number of servers whose stored status is too old to trust.
	Stale int `json:"stale"`
	// Unobserved is running servers with no fresh SLP or RCON evidence.
	Unobserved int `json:"unobserved"`
	// Unjoinable is running servers whose fresh join probe failed.
	Unjoinable int `json:"unjoinable"`
	// TimedOut is servers still marked starting after the startup deadline.
	TimedOut int `json:"timedOut"`
	// Total is every server the panel knows about.
	Total int `json:"total"`
}

// Component is one line of a snapshot.
type Component struct {
	Layer      Layer     `json:"layer"`
	State      State     `json:"state"`
	Detail     string    `json:"detail,omitempty"`
	Source     string    `json:"source,omitempty"`
	ObservedAt time.Time `json:"observedAt,omitempty"`
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

	runtime := summariseRuntime(in)
	game, servers := summariseServers(in)

	components := []Component{
		{Layer: LayerProcess, State: StateOK, Detail: "the API process is serving", Source: SourceProcess, ObservedAt: checkedAt},
		componentFrom(LayerDatabase, in.Database),
		componentFrom(LayerRuntime, runtime),
		game,
		componentFrom(LayerRoute, in.Routes),
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

func componentFrom(layer Layer, obs Observation) Component {
	return Component{
		Layer:      layer,
		State:      obs.State,
		Detail:     obs.Detail,
		Source:     obs.Source,
		ObservedAt: obs.ObservedAt,
	}
}

// summariseRuntime folds per-node probes into one runtime observation. A
// single down node degrades the layer; it never fails the API by itself.
func summariseRuntime(in Input) Observation {
	if len(in.Nodes) == 0 {
		return in.Runtime
	}

	n := len(in.Nodes)
	ok, unavail, unknown := 0, 0, 0
	for _, node := range in.Nodes {
		switch node.State {
		case StateOK:
			ok++
		case StateUnavailable:
			unavail++
		default:
			unknown++
		}
	}

	src := in.Runtime.Source
	if src == "" {
		src = SourceDockerPing
	}
	at := in.Runtime.ObservedAt
	switch {
	case unavail == n:
		return Observation{
			State:      StateUnavailable,
			Detail:     fmt.Sprintf("all %d docker nodes did not answer", n),
			Source:     src,
			ObservedAt: at,
		}
	case ok == n:
		return Observation{
			State:      StateOK,
			Detail:     fmt.Sprintf("all %d docker nodes answered", n),
			Source:     src,
			ObservedAt: at,
		}
	case unavail > 0:
		return Observation{
			State:      StateDegraded,
			Detail:     fmt.Sprintf("%d of %d docker nodes did not answer", unavail, n),
			Source:     src,
			ObservedAt: at,
		}
	default:
		return Observation{
			State:      StateUnknown,
			Detail:     fmt.Sprintf("%d of %d docker nodes were not observed", unknown, n),
			Source:     src,
			ObservedAt: at,
		}
	}
}

func isStarting(status, startingStatus string) bool {
	if startingStatus != "" && status == startingStatus {
		return true
	}
	return status == "starting" || status == "creating"
}

func probeIsFresh(p Probe, checkedAt time.Time, staleAfter time.Duration) bool {
	if p.ObservedAt.IsZero() {
		return false
	}
	return checkedAt.Sub(p.ObservedAt) <= staleAfter
}

func startClock(s ServerObservation) time.Time {
	if !s.StartedAt.IsZero() {
		return s.StartedAt
	}
	return s.UpdatedAt
}

// summariseServers classifies the server records into the game layer.
func summariseServers(in Input) (Component, GameSummary) {
	summary := GameSummary{Total: len(in.Servers)}
	staleAfter := in.StaleAfter
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	startupAfter := in.StartupAfter
	if startupAfter <= 0 {
		startupAfter = DefaultStartupAfter
	}
	checkedAt := in.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}

	sweepIsFresh := !in.ObservedAt.IsZero() && checkedAt.Sub(in.ObservedAt) <= staleAfter
	source := SourceReconciler
	var observedAt time.Time
	if sweepIsFresh {
		observedAt = in.ObservedAt
	}
	for _, s := range in.Servers {
		if !sweepIsFresh && !s.UpdatedAt.IsZero() && checkedAt.Sub(s.UpdatedAt) > staleAfter {
			summary.Stale++
			continue
		}
		switch {
		case s.Status == in.RunningStatus:
			if !probeIsFresh(s.Game, checkedAt, staleAfter) {
				summary.Unobserved++
				continue
			}
			if s.Game.Source != "" {
				source = s.Game.Source
			}
			observedAt = s.Game.ObservedAt
			if s.Game.OK {
				summary.Playable++
			} else {
				summary.Unjoinable++
			}
		case isStarting(s.Status, in.StartingStatus):
			started := startClock(s)
			if !started.IsZero() && checkedAt.Sub(started) > startupAfter {
				summary.TimedOut++
				continue
			}
			summary.Starting++
		default:
			summary.Stopped++
		}
	}

	game := Component{Layer: LayerGame, Source: source, ObservedAt: observedAt}
	switch {
	case summary.Total == 0:
		game.State = StateOK
		game.Detail = "no servers are registered"
		game.Source = SourceReconciler
	case summary.Stale == summary.Total:
		game.State = StateUnknown
		game.Detail = fmt.Sprintf("no server status was observed within %s", staleAfter)
	case summary.Playable == 0 && summary.Unobserved > 0 && summary.Unjoinable == 0 && summary.TimedOut == 0 && summary.Starting == 0 && summary.Stale == 0:
		game.State = StateUnknown
		game.Detail = fmt.Sprintf("%d of %d running servers have no fresh SLP or RCON observation", summary.Unobserved, summary.Total)
	case summary.TimedOut > 0 && summary.Playable == 0 && summary.Starting == 0:
		game.State = StateUnavailable
		game.Detail = fmt.Sprintf("%d of %d servers exceeded the startup deadline of %s", summary.TimedOut, summary.Total, startupAfter)
	case summary.Unjoinable > 0 || summary.TimedOut > 0 || summary.Stale > 0 || summary.Unobserved > 0:
		game.State = StateDegraded
		switch {
		case summary.Unjoinable > 0:
			game.Detail = fmt.Sprintf("%d of %d running servers did not answer SLP or RCON", summary.Unjoinable, summary.Total)
		case summary.TimedOut > 0:
			game.Detail = fmt.Sprintf("%d of %d servers exceeded the startup deadline of %s", summary.TimedOut, summary.Total, startupAfter)
		case summary.Unobserved > 0:
			game.Detail = fmt.Sprintf("%d of %d running servers have no fresh SLP or RCON observation", summary.Unobserved, summary.Total)
		default:
			game.Detail = fmt.Sprintf("%d of %d server statuses are older than %s", summary.Stale, summary.Total, staleAfter)
		}
	case summary.Starting > 0:
		game.State = StateStarting
		game.Detail = fmt.Sprintf("%d of %d servers are starting", summary.Starting, summary.Total)
	default:
		game.State = StateOK
		game.Detail = fmt.Sprintf("%d of %d servers are playable", summary.Playable, summary.Total)
	}
	return game, summary
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
