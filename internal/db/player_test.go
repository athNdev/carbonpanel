// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/go-gormigrate/gormigrate/v2"
)

func newPlayerStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewSQLiteStore(&config.Config{
		Database: config.DatabaseConfig{
			Path:        filepath.Join(t.TempDir(), "player-identity.db"),
			AutoMigrate: true,
		},
		Docker: config.DockerConfig{Host: "unix:///var/run/docker.sock"},
	})
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestManualAllowlistDoesNotStampLastSeen proves a manual allowlist add
// registers the identity without claiming an in-game observation: last_seen_at
// stays nil, and re-registering the same name does not invent one later.
func TestManualAllowlistDoesNotStampLastSeen(t *testing.T) {
	store := newPlayerStore(t)
	ctx := context.Background()

	identity, err := store.EnsureOfflinePlayerIdentity(ctx, "NeverJoined", "srv-1", SourceManualAllowlist)
	if err != nil {
		t.Fatalf("manual allowlist add: %v", err)
	}
	if identity.LastSeenAt != nil {
		t.Fatalf("manual add stamped last_seen_at: %v", identity.LastSeenAt)
	}
	if identity.FirstSeenAt.IsZero() {
		t.Fatal("manual add must still record when the identity was first recorded")
	}

	stored, err := store.GetPlayerIdentity(ctx, identity.ID)
	if err != nil {
		t.Fatalf("reload identity: %v", err)
	}
	if stored.LastSeenAt != nil {
		t.Fatalf("stored manual identity has last_seen_at %v, want nil", stored.LastSeenAt)
	}

	// A repeated manual add must not retroactively claim an observation.
	if _, err := store.EnsureOfflinePlayerIdentity(ctx, "NeverJoined", "srv-1", SourceManualAllowlist); err != nil {
		t.Fatalf("repeated manual add: %v", err)
	}
	stored, err = store.GetPlayerIdentity(ctx, identity.ID)
	if err != nil {
		t.Fatalf("reload after re-add: %v", err)
	}
	if stored.LastSeenAt != nil {
		t.Fatalf("repeated manual add stamped last_seen_at: %v", stored.LastSeenAt)
	}
}

// TestObservationStampsLastSeenButRecordedDoesNotBump proves the split
// semantics: ingestion stamps last_seen_at on create and re-observation, while
// a recorded-only refresh (operator action / lookup) leaves it untouched.
func TestObservationStampsLastSeenButRecordedDoesNotBump(t *testing.T) {
	store := newPlayerStore(t)
	ctx := context.Background()
	const uuid = "11111111-2222-3333-4444-555555555555"

	identity, err := store.EnsurePlayerIdentity(ctx, uuid, "Observed", "", "", false)
	if err != nil {
		t.Fatalf("observed create: %v", err)
	}
	if identity.LastSeenAt == nil {
		t.Fatal("ingestion must stamp last_seen_at on create")
	}

	// Pin a known past observation so a silent bump is detectable.
	past := time.Now().UTC().Add(-48 * time.Hour)
	if err := store.DB().Model(&PlayerIdentity{}).Where("id = ?", identity.ID).
		Update("last_seen_at", past).Error; err != nil {
		t.Fatalf("pin last seen: %v", err)
	}

	if _, err := store.EnsurePlayerIdentityRecorded(ctx, uuid, "Renamed", "", "", false); err != nil {
		t.Fatalf("recorded refresh: %v", err)
	}
	stored, err := store.GetPlayerIdentity(ctx, identity.ID)
	if err != nil {
		t.Fatalf("reload after recorded refresh: %v", err)
	}
	if stored.CurrentName != "Renamed" {
		t.Fatalf("recorded refresh did not update the name: %q", stored.CurrentName)
	}
	if stored.LastSeenAt == nil || !stored.LastSeenAt.Equal(past) {
		t.Fatalf("recorded refresh moved last_seen_at to %v, want pinned %v", stored.LastSeenAt, past)
	}

	if _, err := store.EnsurePlayerIdentity(ctx, uuid, "Renamed", "", "", false); err != nil {
		t.Fatalf("re-observation: %v", err)
	}
	stored, err = store.GetPlayerIdentity(ctx, identity.ID)
	if err != nil {
		t.Fatalf("reload after re-observation: %v", err)
	}
	if stored.LastSeenAt == nil || stored.LastSeenAt.Equal(past) {
		t.Fatalf("ingestion re-observation did not stamp last_seen_at: %v", stored.LastSeenAt)
	}
}

