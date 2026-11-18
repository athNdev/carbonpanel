// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playerskin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeUUIDAndKind(t *testing.T) {
	valid := []string{
		testUUID,
		strings.ToUpper(testUUID),
		"00000000-0000-0000-0000-000000000000",
	}
	for _, in := range valid {
		u, err := validateUUIDForTest(in)
		if err != nil {
			t.Errorf("validateUUIDForTest(%q): %v", in, err)
			continue
		}
		if u != strings.ToLower(in) {
			t.Errorf("uuid %q normalized to %q", in, u)
		}
	}
	invalid := []string{
		"", "not-a-uuid", "http://evil.example/x", "../../etc/passwd",
		"00112233-4455-6677-8899-aabbccddeeff ", // trailing space
		"00112233445566778899aabbccddeeff",      // no dashes
		"00112233-4455-6677-8899-aabbccddeeg",   // non-hex
		"00112233-4455-6677-8899-aabbccddeeff/../../x",
	}
	for _, in := range invalid {
		if _, err := validateUUIDForTest(in); err == nil {
			t.Errorf("validateUUIDForTest(%q) unexpectedly accepted", in)
		}
	}
	for _, k := range []string{KindFace, KindSkin, KindCape} {
		if _, _, err := validateInputs(testUUID, k); err != nil {
			t.Errorf("kind %q rejected: %v", k, err)
		}
	}
	for _, k := range []string{"", "FACE", "http://evil", "face/../../x"} {
		if _, _, err := validateInputs(testUUID, k); err == nil {
			t.Errorf("kind %q unexpectedly accepted", k)
		}
	}
}

// validateUUIDForTest exercises the same regexp used by the production API.
func validateUUIDForTest(uuid string) (string, error) {
	u, _, err := validateInputs(uuid, KindFace)
	return u, err
}

func TestGetSuccessCacheAndRefresh(t *testing.T) {
	f := newSkinFixture(t)
	c := newTestCache(t, time.Hour, nil)
	want := bytes.Clone(f.textureBody)

	first, err := c.Get(context.Background(), testUUID, KindSkin)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if first.FromCache || first.Source != SourceIdentityService || first.Confidence != ConfidenceDetected {
		t.Fatalf("fresh asset metadata = %+v", first)
	}
	if first.ContentType != contentTypePNG || !bytes.Equal(first.Data, want) || first.Note != "" {
		t.Fatalf("fresh asset payload = %+v", first)
	}

	second, err := c.Get(context.Background(), testUUID, KindSkin)
	if err != nil {
		t.Fatalf("cached Get: %v", err)
	}
	if !second.FromCache || second.Source != SourcePanelDB || !bytes.Equal(second.Data, want) {
		t.Fatalf("cached asset = %+v", second)
	}
	if p, tx, _ := f.counts(); p != 1 || tx != 1 {
		t.Fatalf("calls after cache hit = profile %d, texture %d; want 1,1", p, tx)
	}

	refreshed, err := c.Refresh(context.Background(), testUUID, KindSkin)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refreshed.FromCache || refreshed.Source != SourceIdentityService {
		t.Fatalf("refreshed asset metadata = %+v", refreshed)
	}
	if p, tx, _ := f.counts(); p != 2 || tx != 2 {
		t.Fatalf("calls after refresh = profile %d, texture %d; want 2,2", p, tx)
	}
}

func TestUUIDCaseNormalizationUsesOneCacheKey(t *testing.T) {
	f := newSkinFixture(t)
	c := newTestCache(t, time.Hour, nil)
	if _, err := c.Get(context.Background(), strings.ToUpper(testUUID), KindSkin); err != nil {
		t.Fatalf("Get upper-case UUID: %v", err)
	}
	if _, err := c.Get(context.Background(), strings.ToLower(testUUID), KindSkin); err != nil {
		t.Fatalf("Get lower-case UUID: %v", err)
	}
	if p, tx, _ := f.counts(); p != 1 || tx != 1 {
		t.Fatalf("case variants were not one cache key: profile %d, texture %d", p, tx)
	}
}

