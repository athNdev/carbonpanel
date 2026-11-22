// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playerskin

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFixture writes a usercache.json into a fresh server data root.
func writeUsercacheFixture(t *testing.T, contents string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, UsercacheFileName), []byte(contents), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return root
}

// TestUsercacheResolverMapsNameToOnlineUUID proves the resolver reads a real
// usercache.json and maps a name (case-insensitively) to its online UUID.
func TestUsercacheResolverMapsNameToOnlineUUID(t *testing.T) {
	root := writeUsercacheFixture(t, `[
		{"name":"Steve","uuid":"e7fd1a18-8eca-4ba7-a274-28a1a9d6a2b7","expiresOn":"2026-12-01 00:00:00 +0000"},
		{"name":"Alex","uuid":"c3fee0b2-9d31-4a11-9c4e-2b2a1c5f7d33","expiresOn":"2020-01-01 00:00:00 +0000"}
	]`)
	resolver := NewUsercacheResolver(time.Minute)

	uuid, ok := resolver.Lookup(root, "steve")
	if !ok || uuid != "e7fd1a18-8eca-4ba7-a274-28a1a9d6a2b7" {
		t.Fatalf("Lookup(steve) = %q, %v", uuid, ok)
	}
	// An expired vanilla prune deadline does not invalidate the mapping: the
	// server refreshes the file itself, and renames are rare.
	if uuid, ok := resolver.Lookup(root, "ALEX"); !ok || uuid != "c3fee0b2-9d31-4a11-9c4e-2b2a1c5f7d33" {
		t.Fatalf("Lookup(ALEX) = %q, %v", uuid, ok)
	}
	if _, ok := resolver.Lookup(root, "Nobody"); ok {
		t.Fatal("unknown name resolved")
	}
}

// TestUsercacheResolverRejectsOfflineUUIDs proves version-3 (offline server)
// UUIDs are never offered: they can never resolve on the identity service and
// would only poison the skin cache with a guaranteed negative.
func TestUsercacheResolverRejectsOfflineUUIDs(t *testing.T) {
	root := writeUsercacheFixture(t, `[
		{"name":"OfflineMode","uuid":"3f3c36cf-9d0d-3bbd-a4e6-9a34c1a2e1f5","expiresOn":"2026-12-01 00:00:00 +0000"}
	]`)
	resolver := NewUsercacheResolver(time.Minute)

	if _, ok := resolver.Lookup(root, "OfflineMode"); ok {
		t.Fatal("a version-3 offline UUID was offered as a resolvable identity")
	}
}

// TestUsercacheResolverFailsSoft proves missing, malformed and oversized files
// yield no resolution instead of an error or a guess.
func TestUsercacheResolverFailsSoft(t *testing.T) {
	resolver := NewUsercacheResolver(time.Minute)

	if _, ok := resolver.Lookup(t.TempDir(), "Steve"); ok {
		t.Fatal("missing file resolved")
	}
	broken := writeUsercacheFixture(t, `{"name": not json`)
	if _, ok := resolver.Lookup(broken, "Steve"); ok {
		t.Fatal("malformed file resolved")
	}
	oversized := writeUsercacheFixture(t, string(make([]byte, usercacheReadLimit+16)))
	if _, ok := resolver.Lookup(oversized, "Steve"); ok {
		t.Fatal("oversized file resolved")
	}
	if _, ok := resolver.Lookup("", "Steve"); ok {
		t.Fatal("empty root resolved")
	}
	if _, ok := resolver.Lookup(t.TempDir(), "  "); ok {
		t.Fatal("blank name resolved")
	}
}

// TestUsercacheResolverCachesWithinTTL proves the parsed file is cached per
// data root: changes to the file are not observed until the TTL expires, and
// misses are cached too so a batch of lookups reads the file once.
func TestUsercacheResolverCachesWithinTTL(t *testing.T) {
	root := writeUsercacheFixture(t, `[
		{"name":"Steve","uuid":"e7fd1a18-8eca-4ba7-a274-28a1a9d6a2b7","expiresOn":"2026-12-01 00:00:00 +0000"}
	]`)
	clock := newFakeClock(time.Now())
	resolver := &UsercacheResolver{ttl: time.Minute, clock: clock, snapshots: make(map[string]usercacheSnapshot)}

	if _, ok := resolver.Lookup(root, "Missing"); ok {
		t.Fatal("unexpected resolution")
	}
	// The player joins and the server refreshes its cache file; the resolver
	// must keep serving the cached snapshot until the TTL expires.
	refreshed := `[
		{"name":"Steve","uuid":"e7fd1a18-8eca-4ba7-a274-28a1a9d6a2b7","expiresOn":"2026-12-01 00:00:00 +0000"},
		{"name":"Missing","uuid":"c3fee0b2-9d31-4a11-9c4e-2b2a1c5f7d33","expiresOn":"2026-12-01 00:00:00 +0000"}
	]`
	if err := os.WriteFile(filepath.Join(root, UsercacheFileName), []byte(refreshed), 0o644); err != nil {
		t.Fatalf("rewrite fixture: %v", err)
	}
	if _, ok := resolver.Lookup(root, "Missing"); ok {
		t.Fatal("TTL cache did not hold: refreshed file observed too early")
	}
	clock.Advance(time.Minute + time.Second)
	if _, ok := resolver.Lookup(root, "Missing"); !ok {
		t.Fatal("TTL expiry did not refresh the snapshot")
	}
}

// TestUsercacheResolverPublicConstructor keeps the production default explicit:
// NewUsercacheResolver uses wall time and clamps a non-positive TTL.
func TestUsercacheResolverPublicConstructor(t *testing.T) {
	resolver := NewUsercacheResolver(0)
	if _, ok := resolver.clock.(realClock); !ok {
		t.Fatalf("production resolver clock = %T; want realClock", resolver.clock)
	}
	if resolver.ttl <= 0 {
		t.Fatalf("non-positive TTL not defaulted: %v", resolver.ttl)
	}
}
