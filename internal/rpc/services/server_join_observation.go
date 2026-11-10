// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/health"
	"github.com/athNdev/carbon-panel/internal/metrics"
)

// joinProbeFromMetrics copies already-collected SLP or RCON freshness. It
// never pings the game: a missing metric stays unobserved.
func joinProbeFromMetrics(m *metrics.ServerMetrics) health.Probe {
	if m == nil {
		return health.Probe{}
	}
	if !m.SLPLastUpdated.IsZero() {
		return health.Probe{Source: health.SourceSLP, ObservedAt: m.SLPLastUpdated, OK: m.SLPAvailable}
	}
	if !m.RCONLastUpdated.IsZero() {
		return health.Probe{Source: health.SourceRCON, ObservedAt: m.RCONLastUpdated, OK: true}
	}
	return health.Probe{}
}

func classifyServerJoin(server *storage.Server, m *metrics.ServerMetrics, now time.Time) health.Reading {
	obs := health.ServerObservation{
		ID:        server.ID,
		Status:    string(server.Status),
		UpdatedAt: server.UpdatedAt,
		Game:      joinProbeFromMetrics(m),
	}
	if server.LastStarted != nil {
		obs.StartedAt = *server.LastStarted
	}
	return health.ClassifyServer(obs, health.Input{
		CheckedAt:      now,
		RunningStatus:  string(storage.StatusRunning),
		StartingStatus: string(storage.StatusCreating),
	})
}

// applyCachedMetrics copies docker resource stats always, and copies live SLP
// / player fields only when ClassifyServer says the join probe is current
// playable evidence. Last-good samples, MOTD and favicons stay off the live
// fields when the reading is stale, unobserved or unjoinable.
func applyCachedMetrics(server *storage.Server, m *metrics.ServerMetrics, now time.Time) {
	if server == nil {
		return
	}
	if m != nil {
		server.MemoryUsage = m.MemoryUsage
		server.CPUPercent = m.CPUPercent
		server.DiskUsage = m.DiskUsage
		server.DiskTotal = m.DiskTotal
		server.WorldSize = m.WorldSize
		server.TPS = m.TPS
	}

	reading := classifyServerJoin(server, m, now)
	server.JoinObservationKind = string(reading.Kind)
	server.JoinObservedAt = reading.JoinObservedAt
	server.LastGoodJoinAt = reading.LastGoodAt

	if reading.Kind != health.ReadingPlayable || m == nil {
		clearLiveJoinFields(server)
		return
	}

	server.PlayersOnline = m.PlayersOnline
	server.SLPAvailable = m.SLPAvailable
	server.SLPLatencyMs = m.SLPLatencyMs
	server.MOTD = m.MOTD
	server.ServerVersion = m.ServerVersion
	server.ProtocolVersion = m.ProtocolVersion
	server.PlayerSample = append([]string(nil), m.PlayerSample...)
	server.MaxPlayersSLP = m.MaxPlayers
	server.Favicon = m.Favicon
}

func clearLiveJoinFields(server *storage.Server) {
	server.PlayersOnline = 0
	server.SLPAvailable = false
	server.SLPLatencyMs = 0
	server.MOTD = ""
	server.ServerVersion = ""
	server.ProtocolVersion = 0
	server.PlayerSample = nil
	server.MaxPlayersSLP = 0
	server.Favicon = ""
}

func livePlayerRoster(server *storage.Server, m *metrics.ServerMetrics, now time.Time) (players []string, online int) {
	if classifyServerJoin(server, m, now).Kind != health.ReadingPlayable || m == nil {
		return []string{}, 0
	}
	return append([]string(nil), m.PlayerSample...), m.PlayersOnline
}