func TestNilUUIDNoNetwork(t *testing.T) {
	f := newSkinFixture(t)
	c := newTestCache(t, time.Hour, nil)
	for _, kind := range []string{KindFace, KindSkin, KindCape} {
		a, err := c.Get(context.Background(), nilUUID, kind)
		if err != nil {
			t.Fatalf("Get(nil uuid, %s): %v", kind, err)
		}
		if !strings.Contains(a.Note, "skin unavailable") || a.Confidence != ConfidenceUnknown {
			t.Fatalf("nil uuid asset = %+v", a)
		}
		if _, err := decodePNG(a.Data); err != nil {
			t.Fatalf("nil uuid did not return a valid PNG: %v", err)
		}
	}
	if _, err := c.Refresh(context.Background(), nilUUID, KindFace); err != nil {
		t.Fatalf("Refresh(nil uuid): %v", err)
	}
	if p, tx, other := f.counts(); p != 0 || tx != 0 || other != 0 {
		t.Fatalf("nil uuid made network calls: %d %d %d", p, tx, other)
	}
}

func TestNegativeCacheAndTTL(t *testing.T) {
	f := newSkinFixture(t)
	f.mu.Lock()
	f.profileStatus = 204
	f.mu.Unlock()
	c := newTestCache(t, 200*time.Millisecond, nil)

	first, err := c.Get(context.Background(), testUUID, KindFace)
	if err != nil {
		t.Fatalf("Get(204): %v", err)
	}
	if first.FromCache || !strings.Contains(first.Note, "skin unavailable") || first.Source != SourceIdentityService {
		t.Fatalf("first 204 asset = %+v", first)
	}
	second, err := c.Get(context.Background(), testUUID, KindFace)
	if err != nil {
		t.Fatalf("Get(negative cache): %v", err)
	}
	if !second.FromCache || second.Source != SourcePanelDB {
		t.Fatalf("negative cache hit = %+v", second)
	}
	if p, tx, _ := f.counts(); p != 1 || tx != 0 {
		t.Fatalf("negative cache did not suppress profile calls: %d %d", p, tx)
	}

	time.Sleep(c.negativeTTL() + 30*time.Millisecond)
	third, err := c.Get(context.Background(), testUUID, KindFace)
	if err != nil {
		t.Fatalf("Get(expired negative): %v", err)
	}
	if third.FromCache {
		t.Fatalf("expired negative entry was still reported as cached")
	}
	if p, tx, _ := f.counts(); p != 2 || tx != 0 {
		t.Fatalf("expired negative entry did not refetch: %d %d", p, tx)
	}
}

func TestPositiveTTLExpiry(t *testing.T) {
	f := newSkinFixture(t)
	c := newTestCache(t, 120*time.Millisecond, nil)
	if _, err := c.Get(context.Background(), testUUID, KindSkin); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	hit, err := c.Get(context.Background(), testUUID, KindSkin)
	if err != nil || !hit.FromCache {
		t.Fatalf("expected fresh cache hit, got %+v, %v", hit, err)
	}
	time.Sleep(160 * time.Millisecond)
	miss, err := c.Get(context.Background(), testUUID, KindSkin)
	if err != nil || miss.FromCache {
		t.Fatalf("expected expired cache miss, got %+v, %v", miss, err)
	}
	if p, tx, _ := f.counts(); p != 2 || tx != 2 {
		t.Fatalf("TTL expiry calls = %d %d; want 2,2", p, tx)
	}
}