// TestListPlayerIdentitiesSortsNeverObservedLast proves NULLS LAST ordering:
// an identity without any observation sorts after observed ones regardless of
// insertion order.
func TestListPlayerIdentitiesSortsNeverObservedLast(t *testing.T) {
	store := newPlayerStore(t)
	ctx := context.Background()

	// The never-observed identity is inserted first so a naive
	// insertion-ordered or NULLS-first query would put it on top.
	manual, err := store.EnsureOfflinePlayerIdentity(ctx, "NeverJoined", "srv-1", SourceManualAllowlist)
	if err != nil {
		t.Fatalf("manual add: %v", err)
	}
	observed, err := store.EnsurePlayerIdentity(ctx, "22222222-2222-2222-2222-222222222222", "Observed", "", "", false)
	if err != nil {
		t.Fatalf("observed create: %v", err)
	}

	identities, err := store.ListPlayerIdentities(ctx, "", "", 0)
	if err != nil {
		t.Fatalf("list identities: %v", err)
	}
	if len(identities) != 2 {
		t.Fatalf("want 2 identities, got %d", len(identities))
	}
	if identities[0].ID != observed.ID || identities[1].ID != manual.ID {
		t.Fatalf("never-observed identity did not sort last: %s before %s", identities[0].ID, identities[1].ID)
	}
}

// TestPlayerLastSeenBackfillNullsSessionlessIdentities runs the registered
// migration against populated player tables: session-less identities (the
// manual-allowlist signature of the old bug) are reset to "never observed"
// while identities with session evidence keep their real observation time.
func TestPlayerLastSeenBackfillNullsSessionlessIdentities(t *testing.T) {
	store := newPlayerStore(t)
	gdb := store.DB()
	ctx := context.Background()

	observed, err := store.EnsurePlayerIdentity(ctx, "33333333-3333-3333-3333-333333333333", "HasSession", "", "", false)
	if err != nil {
		t.Fatalf("observed create: %v", err)
	}
	if _, err := store.StartPlayerSession(ctx, observed.ID, "srv-1", "metrics", time.Now().UTC()); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	manual, err := store.EnsureOfflinePlayerIdentity(ctx, "LegacyManual", "srv-1", SourceManualAllowlist)
	if err != nil {
		t.Fatalf("manual add: %v", err)
	}

	var migration *gormigrate.Migration
	for _, m := range migrations() {
		if m.ID == playerLastSeenObservationMigrationID {
			migration = m
			break
		}
	}
	if migration == nil {
		t.Fatalf("migration %s is not registered", playerLastSeenObservationMigrationID)
	}
	if err := migration.Migrate(gdb); err != nil {
		t.Fatalf("backfill migration failed: %v", err)
	}

	storedManual, err := store.GetPlayerIdentity(ctx, manual.ID)
	if err != nil {
		t.Fatalf("reload manual identity: %v", err)
	}
	if storedManual.LastSeenAt != nil {
		t.Fatalf("session-less identity kept a last_seen value: %v", storedManual.LastSeenAt)
	}
	storedObserved, err := store.GetPlayerIdentity(ctx, observed.ID)
	if err != nil {
		t.Fatalf("reload observed identity: %v", err)
	}
	if storedObserved.LastSeenAt == nil {
		t.Fatal("backfill erased the last_seen of an identity with session evidence")
	}

	// Re-applying must be safe.
	if err := migration.Migrate(gdb); err != nil {
		t.Fatalf("idempotent re-apply failed: %v", err)
	}
}
