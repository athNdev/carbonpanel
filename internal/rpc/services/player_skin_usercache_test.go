// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	storage "github.com/athNdev/carbon-panel/internal/db"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// offlineSkinUUID is a canonical online (version 4) UUID used as the
// usercache-resolved identity in these fixtures.
const offlineSkinUUID = "e7fd1a18-8eca-4ba7-a274-28a1a9d6a2b7"

func writeUsercache(t *testing.T, dataPath, contents string) {
	t.Helper()
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		t.Fatalf("create data path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "usercache.json"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write usercache: %v", err)
	}
}

// TestGetPlayerSkinResolvesOfflineIdentityViaServerUsercache proves the
// resolution order: a name-only (offline) identity with a panel-derived
// pseudo-UUID resolves its real online UUID through the owning server's own
// usercache.json before anything upstream is asked.
func TestGetPlayerSkinResolvesOfflineIdentityViaServerUsercache(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	fake := &fakeSkin{}
	svc.SetSkinProvider(fake)

	server, err := store.GetServer(context.Background(), "srv-1")
	if err != nil {
		t.Fatalf("load server: %v", err)
	}
	writeUsercache(t, server.DataPath,
		`[{"name":"OfflineSteve","uuid":"`+offlineSkinUUID+`","expiresOn":"2026-12-01 00:00:00 +0000"}]`)

	identity, err := store.EnsureOfflinePlayerIdentity(context.Background(), "OfflineSteve", "srv-1", storage.SourceManualAllowlist)
	if err != nil {
		t.Fatalf("manual allowlist add: %v", err)
	}
	if identity.PrimaryUUID == offlineSkinUUID || !identity.OfflineUUID {
		t.Fatalf("fixture identity is not an offline pseudo-identity: %+v", identity)
	}

	res, err := svc.GetPlayerSkin(context.Background(), connect.NewRequest(&v1.GetPlayerSkinRequest{
		IdentityKey: identity.ID,
		Kind:        "face",
		ServerId:    "srv-1",
	}))
	if err != nil {
		t.Fatalf("GetPlayerSkin: %v", err)
	}
	if fake.lastUUID != offlineSkinUUID {
		t.Fatalf("skin requested for %q, want the usercache-resolved %q", fake.lastUUID, offlineSkinUUID)
	}
	if fake.lastKind != "face" {
		t.Fatalf("skin kind = %q, want face", fake.lastKind)
	}
	if res.Msg.Asset == nil || len(res.Msg.Asset.Data) == 0 {
		t.Fatal("resolved skin returned no asset")
	}
}

// TestGetPlayerSkinOfflineIdentityWithoutUsercacheStaysLocal proves the
// panel-derived pseudo-UUID is never sent upstream: without a usercache
// mapping the request resolves to the local placeholder instead of caching a
// guaranteed Mojang negative for a key that can never become valid.
func TestGetPlayerSkinOfflineIdentityWithoutUsercacheStaysLocal(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	fake := &fakeSkin{}
	svc.SetSkinProvider(fake)

	identity, err := store.EnsureOfflinePlayerIdentity(context.Background(), "NoCacheUser", "srv-1", storage.SourceManualAllowlist)
	if err != nil {
		t.Fatalf("manual allowlist add: %v", err)
	}

	res, err := svc.GetPlayerSkin(context.Background(), connect.NewRequest(&v1.GetPlayerSkinRequest{
		IdentityKey: identity.ID,
		Kind:        "face",
		ServerId:    "srv-1",
	}))
	if err != nil {
		t.Fatalf("GetPlayerSkin: %v", err)
	}
	if fake.lastUUID != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("pseudo-UUID %q was sent upstream, want the local placeholder", fake.lastUUID)
	}
	if res.Msg.Asset == nil || len(res.Msg.Asset.Data) == 0 {
		t.Fatal("placeholder path returned no asset")
	}
}

// TestListPlayersSkinAvailableHonorsUsercache proves SkinAvailable on the
// roster: true for a real online UUID, true for an offline identity the
// server's usercache can map, and false otherwise so the console can skip the
// request entirely.
func TestListPlayersSkinAvailableHonorsUsercache(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	server, err := store.GetServer(context.Background(), "srv-1")
	if err != nil {
		t.Fatalf("load server: %v", err)
	}
	writeUsercache(t, server.DataPath,
		`[{"name":"OfflineSteve","uuid":"`+offlineSkinUUID+`","expiresOn":"2026-12-01 00:00:00 +0000"}]`)

	manual, err := store.EnsureOfflinePlayerIdentity(context.Background(), "OfflineSteve", "srv-1", storage.SourceManualAllowlist)
	if err != nil {
		t.Fatalf("manual allowlist add: %v", err)
	}
	// An access state is what carries the server association for a
	// never-observed identity, so the row resolves a server for the lookup.
	if err := store.RecordPlayerAccess(context.Background(), storage.PlayerAccessState{
		IdentityID: manual.ID, ServerID: "srv-1", Whitelisted: true,
		Source: "rcon", Confidence: "detected", ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed access state: %v", err)
	}
	observed := seedIdentity(t, store, "OnlinePlayer", "55555555-5555-5555-5555-555555555555", "srv-1", time.Now())

	res, err := svc.ListPlayers(context.Background(), connect.NewRequest(&v1.ListPlayersRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byKey := map[string]*v1.PlayerSummary{}
	for _, player := range res.Msg.Players {
		byKey[player.Ref.GetIdentityKey()] = player
	}
	if row := byKey[manual.ID]; row == nil || !row.SkinAvailable {
		t.Fatalf("usercache-resolvable offline identity not marked skin-available: %+v", byKey[manual.ID])
	}
	if row := byKey[observed.ID]; row == nil || !row.SkinAvailable {
		t.Fatalf("online identity not marked skin-available: %+v", byKey[observed.ID])
	}
}

// TestListPlayersSkinAvailableFalseWithoutUsercache proves an offline identity
// with no usercache mapping reports SkinAvailable=false, which lets the
// console skip the skin request.
func TestListPlayersSkinAvailableFalseWithoutUsercache(t *testing.T) {
	svc, store, _, _ := newPlayerTestService(t)
	manual, err := store.EnsureOfflinePlayerIdentity(context.Background(), "UnmappedUser", "srv-1", storage.SourceManualAllowlist)
	if err != nil {
		t.Fatalf("manual allowlist add: %v", err)
	}

	res, err := svc.ListPlayers(context.Background(), connect.NewRequest(&v1.ListPlayersRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, player := range res.Msg.Players {
		if player.Ref.GetIdentityKey() == manual.ID && player.SkinAvailable {
			t.Fatal("unmapped offline identity claimed skin availability")
		}
	}
}
