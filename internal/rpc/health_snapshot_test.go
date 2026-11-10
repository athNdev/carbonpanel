// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"context"
	"net/http"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/health"
	"github.com/athNdev/carbon-panel/internal/metrics"
)

func TestHealthSnapshotDoesNotTreatStoredRunningAsPlayable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateServer(ctx, &storage.Server{
		ID: "s1", Name: "alpha", Status: storage.StatusRunning, DataPath: t.TempDir(),
	}); err != nil {
		t.Fatalf("create server: %v", err)
	}

	s := &Server{store: store, startedAt: time.Now().Add(-time.Minute)}
	snap := s.healthSnapshot(ctx)

	if snap.HTTPStatus() != http.StatusOK && snap.HTTPStatus() != http.StatusServiceUnavailable {
		t.Fatalf("unexpected HTTP status %d", snap.HTTPStatus())
	}
	if snap.Servers.Playable != 0 {
		t.Errorf("playable = %d, want 0 without SLP/RCON", snap.Servers.Playable)
	}
	if snap.Servers.Unobserved != 1 {
		t.Errorf("unobserved = %d, want 1", snap.Servers.Unobserved)
	}
	if snap.Servers.Stopped != 0 {
		t.Errorf("stopped = %d, want 0: unknown must not become stopped", snap.Servers.Stopped)
	}
	if c, _ := snap.Component(health.LayerGame); c.State != health.StateUnknown {
		t.Errorf("game = %s, want unknown", c.State)
	}
}

func TestHealthSnapshotUsesCollectedSLPForPlayable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateServer(ctx, &storage.Server{
		ID: "s1", Name: "alpha", Status: storage.StatusRunning, DataPath: t.TempDir(),
	}); err != nil {
		t.Fatalf("create server: %v", err)
	}

	collector := metrics.NewCollector(store, nil, nil, nil, nil, nil)
	collector.SeedMetrics(metrics.ServerMetrics{
		ServerID:       "s1",
		SLPAvailable:   true,
		SLPLastUpdated: time.Now(),
	})

	s := &Server{store: store, startedAt: time.Now().Add(-time.Minute), metricsCollector: collector}
	snap := s.healthSnapshot(ctx)
	if snap.Servers.Playable != 1 {
		t.Fatalf("playable = %d, want 1 with fresh SLP", snap.Servers.Playable)
	}
}

func TestHealthSnapshotStartingPastDeadlineIsNotStopped(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	started := time.Now().Add(-10 * time.Minute)
	if err := store.CreateServer(ctx, &storage.Server{
		ID: "s1", Name: "alpha", Status: storage.StatusStarting, DataPath: t.TempDir(), LastStarted: &started,
	}); err != nil {
		t.Fatalf("create server: %v", err)
	}

	s := &Server{store: store, startedAt: time.Now().Add(-time.Hour)}
	snap := s.healthSnapshot(ctx)
	if snap.Servers.TimedOut != 1 {
		t.Fatalf("timedOut = %d, want 1: %+v", snap.Servers.TimedOut, snap.Servers)
	}
	if snap.Servers.Stopped != 0 || snap.Servers.Playable != 0 {
		t.Fatalf("servers = %+v, want timed out not stopped/playable", snap.Servers)
	}
	if snap.HTTPStatus() != http.StatusOK {
		t.Errorf("HTTP = %d, want 200", snap.HTTPStatus())
	}
}

func TestJoinProbePrefersSLPThenRCON(t *testing.T) {
	if p := joinProbe(nil, "s1"); !p.ObservedAt.IsZero() {
		t.Fatalf("nil collector = %+v, want empty", p)
	}

	c := metrics.NewCollector(nil, nil, nil, nil, nil, nil)
	c.SeedMetrics(metrics.ServerMetrics{
		ServerID:        "s1",
		RCONLastUpdated: time.Now(),
		SLPLastUpdated:  time.Now(),
		SLPAvailable:    false,
	})
	p := joinProbe(c, "s1")
	if p.Source != health.SourceSLP || p.OK {
		t.Fatalf("probe = %+v, want failed SLP over RCON", p)
	}
}
