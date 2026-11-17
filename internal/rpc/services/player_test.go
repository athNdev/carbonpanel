// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// fakes implement the local ports so service seams can be tested without a
// live Minecraft server, Docker daemon or third-party skin host.
type fakeSource struct {
	name       string
	kicks      int
	bans       int
	pardons    int
	ops        int
	allowlist  int
	failNext   error
	lastReason string
}

func (f *fakeSource) Source() string { return f.name }
func (f *fakeSource) List(context.Context) ([]observedPlayer, error) {
	return []observedPlayer{{Name: "Steve", UUID: "11111111-1111-1111-1111-111111111111"}}, nil
}
func (f *fakeSource) fail() error {
	err := f.failNext
	f.failNext = nil
	return err
}
func (f *fakeSource) Kick(_ context.Context, _, reason string) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.kicks++
	f.lastReason = reason
	return nil
}
func (f *fakeSource) Ban(_ context.Context, _, reason string) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.bans++
	f.lastReason = reason
	return nil
}
func (f *fakeSource) Pardon(context.Context, string) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.pardons++
	return nil
}
func (f *fakeSource) SetOperator(context.Context, string, int32, bool) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.ops++
	return nil
}
func (f *fakeSource) SetWhitelist(context.Context, string, bool) error {
	if err := f.fail(); err != nil {
		return err
	}
	f.allowlist++
	return nil
}

type fakeNegotiator struct {
	source  playerSource
	caps    capabilityInfo
	err     error
	called  int
}

func (f *fakeNegotiator) Negotiate(context.Context, *storage.Server, *storage.ServerConfig) (playerSource, capabilityInfo, error) {
	f.called++
	return f.source, f.caps, f.err
}

type fakeSkin struct{ refreshed int }

func (f *fakeSkin) Get(context.Context, string, string) (skinAsset, error) {
	return skinAsset{Data: []byte("png"), ContentType: "image/png", Source: "identity_service", Confidence: "detected", ObservedAt: time.Now().UTC()}, nil
}
func (f *fakeSkin) Refresh(context.Context, string, string) (skinAsset, error) {
	f.refreshed++
	return skinAsset{Data: []byte("png"), ContentType: "image/png", Source: "identity_service", Confidence: "detected"}, nil
}
func (f *fakeSkin) Close() {}

type fakeDeep struct {
	err error
}

func (f *fakeDeep) Read(context.Context, *storage.Server, string, string) (deepResult, error) {
	if f.err != nil {
		return deepResult{}, f.err
	}
	return deepResult{
		Fields: []deepField{{
			Name: "play_time", Value: "12h", Unit: "", Source: "on_disk_json",
			Confidence: "detected", ObservedAt: time.Now().UTC(),
		}},
		SnapshotNote: "server stopped — safe snapshot",
	}, nil
}

func newPlayerTestService(t *testing.T) (*PlayerService, *storage.Store, *fakeSource, *fakeNegotiator) {
	t.Helper()
	store, err := storage.NewSQLiteStore(&config.Config{
		Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "players.db"), AutoMigrate: true},
		Docker:   config.DockerConfig{Host: "unix:///var/run/docker.sock"},
	})
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{
		ID: "srv-1", Name: "Survival", NodeID: "default", DataPath: filepath.Join(t.TempDir(), "data"),
		ContainerID: "container-1",
	}
	if err := store.DB().Create(server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	source := &fakeSource{name: "rcon"}
	negotiator := &fakeNegotiator{source: source, caps: capabilityInfo{RCON: true, ChosenSource: "rcon", Detail: "RCON available"}}
	svc := NewPlayerService(store, logger.New(), nil)
	svc.SetSourceNegotiator(negotiator)
	svc.SetSkinProvider(&fakeSkin{})
	svc.SetDeepDataReader(&fakeDeep{})
	return svc, store, source, negotiator
}

func seedIdentity(t *testing.T, store *storage.Store, name, uuid, serverID string, lastSeen time.Time) *storage.PlayerIdentity {
	t.Helper()
	identity, err := store.EnsurePlayerIdentity(context.Background(), uuid, name, "", "", false)
	if err != nil {
		t.Fatalf("seed identity %s: %v", name, err)
	}
	seen := lastSeen
	if seen.IsZero() {
		seen = time.Time{}
	}
	if err := store.DB().Model(&storage.PlayerIdentity{}).Where("id = ?", identity.ID).
		Update("last_seen_at", seen).Error; err != nil {
		t.Fatalf("set last seen: %v", err)
	}
	if _, err := store.StartPlayerSession(context.Background(), identity.ID, serverID, "poll", lastSeen); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return identity
}

