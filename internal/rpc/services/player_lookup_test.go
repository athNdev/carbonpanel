// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/activity"
	storage "github.com/athNdev/carbon-panel/internal/db"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

const (
	lookupUUID  = "11111111-2222-3333-4444-555555555555"
	lookupUUID2 = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

func withLookup(svc *PlayerService, fn func(string) identityLookup) {
	svc.SetSkinProvider(&fakeSkin{lookup: fn})
}

func foundLookup(query string) identityLookup {
	return identityLookup{Status: "found", UUID: lookupUUID, Name: "NewPlayer", Source: "identity_service"}
}

func countIdentities(t *testing.T, store *storage.Store) int64 {
	t.Helper()
	var n int64
	if err := store.DB().Model(&storage.PlayerIdentity{}).Count(&n).Error; err != nil {
		t.Fatalf("count identities: %v", err)
	}
	return n
}

func countAudit(t *testing.T, store *storage.Store, event string) int64 {
	t.Helper()
	var n int64
	if err := store.DB().Model(&storage.ActivityLog{}).Where("event = ?", event).Count(&n).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	return n
}

func TestLookupPlayerByExactUUIDIsEphemeralAndDoesNotGrowOnRepeat(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	withLookup(svc, foundLookup)
	beforeIdentities, beforeAudit := countIdentities(t, store), countAudit(t, store, "player.lookup")

	for i := 0; i < 3; i++ {
		res, err := svc.LookupPlayer(context.Background(), connect.NewRequest(&v1.LookupPlayerRequest{
			Query: "11111111222233334444555555555555", // undashed exact UUID
		}))
		if err != nil {
			t.Fatalf("lookup %d: %v", i, err)
		}
		if res.Msg.Status != v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_FOUND {
			t.Fatalf("status = %v, want FOUND", res.Msg.Status)
		}
		if res.Msg.Name != "NewPlayer" || res.Msg.Uuid != lookupUUID {
			t.Fatalf("ephemeral result = %+v", res.Msg)
		}
		if res.Msg.Observation == nil || res.Msg.Observation.Source != v1.PlayerDataSource_PLAYER_DATA_SOURCE_IDENTITY_SERVICE {
			t.Fatalf("observation = %+v", res.Msg.Observation)
		}
		if !strings.Contains(strings.ToLower(res.Msg.Note), "name history") ||
			!strings.Contains(strings.ToLower(res.Msg.Note), "no identity") {
			t.Fatalf("note does not disclose ephemeral/no-name-history: %q", res.Msg.Note)
		}
	}

	if got := countIdentities(t, store); got != beforeIdentities {
		t.Fatalf("repeated lookup grew identity table: %d -> %d", beforeIdentities, got)
	}
	if got := countAudit(t, store, "player.lookup"); got != beforeAudit {
		t.Fatalf("read-only lookup wrote audit rows: %d -> %d", beforeAudit, got)
	}
	var sessions int64
	if err := store.DB().Model(&storage.PlayerSession{}).Count(&sessions).Error; err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("read-only lookup created %d sessions", sessions)
	}
}

func TestLookupPlayerByExactUsernameIsEphemeral(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	withLookup(svc, func(query string) identityLookup {
		if query != "NewPlayer" {
			t.Fatalf("unexpected query forwarded: %q", query)
		}
		return foundLookup(query)
	})
	before := countIdentities(t, store)
	res, err := svc.LookupPlayer(context.Background(), connect.NewRequest(&v1.LookupPlayerRequest{Query: "NewPlayer"}))
	if err != nil || res.Msg.Status != v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_FOUND {
		t.Fatalf("username lookup: res=%+v err=%v", res, err)
	}
	if res.Msg.Name != "NewPlayer" || res.Msg.Uuid != lookupUUID {
		t.Fatalf("result = %+v", res.Msg)
	}
	if got := countIdentities(t, store); got != before {
		t.Fatalf("username lookup persisted an identity: %d -> %d", before, got)
	}
}

func TestLookupPlayerNotFoundNeverInventsIdentity(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	withLookup(svc, func(string) identityLookup {
		return identityLookup{Status: "not_found", Note: "No Mojang profile."}
	})
	before := countIdentities(t, store)
	res, err := svc.LookupPlayer(context.Background(), connect.NewRequest(&v1.LookupPlayerRequest{Query: "Ghost"}))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if res.Msg.Status != v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_NOT_FOUND {
		t.Fatalf("status = %v, want NOT_FOUND", res.Msg.Status)
	}
	if res.Msg.Name != "" || res.Msg.Uuid != "" || res.Msg.Observation != nil {
		t.Fatalf("not-found lookup invented identity: %+v", res.Msg)
	}
	if strings.TrimSpace(res.Msg.Note) == "" {
		t.Fatal("not-found state has no note")
	}
	if got := countIdentities(t, store); got != before {
		t.Fatalf("not-found lookup persisted an identity: %d -> %d", before, got)
	}
}

