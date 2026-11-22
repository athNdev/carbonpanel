// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	storage "github.com/athNdev/carbon-panel/internal/db"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// seedAccessState records an observed allowlist/access snapshot for one
// identity on one server, the way ingestion adapters do.
func seedAccessState(t *testing.T, store interface {
	RecordPlayerAccess(context.Context, storage.PlayerAccessState) error
}, identityID, serverID string, whitelisted bool, observedAt time.Time) {
	t.Helper()
	if err := store.RecordPlayerAccess(context.Background(), storage.PlayerAccessState{
		IdentityID:  identityID,
		ServerID:    serverID,
		Whitelisted: whitelisted,
		Source:      "rcon",
		Confidence:  "detected",
		ObservedAt:  observedAt,
	}); err != nil {
		t.Fatalf("seed access state: %v", err)
	}
}

// TestPresenceForManualAllowlistIdentityHasNoTimestamp proves the core honesty
// rule: a manually registered identity with no session and no observation
// reports OFFLINE with a nil last-seen — never a plausible fake timestamp.
func TestPresenceForManualAllowlistIdentityHasNoTimestamp(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	identity, err := store.EnsureOfflinePlayerIdentity(context.Background(), "ManualOnly", "srv-1", storage.SourceManualAllowlist)
	if err != nil {
		t.Fatalf("manual allowlist add: %v", err)
	}

	presence, obs, lastSeen := svc.presenceFor(context.Background(), identity, "srv-1")
	if presence != v1.PlayerPresence_PLAYER_PRESENCE_OFFLINE {
		t.Fatalf("manual-only presence = %v, want OFFLINE", presence)
	}
	if lastSeen != nil {
		t.Fatalf("manual-only identity reported last seen %v, want nil", lastSeen)
	}
	if obs == nil {
		t.Fatal("presence observation missing")
	}
	if obs.ObservedAt != nil {
		t.Fatalf("observation carries a fabricated timestamp: %v", obs.ObservedAt)
	}
	if !strings.Contains(strings.ToLower(obs.ConflictNote), "never observed") {
		t.Fatalf("observation does not disclose the never-observed state: %q", obs.ConflictNote)
	}
}

// TestPresenceForManualIdentityPromotesOnFirstObservation proves a registered
// identity becomes an observed one honestly: after a real session the presence
// path uses that evidence.
func TestPresenceForManualIdentityPromotesOnFirstObservation(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	identity, err := store.EnsureOfflinePlayerIdentity(context.Background(), "LateJoiner", "srv-1", storage.SourceManualAllowlist)
	if err != nil {
		t.Fatalf("manual allowlist add: %v", err)
	}
	if _, _, lastSeen := svc.presenceFor(context.Background(), identity, "srv-1"); lastSeen != nil {
		t.Fatalf("pre-observation last seen = %v, want nil", lastSeen)
	}

	// The player finally joins: ingestion records the session.
	if _, err := store.StartPlayerSession(context.Background(), identity.ID, "srv-1", "rcon", time.Now().UTC()); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	presence, _, lastSeen := svc.presenceFor(context.Background(), identity, "srv-1")
	if presence != v1.PlayerPresence_PLAYER_PRESENCE_RECENTLY_SEEN {
		t.Fatalf("session-backed presence = %v, want RECENTLY_SEEN", presence)
	}
	if lastSeen == nil {
		t.Fatal("session-backed identity must carry a real observation timestamp")
	}
}