func TestListPlayersSearchFilterAndSortOutOfOrder(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	older := seedIdentity(t, store, "Zed", "22222222-2222-2222-2222-222222222222", "srv-1", time.Now().Add(-2*time.Hour))
	newer := seedIdentity(t, store, "Alice", "33333333-3333-3333-3333-333333333333", "srv-1", time.Now().Add(-time.Minute))
	_ = older
	_ = newer

	res, err := svc.ListPlayers(context.Background(), connect.NewRequest(&v1.ListPlayersRequest{
		ServerId: "srv-1", SortBy: "name", Limit: 10,
	}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	players := res.Msg.Players
	if len(players) != 2 {
		t.Fatalf("want 2 players, got %d", len(players))
	}
	if players[0].Ref.Name != "Alice" {
		t.Fatalf("name sort out of order: first = %q", players[0].Ref.Name)
	}
	if players[0].ServerName != "Survival" {
		t.Fatalf("server name not resolved: %q", players[0].ServerName)
	}

	desc, err := svc.ListPlayers(context.Background(), connect.NewRequest(&v1.ListPlayersRequest{
		ServerId: "srv-1", SortBy: "last_seen", SortDescending: true, Limit: 10,
	}))
	if err != nil {
		t.Fatalf("list descending: %v", err)
	}
	if desc.Msg.Players[0].Ref.Name != "Alice" {
		t.Fatalf("last_seen sort wrong: first = %q", desc.Msg.Players[0].Ref.Name)
	}

	filtered, err := svc.ListPlayers(context.Background(), connect.NewRequest(&v1.ListPlayersRequest{
		ServerId: "srv-1", Query: "ali", Limit: 10,
	}))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(filtered.Msg.Players) != 1 || filtered.Msg.Players[0].Ref.Name != "Alice" {
		t.Fatalf("search returned %+v", filtered.Msg.Players)
	}
}

func TestListPlayersDoesNotFabricatePresence(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	seedIdentity(t, store, "NeverSeen", "44444444-4444-4444-4444-444444444444", "srv-1", time.Time{})
	res, err := svc.ListPlayers(context.Background(), connect.NewRequest(&v1.ListPlayersRequest{ServerId: "srv-1"}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Msg.Players) != 1 {
		t.Fatalf("want one row, got %d", len(res.Msg.Players))
	}
	row := res.Msg.Players[0]
	if row.Presence != v1.PlayerPresence_PLAYER_PRESENCE_UNOBSERVED {
		t.Fatalf("unobserved player classified as %v", row.Presence)
	}
	if row.PresenceObservation.ConflictNote == "" {
		t.Fatal("missing honest unknown note")
	}
}

func TestCapabilitiesDiscloseDecisionPendingStates(t *testing.T) {
	svc, _, _, _ := newPlayerTestService(t)
	res, err := svc.ListPlayerCapabilities(context.Background(), connect.NewRequest(&v1.ListPlayerCapabilitiesRequest{ServerId: "srv-1"}))
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	pending := 0
	for _, cap := range res.Msg.Capabilities {
		if cap.State == v1.PlayerSupportState_PLAYER_SUPPORT_STATE_DECISION_PENDING {
			pending++
			if strings.TrimSpace(cap.Detail) == "" {
				t.Fatalf("decision-pending capability %v has no actionable detail", cap.Capability)
			}
		}
	}
	if pending < 5 {
		t.Fatalf("want bridge/Bedrock gates disclosed, got %d pending", pending)
	}
}

func TestKickIsAuditedAndIdempotent(t *testing.T) {
	svc, store, source, _ := newPlayerTestService(t)
	identity := seedIdentity(t, store, "RuleBreaker", "55555555-5555-5555-5555-555555555555", "srv-1", time.Now())
	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "actor-1", Username: "mod"})
	req := connect.NewRequest(&v1.KickPlayerRequest{
		ServerId: "srv-1", IdentityKey: identity.ID, Reason: "spam", RequestId: "kick-req-1",
	})
	if _, err := svc.KickPlayer(ctx, req); err != nil {
		t.Fatalf("kick: %v", err)
	}
	if source.kicks != 1 || source.lastReason != "spam" {
		t.Fatalf("kick not applied exactly once: %+v", source)
	}
	// A retry with the same request id must not kick twice.
	if _, err := svc.KickPlayer(ctx, req); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if source.kicks != 1 {
		t.Fatalf("duplicate kick applied: %d", source.kicks)
	}
	var rows []storage.ActivityLog
	if err := store.DB().Where("event = ? AND request_id = ?", activity.EventPlayerKick, "kick-req-1").Find(&rows).Error; err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want one audit row, got %d", len(rows))
	}
	if rows[0].SubjectTyp != "player" || rows[0].SubjectID != identity.ID || rows[0].ActorName != "mod" {
		t.Fatalf("audit row lacks actor/subject: %+v", rows[0])
	}
}

