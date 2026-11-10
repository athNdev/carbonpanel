// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/health"
	"github.com/athNdev/carbon-panel/internal/metrics"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func TestGetServer_StaleSLPIsNotCurrentPlayable(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	lastGood := now.Add(-6 * time.Hour)
	server := &storage.Server{
		ID: "stale-slp", Name: "Secret World", NodeID: "test-node",
		Port: 25565, Memory: 1024, Status: storage.StatusRunning,
		DataPath: t.TempDir(), UpdatedAt: now,
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	collector := metrics.NewCollectorWithPool(nil, nil, nil, nil, nil, nil, logger.New())
	collector.SeedMetrics(metrics.ServerMetrics{
		ServerID:        server.ID,
		PlayersOnline:   3,
		SLPAvailable:    true,
		SLPLatencyMs:    12,
		MOTD:            "private motd with player data",
		ServerVersion:   "1.21.1",
		ProtocolVersion: 767,
		PlayerSample:    []string{"Notch", "jeb_"},
		MaxPlayers:      20,
		Favicon:         "data:image/png;base64,secret",
		SLPLastUpdated:  lastGood,
		LastUpdated:     lastGood,
	})

	svc := &ServerService{store: store, log: logger.New(), metricsCollector: collector}
	resp, err := svc.GetServer(ctx, connect.NewRequest(&v1.GetServerRequest{Id: server.ID}))
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	got := resp.Msg.Server
	if got.Status != v1.ServerStatus_SERVER_STATUS_RUNNING {
		t.Fatalf("status = %v, want running (container status stays distinct from join freshness)", got.Status)
	}
	if got.JoinObservationKind != string(health.ReadingUnobserved) {
		t.Fatalf("join kind = %q, want unobserved so last-good SLP is not current playable", got.JoinObservationKind)
	}
	if got.SlpAvailable {
		t.Fatal("stale SLP copied into slp_available; last-good join must not look live")
	}
	if got.PlayersOnline != 0 {
		t.Fatalf("players_online = %d, want 0 for a stale observation", got.PlayersOnline)
	}
	if len(got.PlayerSample) != 0 {
		t.Fatalf("player_sample = %v, want empty (do not leak last-good names as current)", got.PlayerSample)
	}
	if got.Motd != "" {
		t.Fatalf("motd = %q, want empty on a stale observation", got.Motd)
	}
	if got.Favicon != "" {
		t.Fatalf("favicon leaked on a stale observation")
	}
	if got.LastGoodJoinAt == nil || got.LastGoodJoinAt.AsTime().UTC().Truncate(time.Second) != lastGood {
		t.Fatalf("last_good_join_at = %v, want %s", got.LastGoodJoinAt, lastGood)
	}
}

func TestGetServer_FreshSLPStaysPlayableWithLiveSample(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	server := &storage.Server{
		ID: "fresh-slp", Name: "Live World", NodeID: "test-node",
		Port: 25566, Memory: 1024, Status: storage.StatusRunning,
		DataPath: t.TempDir(), UpdatedAt: now,
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	collector := metrics.NewCollectorWithPool(nil, nil, nil, nil, nil, nil, logger.New())
	collector.SeedMetrics(metrics.ServerMetrics{
		ServerID:       server.ID,
		PlayersOnline:  1,
		SLPAvailable:   true,
		MOTD:           "welcome",
		PlayerSample:   []string{"Steve"},
		SLPLastUpdated: now,
		LastUpdated:    now,
	})

	svc := &ServerService{store: store, log: logger.New(), metricsCollector: collector}
	resp, err := svc.GetServer(ctx, connect.NewRequest(&v1.GetServerRequest{Id: server.ID}))
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	got := resp.Msg.Server
	if got.JoinObservationKind != string(health.ReadingPlayable) {
		t.Fatalf("join kind = %q, want playable", got.JoinObservationKind)
	}
	if !got.SlpAvailable {
		t.Fatal("fresh successful SLP must remain slp_available")
	}
	if got.PlayersOnline != 1 || len(got.PlayerSample) != 1 || got.PlayerSample[0] != "Steve" {
		t.Fatalf("fresh roster = %d %v, want Steve", got.PlayersOnline, got.PlayerSample)
	}
	if got.Motd != "welcome" {
		t.Fatalf("motd = %q, want the live MOTD", got.Motd)
	}
}

func TestGetServer_FreshFailedSLPIsUnjoinableNotSuccess(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	server := &storage.Server{
		ID: "failed-slp", Name: "Closed World", NodeID: "test-node",
		Port: 25567, Memory: 1024, Status: storage.StatusRunning,
		DataPath: t.TempDir(), UpdatedAt: now,
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	collector := metrics.NewCollectorWithPool(nil, nil, nil, nil, nil, nil, logger.New())
	collector.SeedMetrics(metrics.ServerMetrics{
		ServerID:       server.ID,
		PlayersOnline:  2,
		SLPAvailable:   false,
		MOTD:           "stale motd",
		PlayerSample:   []string{"Alex"},
		SLPLastUpdated: now,
		LastUpdated:    now,
	})

	svc := &ServerService{store: store, log: logger.New(), metricsCollector: collector}
	resp, err := svc.GetServer(ctx, connect.NewRequest(&v1.GetServerRequest{Id: server.ID}))
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	got := resp.Msg.Server
	if got.JoinObservationKind != string(health.ReadingUnjoinable) {
		t.Fatalf("join kind = %q, want unjoinable", got.JoinObservationKind)
	}
	if got.SlpAvailable {
		t.Fatal("failed SLP must not report slp_available")
	}
	if len(got.PlayerSample) != 0 || got.Motd != "" {
		t.Fatalf("failed join leaked last-good sample/motd: %v %q", got.PlayerSample, got.Motd)
	}
	if got.Status != v1.ServerStatus_SERVER_STATUS_RUNNING {
		t.Fatalf("status = %v, want running (unjoinable is not stopped)", got.Status)
	}
}

func TestListServerPlayers_OmitsStaleSample(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	server := &storage.Server{
		ID: "players-stale", Name: "Roster", NodeID: "test-node",
		Port: 25568, Memory: 1024, Status: storage.StatusRunning,
		DataPath: t.TempDir(), UpdatedAt: now,
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	collector := metrics.NewCollectorWithPool(nil, nil, nil, nil, nil, nil, logger.New())
	collector.SeedMetrics(metrics.ServerMetrics{
		ServerID:       server.ID,
		PlayersOnline:  2,
		SLPAvailable:   true,
		PlayerSample:   []string{"Notch"},
		SLPLastUpdated: now.Add(-time.Hour),
	})

	svc := &ServerService{store: store, log: logger.New(), metricsCollector: collector}
	resp, err := svc.ListServerPlayers(ctx, connect.NewRequest(&v1.ListServerPlayersRequest{ServerId: server.ID}))
	if err != nil {
		t.Fatalf("ListServerPlayers: %v", err)
	}
	if resp.Msg.OnlineCount != 0 || len(resp.Msg.Players) != 0 {
		t.Fatalf("stale roster returned as current: count=%d players=%v", resp.Msg.OnlineCount, resp.Msg.Players)
	}
}

func TestGetServer_StaleStoredStatusIsNotRunningSuccess(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	lastGood := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)
	server := &storage.Server{
		ID: "stale-status", Name: "Aged World", NodeID: "test-node",
		Port: 25569, Memory: 1024, Status: storage.StatusRunning,
		DataPath: t.TempDir(), UpdatedAt: lastGood,
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	if err := store.DB().Model(&storage.Server{}).Where("id = ?", server.ID).Update("updated_at", lastGood).Error; err != nil {
		t.Fatalf("backdate server: %v", err)
	}

	collector := metrics.NewCollectorWithPool(nil, nil, nil, nil, nil, nil, logger.New())
	collector.SeedMetrics(metrics.ServerMetrics{
		ServerID:       server.ID,
		PlayersOnline:  4,
		SLPAvailable:   true,
		PlayerSample:   []string{"Notch"},
		SLPLastUpdated: lastGood,
	})

	svc := &ServerService{store: store, log: logger.New(), metricsCollector: collector}
	resp, err := svc.GetServer(ctx, connect.NewRequest(&v1.GetServerRequest{Id: server.ID}))
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	got := resp.Msg.Server
	if got.JoinObservationKind != string(health.ReadingStale) {
		t.Fatalf("join kind = %q, want stale", got.JoinObservationKind)
	}
	if got.SlpAvailable || got.PlayersOnline != 0 || len(got.PlayerSample) != 0 {
		t.Fatalf("stale status still presented as live join: available=%v players=%d sample=%v", got.SlpAvailable, got.PlayersOnline, got.PlayerSample)
	}
}
