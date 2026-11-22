// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playerskin

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// UsercacheFileName is the vanilla server cache file that maps player names to
// the UUIDs the server itself issued. It lives in the server data root.
const UsercacheFileName = "usercache.json"

// usercacheReadLimit bounds a single usercache.json read. Real files are a few
// kilobytes; anything beyond the bound is treated as unreadable rather than
// trusted.
const usercacheReadLimit = 4 << 20 // 4 MiB

// usercacheEntry is one row of the vanilla usercache.json array:
// [{"name":"Steve","uuid":"...","expiresOn":"2026-11-23 12:00:00 +0000"}].
// expiresOn is vanilla's lazy-prune deadline, not a validity statement about
// the name→UUID mapping (renames are rare and the server refreshes the file),
// so expired entries are still accepted here.
type usercacheEntry struct {
	Name      string `json:"name"`
	UUID      string `json:"uuid"`
	ExpiresOn string `json:"expiresOn"`
}

// usercacheSnapshot is one parsed usercache.json, keyed by lowercased name.
type usercacheSnapshot struct {
	readAt time.Time
	byName map[string]string
}

// UsercacheResolver maps player names to their online UUIDs using a server's
// own usercache.json. Offline-mode and name-only identities have a
// panel-derived pseudo-UUID that can never resolve upstream; the server's
// cache is the first-party, on-disk source that can. Lookups are soft: any
// error (missing file, unreadable JSON, oversized file) yields no resolution
// and never a fallback guess. Results are cached in memory per data root with
// a TTL, including misses, so a roster-sized batch of lookups reads the file
// at most once per TTL. It is safe for concurrent use.
type UsercacheResolver struct {
	ttl   time.Duration
	clock cacheClock

	mu        sync.Mutex
	snapshots map[string]usercacheSnapshot
}

// NewUsercacheResolver returns a resolver with the given TTL; a non-positive
// TTL selects 15 minutes and values above 24 hours are clamped.
func NewUsercacheResolver(ttl time.Duration) *UsercacheResolver {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if ttl > 24*time.Hour {
		ttl = 24 * time.Hour
	}
	return &UsercacheResolver{ttl: ttl, clock: realClock{}, snapshots: make(map[string]usercacheSnapshot)}
}

// Lookup returns the online UUID the usercache of the server rooted at
// dataRoot records for name. The second result is false when there is no
// usable mapping; errors are never surfaced.
func (r *UsercacheResolver) Lookup(dataRoot, name string) (string, bool) {
	if r == nil {
		return "", false
	}
	root := strings.TrimSpace(dataRoot)
	player := strings.ToLower(strings.TrimSpace(name))
	if root == "" || player == "" {
		return "", false
	}
	snapshot := r.snapshot(root)
	if snapshot == nil {
		return "", false
	}
	uuid, ok := snapshot.byName[player]
	if !ok {
		return "", false
	}
	return uuid, true
}

// snapshot returns the cached parse for a data root, refreshing it when the
// TTL has expired. A missing or unreadable file is cached as an empty
// snapshot so a batch of lookups does not re-read a nonexistent file.
func (r *UsercacheResolver) snapshot(root string) *usercacheSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cached, ok := r.snapshots[root]; ok && r.clock.Now().Sub(cached.readAt) < r.ttl {
		return &cached
	}
	parsed := readUsercacheSnapshot(filepath.Join(root, UsercacheFileName), r.clock.Now())
	r.snapshots[root] = *parsed
	return parsed
}

// readUsercacheSnapshot parses one usercache.json. It never fails: an
// unreadable or malformed file yields an empty snapshot carrying the read
// time, so the negative result is cached for the TTL like any other.
func readUsercacheSnapshot(path string, now time.Time) *usercacheSnapshot {
	empty := &usercacheSnapshot{readAt: now, byName: map[string]string{}}
	file, err := os.Open(path)
	if err != nil {
		return empty
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, usercacheReadLimit+1))
	if err != nil || int64(len(data)) > usercacheReadLimit {
		return empty
	}
	var entries []usercacheEntry
	if json.Unmarshal(data, &entries) != nil {
		return empty
	}
	byName := make(map[string]string, len(entries))
	for _, entry := range entries {
		name := strings.ToLower(strings.TrimSpace(entry.Name))
		uuid := strings.TrimSpace(entry.UUID)
		// Only online (version 4) UUIDs are offered: an offline-mode server
		// writes version 3 name-derived UUIDs that can never resolve on the
		// identity service, and offering them would only poison the skin
		// cache with a guaranteed negative.
		if name == "" || !isOnlineUUID(uuid) {
			continue
		}
		byName[name] = strings.ToLower(uuid)
	}
	return &usercacheSnapshot{readAt: now, byName: byName}
}

// isOnlineUUID reports whether value is a canonical dashed UUID whose version
// nibble marks it as an online (version 4, RFC 4122 variant) identifier.
func isOnlineUUID(value string) bool {
	if !uuidPattern.MatchString(value) {
		return false
	}
	// Canonical dashed form 8-4-4-4-12: the version nibble sits at index 14
	// and the variant nibble at index 19.
	if value[14] != '4' {
		return false
	}
	switch value[19] {
	case '8', '9', 'a', 'b':
		return true
	default:
		return false
	}
}