// TestListPlayersManualIdentityReportsNeverObservedRow proves the roster row
// for a manual-only identity has no LastSeen, and that the server fallback to
// the observed access state fills in the server the player is registered on.
func TestListPlayersManualIdentityReportsNeverObservedRow(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	identity, err := store.EnsureOfflinePlayerIdentity(context.Background(), "ManualOnly", "srv-1", storage.SourceManualAllowlist)
	if err != nil {
		t.Fatalf("manual allowlist add: %v", err)
	}
	seedAccessState(t, store, identity.ID, "srv-1", true, time.Now().UTC())

	res, err := svc.ListPlayers(context.Background(), connect.NewRequest(&v1.ListPlayersRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var row *v1.PlayerSummary
	for _, player := range res.Msg.Players {
		if player.Ref.GetIdentityKey() == identity.ID {
			row = player
		}
	}
	if row == nil {
		t.Fatalf("manual identity missing from roster: %+v", res.Msg.Players)
	}
	if row.LastSeen != nil {
		t.Fatalf("roster reported last seen %v for a never-observed player, want nil", row.LastSeen)
	}
	if row.Presence != v1.PlayerPresence_PLAYER_PRESENCE_OFFLINE {
		t.Fatalf("roster presence = %v, want OFFLINE", row.Presence)
	}
	if row.ServerId != "srv-1" {
		t.Fatalf("server fallback failed: serverId = %q, want srv-1", row.ServerId)
	}
	if !row.Whitelisted {
		t.Fatal("access state from the fallback server was not applied")
	}
	if row.SkinAvailable {
		t.Fatal("offline identity without a usercache mapping must not claim skin availability")
	}
}

// TestListPlayersFallsBackToMostRecentAccessServer proves the fallback picks
// the most recently observed access entry when an identity has no sessions.
func TestListPlayersFallsBackToMostRecentAccessServer(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	if err := store.DB().Create(&storage.Server{ID: "srv-2", Name: "Creative", NodeID: "default"}).Error; err != nil {
		t.Fatalf("create second server: %v", err)
	}
	identity, err := store.EnsureOfflinePlayerIdentity(context.Background(), "Wandering", "srv-1", storage.SourceManualAllowlist)
	if err != nil {
		t.Fatalf("manual allowlist add: %v", err)
	}
	seedAccessState(t, store, identity.ID, "srv-1", true, time.Now().Add(-time.Hour))
	seedAccessState(t, store, identity.ID, "srv-2", true, time.Now().UTC())

	res, err := svc.ListPlayers(context.Background(), connect.NewRequest(&v1.ListPlayersRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, player := range res.Msg.Players {
		if player.Ref.GetIdentityKey() == identity.ID && player.ServerId != "srv-2" {
			t.Fatalf("fallback server = %q, want the most recent srv-2", player.ServerId)
		}
	}
}

// TestActionContextRefusesEmptyServerWithArgumentError proves moderation on a
// player with no server association is refused as an actionable invalid
// argument instead of a misleading "server not found".
func TestActionContextRefusesEmptyServerWithArgumentError(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	identity, err := store.EnsureOfflinePlayerIdentity(context.Background(), "NoServer", "srv-1", storage.SourceManualAllowlist)
	if err != nil {
		t.Fatalf("manual allowlist add: %v", err)
	}

	_, _, _, _, err = svc.actionContext(context.Background(), "", identity.ID, "")
	if err == nil {
		t.Fatal("empty server id was accepted")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("error code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "not associated with a server") {
		t.Fatalf("error is not actionable: %v", err)
	}
}

// TestListPlayersLastSeenSortKeepsNeverObservedLast proves the backend sort
// never floats an unobserved identity above observed ones, in either
// direction.
func TestListPlayersLastSeenSortKeepsNeverObservedLast(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	manual, err := store.EnsureOfflinePlayerIdentity(context.Background(), "ZManual", "srv-1", storage.SourceManualAllowlist)
	if err != nil {
		t.Fatalf("manual allowlist add: %v", err)
	}
	seedAccessState(t, store, manual.ID, "srv-1", true, time.Now().UTC())
	observed := seedIdentity(t, store, "AObserved", "44444444-4444-4444-4444-444444444444", "srv-1", time.Now().Add(-2*time.Hour))
	seedAccessState(t, store, observed.ID, "srv-1", false, time.Now().UTC())

	for _, descending := range []bool{false, true} {
		res, err := svc.ListPlayers(context.Background(), connect.NewRequest(&v1.ListPlayersRequest{
			SortBy: "last_seen", SortDescending: descending,
		}))
		if err != nil {
			t.Fatalf("list (desc=%v): %v", descending, err)
		}
		last := res.Msg.Players[len(res.Msg.Players)-1]
		if last.Ref.GetIdentityKey() != manual.ID {
			t.Fatalf("desc=%v: never-observed identity did not sort last: last = %s", descending, last.Ref.GetName())
		}
	}
}