func TestLookupPlayerAmbiguousOnMultipleLocalMatches(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	withLookup(svc, func(string) identityLookup {
		return identityLookup{Status: "not_found", Note: "No online profile."}
	})
	if _, err := store.EnsureOfflinePlayerIdentity(context.Background(), "SameName", "srv-1", "poll"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureOfflinePlayerIdentity(context.Background(), "SameName", "srv-2", "poll"); err != nil {
		t.Fatal(err)
	}
	res, err := svc.LookupPlayer(context.Background(), connect.NewRequest(&v1.LookupPlayerRequest{Query: "SameName"}))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if res.Msg.Status != v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_AMBIGUOUS || res.Msg.Name != "" {
		t.Fatalf("ambiguous lookup: %+v", res.Msg)
	}
}

func TestLookupProviderOutageIsUnavailableNotAbsent(t *testing.T) {
	svc, _, _, _ := newPlayerTestService(t)
	withLookup(svc, func(string) identityLookup {
		return identityLookup{Status: "unavailable", Note: "Identity service timed out."}
	})
	res, err := svc.LookupPlayer(context.Background(), connect.NewRequest(&v1.LookupPlayerRequest{Query: lookupUUID}))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if res.Msg.Status != v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_UNAVAILABLE || res.Msg.Name != "" {
		t.Fatalf("outage lookup: %+v", res.Msg)
	}
	if !strings.Contains(res.Msg.Note, "timed out") {
		t.Fatalf("note lost provider reason: %q", res.Msg.Note)
	}
}

func TestLookupPlayerInvalidQueryIsAmbiguousWithoutIdentity(t *testing.T) {
	svc, _, _, _ := newPlayerTestService(t)
	res, err := svc.LookupPlayer(context.Background(), connect.NewRequest(&v1.LookupPlayerRequest{Query: "not a name!"}))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if res.Msg.Status != v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_AMBIGUOUS || res.Msg.Name != "" {
		t.Fatalf("invalid query: %+v", res.Msg)
	}
}

func TestLookupAuthoritativeNotFoundWithLocalObservationIsEphemeral(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	seedIdentity(t, store, "Observed", lookupUUID, "srv-1", time.Now())
	before := countIdentities(t, store)
	withLookup(svc, func(string) identityLookup {
		return identityLookup{Status: "not_found", Note: "No online profile."}
	})
	// With an authoritative negative, a single local observation may be shown
	// as ephemeral context, but the lookup stays read-only and reports panel_db
	// provenance without any roster/presence mutation.
	res, err := svc.LookupPlayer(context.Background(), connect.NewRequest(&v1.LookupPlayerRequest{Query: "Observed"}))
	if err != nil || res.Msg.Status != v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_FOUND {
		t.Fatalf("local fallback: res=%+v err=%v", res, err)
	}
	if res.Msg.Observation.Source != v1.PlayerDataSource_PLAYER_DATA_SOURCE_PANEL_DB || res.Msg.Observation.GetObservedAt() != nil {
		t.Fatalf("local fallback provenance or timestamp = %v", res.Msg.Observation)
	}
	if got := countIdentities(t, store); got != before {
		t.Fatalf("local fallback persisted an identity: %d -> %d", before, got)
	}
}

func TestLookupThenAuthorizedWhitelistAddPersistsExactlyOneIdentity(t *testing.T) {
	svc, store, source, _ := newPlayerTestService(t)
	svc.SetEdition("java")
	withLookup(svc, foundLookup)
	before := countIdentities(t, store)

	// Read-only lookup first: no persistence.
	if _, err := svc.LookupPlayer(context.Background(), connect.NewRequest(&v1.LookupPlayerRequest{Query: "FreshName"})); err != nil {
		t.Fatal(err)
	}
	if got := countIdentities(t, store); got != before {
		t.Fatalf("lookup mutated state: %d -> %d", before, got)
	}

	// The separate authorized mutation is the only path that persists identity.
	res, err := svc.SetWhitelistEntry(context.Background(), connect.NewRequest(&v1.SetWhitelistEntryRequest{
		ServerId: "srv-1", PlayerQuery: "FreshName", RequestId: "wl-after-lookup-1",
	}))
	if err != nil {
		t.Fatalf("whitelist add: %v", err)
	}
	if source.allowlist != 1 || source.lastWhitelistName != "NewPlayer" {
		t.Fatalf("adapter not called once with resolved name: %+v", source)
	}
	if got := countIdentities(t, store); got != before+1 {
		t.Fatalf("mutation should create exactly one identity: %d -> %d", before, got)
	}
	var rows []storage.PlayerModerationAction
	if err := store.DB().Where("request_id = ?", "wl-after-lookup-1").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Phase != storage.PlayerActionComplete {
		t.Fatalf("journal rows = %+v", rows)
	}
	var audit []storage.ActivityLog
	if err := store.DB().Where("event = ? AND request_id = ?", activity.EventPlayerWhitelistAdd, "wl-after-lookup-1").Find(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if len(audit) != 1 {
		t.Fatalf("want one whitelist audit row, got %d", len(audit))
	}
	_ = res
}

func TestSetWhitelistManualAddByNameBeforeFirstJoin(t *testing.T) {
	svc, store, source, _ := newPlayerTestService(t)
	svc.SetEdition("java")
	withLookup(svc, func(string) identityLookup {
		return identityLookup{Status: "not_found", Note: "no online profile yet"}
	})
	res, err := svc.SetWhitelistEntry(context.Background(), connect.NewRequest(&v1.SetWhitelistEntryRequest{
		ServerId: "srv-1", PlayerQuery: "FreshName", RequestId: "wl-manual-name-1",
	}))
	if err != nil {
		t.Fatalf("manual allowlist add: %v", err)
	}
	if source.allowlist != 1 || source.lastWhitelistName != "FreshName" || source.lastWhitelistRemove {
		t.Fatalf("allowlist command not applied once with exact name: %+v", source)
	}
	result := res.Msg.Outcome.Results[0]
	if !result.Success || !result.UndoSupported || result.UndoToken == "" {
		t.Fatalf("result = %+v", result)
	}
	var row storage.PlayerModerationAction
	if err := store.DB().Where("request_id = ?", "wl-manual-name-1").First(&row).Error; err != nil {
		t.Fatalf("journal row: %v", err)
	}
	if row.Action != "allowlist_add" || row.ServerID != "srv-1" || row.Phase != storage.PlayerActionComplete {
		t.Fatalf("journal row = %+v", row)
	}
	var audit []storage.ActivityLog
	if err := store.DB().Where("event = ? AND request_id = ?", activity.EventPlayerWhitelistAdd, "wl-manual-name-1").Find(&audit).Error; err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if len(audit) != 1 {
		t.Fatalf("want one whitelist audit row, got %d", len(audit))
	}
}

func TestSetWhitelistManualAddByUUIDResolvesCurrentName(t *testing.T) {
	svc, _, source, _ := newPlayerTestService(t)
	svc.SetEdition("java")
	withLookup(svc, func(query string) identityLookup {
		if query != lookupUUID {
			t.Fatalf("unexpected UUID query: %q", query)
		}
		return foundLookup(query)
	})
	if _, err := svc.SetWhitelistEntry(context.Background(), connect.NewRequest(&v1.SetWhitelistEntryRequest{
		ServerId: "srv-1", PlayerQuery: lookupUUID, RequestId: "wl-manual-uuid-1",
	})); err != nil {
		t.Fatalf("manual allowlist add by uuid: %v", err)
	}
	if source.allowlist != 1 || source.lastWhitelistName != "NewPlayer" {
		t.Fatalf("UUID was not resolved to current name: %+v", source)
	}
}

func TestSetWhitelistManualAddBedrockRefused(t *testing.T) {
	svc, _, source, _ := newPlayerTestService(t)
	svc.SetEdition("bedrock")
	res, err := svc.SetWhitelistEntry(context.Background(), connect.NewRequest(&v1.SetWhitelistEntryRequest{
		ServerId: "srv-1", PlayerQuery: "FreshName", RequestId: "wl-bedrock-1",
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("bedrock add not refused: res=%+v err=%v", res, err)
	}
	if source.allowlist != 0 {
		t.Fatalf("bedrock add reached a command adapter: %+v", source)
	}
	if !strings.Contains(err.Error(), "Gamertag/XUID") {
		t.Fatalf("capability error lacks Bedrock explanation: %v", err)
	}
}

func TestSetWhitelistManualAddProviderOutagePersistsNothing(t *testing.T) {
	svc, store, source, _ := newPlayerTestService(t)
	svc.SetEdition("java")
	withLookup(svc, func(string) identityLookup {
		return identityLookup{Status: "unavailable", Note: "identity service request failed"}
	})
	before := countIdentities(t, store)
	_, err := svc.SetWhitelistEntry(context.Background(), connect.NewRequest(&v1.SetWhitelistEntryRequest{
		ServerId: "srv-1", PlayerQuery: "FreshName", RequestId: "wl-outage-1",
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("outage must be refused as unavailable: %v", err)
	}
	if got := countIdentities(t, store); got != before {
		t.Fatalf("outage persisted an identity: %d -> %d", before, got)
	}
	if source.allowlist != 0 {
		t.Fatalf("outage reached a command adapter: %+v", source)
	}
}

func TestSetWhitelistManualAddAmbiguousProviderPersistsNothing(t *testing.T) {
	svc, store, source, _ := newPlayerTestService(t)
	svc.SetEdition("java")
	withLookup(svc, func(string) identityLookup {
		return identityLookup{Status: "ambiguous", Note: "conflicting provider data"}
	})
	before := countIdentities(t, store)
	_, err := svc.SetWhitelistEntry(context.Background(), connect.NewRequest(&v1.SetWhitelistEntryRequest{
		ServerId: "srv-1", PlayerQuery: "FreshName", RequestId: "wl-ambiguous-1",
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("ambiguous must be refused as a precondition: %v", err)
	}
	if got := countIdentities(t, store); got != before {
		t.Fatalf("ambiguous persisted an identity: %d -> %d", before, got)
	}
	if source.allowlist != 0 {
		t.Fatalf("ambiguous reached a command adapter: %+v", source)
	}
}

func TestSetWhitelistManualAddRequiresAllowlistCapableSource(t *testing.T) {
	svc, store, source, negotiator := newPlayerTestService(t)
	svc.SetEdition("java")
	negotiator.caps.ChosenSource = "unverified_bridge"
	withLookup(svc, func(string) identityLookup {
		return identityLookup{Status: "not_found"}
	})
	before := countIdentities(t, store)
	_, err := svc.SetWhitelistEntry(context.Background(), connect.NewRequest(&v1.SetWhitelistEntryRequest{
		ServerId: "srv-1", PlayerQuery: "FreshName", RequestId: "wl-capability-1",
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("non-allowlist source must be refused: %v", err)
	}
	if got := countIdentities(t, store); got != before {
		t.Fatalf("refused capability persisted an identity: %d -> %d", before, got)
	}
	if source.allowlist != 0 {
		t.Fatalf("refused capability reached a command adapter: %+v", source)
	}
}

func TestSetWhitelistManualAddFailureKeepsJournalFailed(t *testing.T) {
	svc, store, source, _ := newPlayerTestService(t)
	svc.SetEdition("java")
	withLookup(svc, func(string) identityLookup {
		return identityLookup{Status: "not_found"}
	})
	source.failNext = errors.New("rcon unavailable")
	_, err := svc.SetWhitelistEntry(context.Background(), connect.NewRequest(&v1.SetWhitelistEntryRequest{
		ServerId: "srv-1", PlayerQuery: "FailName", RequestId: "wl-manual-fail-1",
	}))
	if err == nil {
		t.Fatal("adapter failure reported success")
	}
	var row storage.PlayerModerationAction
	if err := store.DB().Where("request_id = ?", "wl-manual-fail-1").First(&row).Error; err != nil {
		t.Fatalf("journal row: %v", err)
	}
	if row.Phase != storage.PlayerActionFailed {
		t.Fatalf("journal phase = %q, want failed", row.Phase)
	}
	var audit []storage.ActivityLog
	if err := store.DB().Where("event = ? AND request_id = ?", activity.EventPlayerWhitelistAdd, "wl-manual-fail-1").Find(&audit).Error; err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if len(audit) != 0 {
		t.Fatalf("failed action wrote %d success audit rows", len(audit))
	}
}

type portraitLookupProvider struct {
	fakeSkin
	get func(context.Context, string, string) (skinAsset, error)
}

func (p *portraitLookupProvider) Get(ctx context.Context, uuid, kind string) (skinAsset, error) {
	return p.get(ctx, uuid, kind)
}

func TestLookupProfileOptInStaysReadOnly(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	ctx := context.Background()
	seenAt := time.Date(2026, time.September, 7, 4, 0, 0, 0, time.UTC)
	known := seedIdentity(t, store, "EarlierName", lookupUUID, "srv-1", seenAt)
	seedIdentity(t, store, "NewPlayer", lookupUUID2, "srv-1", seenAt)
	calls := 0
	provider := &portraitLookupProvider{
		fakeSkin: fakeSkin{lookup: foundLookup},
		get: func(_ context.Context, uuid, kind string) (skinAsset, error) {
			calls++
			if uuid != lookupUUID || kind != "portrait" {
				t.Fatalf("portrait request got (%q, %q)", uuid, kind)
			}
			switch calls {
			case 1:
				return skinAsset{Data: []byte("fixture PNG"), ContentType: "image/png", Source: "identity_service", Confidence: "detected"}, nil
			case 2:
				return skinAsset{Data: []byte("placeholder"), ContentType: "image/png", Confidence: "unknown"}, nil
			default:
				return skinAsset{}, errors.New("texture provider unavailable")
			}
		},
	}
	svc.SetSkinProvider(provider)
	count := func(model any) int64 {
		t.Helper()
		var n int64
		if err := store.DB().Model(model).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	models := []any{&storage.PlayerIdentity{}, &storage.PlayerAlias{}, &storage.PlayerSession{}, &storage.PlayerAccessState{}, &storage.PlayerModerationAction{}, &storage.ActivityLog{}}
	before := make([]int64, len(models))
	for i, model := range models {
		before[i] = count(model)
	}
	for _, tc := range []struct {
		includeSkin, wantSkin bool
	}{
		{false, false},
		{true, true},
		{true, false},
		{true, false},
	} {
		res, err := svc.LookupPlayer(ctx, connect.NewRequest(&v1.LookupPlayerRequest{Query: "NewPlayer", IncludeSkin: tc.includeSkin}))
		if err != nil || res.Msg.Status != v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_FOUND || res.Msg.Name != "NewPlayer" {
			t.Fatalf("lookup profile: res=%+v error=%v", res, err)
		}
		if (res.Msg.Skin != nil) != tc.wantSkin || res.Msg.Observation.GetConflictNote() != "" {
			t.Fatalf("incorrect asset or observation: %v", res.Msg)
		}
		if tc.includeSkin {
			if res.Msg.ObservedIdentityKey != known.ID {
				t.Fatalf("profile did not match exact online UUID and observation: %v", res.Msg)
			}
		} else if calls != 0 || res.Msg.ObservedIdentityKey != "" {
			t.Fatal("lookup without opt-in enriched the identity")
		}
	}
	if calls != 3 {
		t.Fatalf("skin request count=%d, want 3", calls)
	}
	for i, model := range models {
		if after := count(model); after != before[i] {
			t.Errorf("lookup mutated %T: %d to %d", model, before[i], after)
		}
	}
}

func TestLookupProfileDoesNotFetchSkinForLocalOnlyIdentity(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	known := seedIdentity(t, store, "Observed", lookupUUID, "srv-1", time.Now())
	svc.SetSkinProvider(&portraitLookupProvider{
		get: func(context.Context, string, string) (skinAsset, error) {
			t.Fatal("a local-only account triggered a public skin lookup")
			return skinAsset{}, nil
		},
	})
	res, err := svc.LookupPlayer(context.Background(), connect.NewRequest(&v1.LookupPlayerRequest{Query: "Observed", IncludeSkin: true}))
	if err != nil || res.Msg.ObservedIdentityKey != known.ID || res.Msg.Skin != nil ||
		res.Msg.Observation.GetSource() != v1.PlayerDataSource_PLAYER_DATA_SOURCE_PANEL_DB ||
		res.Msg.Observation.GetObservedAt() != nil {
		t.Fatalf("local profile=%v error=%v", res, err)
	}
}

func TestLookupProfilePreservesCallerCancellation(t *testing.T) {
	svc, _, _, _ := newPlayerTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.SetSkinProvider(&portraitLookupProvider{
		fakeSkin: fakeSkin{lookup: foundLookup},
		get: func(_ context.Context, _, _ string) (skinAsset, error) {
			cancel()
			return skinAsset{}, context.Canceled
		},
	})
	_, err := svc.LookupPlayer(ctx, connect.NewRequest(&v1.LookupPlayerRequest{Query: "NewPlayer", IncludeSkin: true}))
	if connect.CodeOf(err) != connect.CodeCanceled {
		t.Fatalf("canceled lookup produced %v", err)
	}
}