func TestEvictionOldestFirst(t *testing.T) {
	oldMax := maxCacheEntries
	maxCacheEntries = 3
	defer func() { maxCacheEntries = oldMax }()

	f := newSkinFixture(t)
	c := newTestCache(t, time.Hour, nil)
	uuids := []string{
		"00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-000000000002",
		"00000000-0000-0000-0000-000000000003",
		"00000000-0000-0000-0000-000000000004",
	}
	for i, u := range uuids {
		if _, err := c.Get(context.Background(), u, KindSkin); err != nil {
			t.Fatalf("Get(%s): %v", u, err)
		}
		path, err := c.pathFor(u, KindSkin)
		if err != nil {
			t.Fatalf("pathFor(%s): %v", u, err)
		}
		stamp := time.Now().Add(time.Duration(i-10) * time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}
	for i, u := range uuids {
		path, err := c.pathFor(u, KindSkin)
		if err != nil {
			t.Fatalf("pathFor(%s): %v", u, err)
		}
		_, statErr := os.Stat(path)
		if i == 0 && !os.IsNotExist(statErr) {
			t.Errorf("oldest entry was not evicted: %s", path)
		}
		if i > 0 && statErr != nil {
			t.Errorf("newer entry was evicted: %s: %v", path, statErr)
		}
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != maxCacheEntries {
		t.Fatalf("cache has %d entries; want %d", len(entries), maxCacheEntries)
	}
	_ = f
}

func TestAtomicCacheWrites(t *testing.T) {
	f := newSkinFixture(t)
	c := newTestCache(t, time.Hour, nil)
	if _, err := c.Get(context.Background(), testUUID, KindSkin); err != nil {
		t.Fatalf("Get: %v", err)
	}
	path, err := c.pathFor(testUUID, KindSkin)
	if err != nil {
		t.Fatalf("pathFor: %v", err)
	}
	assertNoTempsAndValidJSON(t, c.dir, path)

	// Concurrent refreshers and readers must never observe a half-written file.
	var wg sync.WaitGroup
	errCh := make(chan string, 64)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := c.Refresh(context.Background(), testUUID, KindSkin); err != nil {
					errCh <- fmt.Sprintf("refresh: %v", err)
				}
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				raw, err := os.ReadFile(path)
				if err != nil {
					continue // a concurrent eviction may briefly remove the file
				}
				var parsed cacheEntry
				if json.Unmarshal(raw, &parsed) != nil {
					errCh <- "reader observed a partial cache file"
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
	assertNoTempsAndValidJSON(t, c.dir, path)
	_ = f

	// A stale scratch file from a crashed writer is harmless and swept.
	stale := filepath.Join(c.dir, ".stale.tmp")
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write stale temp: %v", err)
	}
	old := time.Now().Add(-staleTempAge - time.Minute)
	_ = os.Chtimes(stale, old, old)
	c.sweepExpired()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temp was not swept: %v", err)
	}
}

func assertNoTempsAndValidJSON(t *testing.T, dir, path string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %q", e.Name())
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache file: %v", err)
	}
	var parsed cacheEntry
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("cache file is not valid JSON: %v", err)
	}
	if parsed.Version != cacheVersion || parsed.Kind != KindSkin || len(parsed.Data) == 0 {
		t.Fatalf("unexpected cache entry: %+v", parsed)
	}
}

func TestPathConfinement(t *testing.T) {
	c := NewCache(t.TempDir(), time.Hour, nil)
	defer c.Close()
	path, err := c.pathFor(testUUID, KindSkin)
	if err != nil {
		t.Fatalf("pathFor: %v", err)
	}
	if filepath.Dir(path) != c.dir || !cacheNamePattern.MatchString(filepath.Base(path)) {
		t.Fatalf("cache path escaped confinement: %q (dir %q)", path, c.dir)
	}
	for _, input := range []string{"../../etc/passwd", "http://evil/x", "a\\b\\c"} {
		p, err := c.pathFor(input, "skin/../../evil")
		if err != nil {
			t.Fatalf("pathFor(%q): %v", input, err)
		}
		if filepath.Dir(p) != c.dir || !cacheNamePattern.MatchString(filepath.Base(p)) {
			t.Fatalf("attacker input escaped confinement: %q", p)
		}
	}
}

func TestCloseIdempotent(t *testing.T) {
	c := NewCache(t.TempDir(), time.Hour, nil)
	c.Close()
	c.Close()
	select {
	case <-c.done:
	default:
		t.Fatal("janitor did not stop on Close")
	}
	var nilCache *Cache
	nilCache.Close()
}

func TestConstructorBounds(t *testing.T) {
	c := NewCache(t.TempDir(), 0, nil)
	defer c.Close()
	if c.ttl != defaultTTL {
		t.Fatalf("default ttl = %v; want %v", c.ttl, defaultTTL)
	}
	c2 := NewCache(t.TempDir(), 100*maxTTL, nil)
	defer c2.Close()
	if c2.ttl != maxTTL {
		t.Fatalf("clamped ttl = %v; want %v", c2.ttl, maxTTL)
	}
	c3 := NewCache("", time.Hour, nil)
	defer c3.Close()
	if c3.initErr == nil {
		t.Fatal("empty directory was accepted")
	}
	if _, err := c3.Get(context.Background(), testUUID, KindSkin); err == nil {
		t.Fatal("Get on an invalid cache should fail")
	}
}