func TestAdapterFailureDoesNotReportSuccess(t *testing.T) {
	svc, store, source, _ := newPlayerTestService(t)
	identity := seedIdentity(t, store, "FailTarget", "66666666-6666-6666-6666-666666666666", "srv-1", time.Now())
	source.failNext = errors.New("rcon unreachable")
	_, err := svc.BanPlayer(context.Background(), connect.NewRequest(&v1.BanPlayerRequest{
		ServerId: "srv-1", IdentityKey: identity.ID, Reason: "abuse", RequestId: "ban-fail-1",
	}))
	if err == nil {
		t.Fatal("adapter failure reported success")
	}
	var row storage.PlayerModerationAction
	if err := store.DB().Where("request_id = ?", "ban-fail-1").First(&row).Error; err != nil {
		t.Fatalf("journal row missing: %v", err)
	}
	if row.Phase != storage.PlayerActionFailed {
		t.Fatalf("journal phase = %q, want failed", row.Phase)
	}
}

func TestNoSelfElevation(t *testing.T) {
	svc, _, _, _ := newPlayerTestService(t)
	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "actor-1", Username: "alice"})
	_, err := svc.SetOperator(ctx, connect.NewRequest(&v1.SetOperatorRequest{
		ServerId: "srv-1", PlayerName: "alice", RequestId: "op-self-1",
	}))
	if err == nil || connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("self-elevation not denied: %v", err)
	}
}

func TestKickHasNoUndoToken(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	identity := seedIdentity(t, store, "NoUndo", "77777777-7777-7777-7777-777777777777", "srv-1", time.Now())
	res, err := svc.KickPlayer(context.Background(), connect.NewRequest(&v1.KickPlayerRequest{
		ServerId: "srv-1", IdentityKey: identity.ID, RequestId: "kick-noundo-1",
	}))
	if err != nil {
		t.Fatalf("kick: %v", err)
	}
	result := res.Msg.Outcome.Results[0]
	if result.UndoSupported || result.UndoToken != "" {
		t.Fatalf("kick exposed undo: %+v", result)
	}
	_, err = svc.UndoPlayerAction(context.Background(), connect.NewRequest(&v1.UndoPlayerActionRequest{
		ServerId: "srv-1", UndoToken: result.UndoToken, RequestId: "undo-1",
	}))
	if err == nil {
		t.Fatal("undo with empty token unexpectedly succeeded")
	}
}

func TestBulkPartialResultsArePerTarget(t *testing.T) {
	svc, store, source, _ := newPlayerTestService(t)
	names := []string{"BulkA", "BulkB", "BulkC"}
	keys := make([]string, 0, len(names))
	for i, name := range names {
		uuid := fmt.Sprintf("88888888-8888-8888-8888-88888888888%d", i)
		keys = append(keys, seedIdentity(t, store, name, uuid, "srv-1", time.Now()).ID)
	}
	source.failNext = errors.New("one target disconnected")
	res, err := svc.BulkPlayerAction(context.Background(), connect.NewRequest(&v1.BulkPlayerActionRequest{
		ServerId: "srv-1", Kind: "kick", IdentityKeys: keys, RequestId: "bulk-1",
	}))
	if err != nil {
		t.Fatalf("bulk: %v", err)
	}
	if len(res.Msg.Outcome.Results) != len(keys) {
		t.Fatalf("want %d results, got %d", len(keys), len(res.Msg.Outcome.Results))
	}
	successes, failures := 0, 0
	for _, r := range res.Msg.Outcome.Results {
		if r.Success {
			successes++
		} else {
			failures++
		}
	}
	if successes != 2 || failures != 1 {
		t.Fatalf("partial results = %d success / %d failure", successes, failures)
	}
}

func TestDeepDataIsBoundedAndProvenanced(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	identity := seedIdentity(t, store, "DeepReader", "99999999-9999-9999-9999-999999999999", "srv-1", time.Now())
	res, err := svc.GetPlayerDeepData(context.Background(), connect.NewRequest(&v1.GetPlayerDeepDataRequest{
		ServerId: "srv-1", IdentityKey: identity.ID, Kind: "stats",
	}))
	if err != nil {
		t.Fatalf("deep data: %v", err)
	}
	if len(res.Msg.Fields) != 1 || res.Msg.Fields[0].Observation.Source != v1.PlayerDataSource_PLAYER_DATA_SOURCE_ON_DISK_JSON {
		t.Fatalf("deep field lacks provenance: %+v", res.Msg.Fields)
	}
	if res.Msg.SnapshotNote == "" {
		t.Fatal("snapshot note missing")
	}
}

func TestSkinProxyRejectsArbitraryKind(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	identity := seedIdentity(t, store, "SkinUser", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "srv-1", time.Now())
	_, err := svc.GetPlayerSkin(context.Background(), connect.NewRequest(&v1.GetPlayerSkinRequest{
		IdentityKey: identity.ID, Kind: "https://evil.example/x",
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("arbitrary skin kind not rejected: %v", err)
	}
}
