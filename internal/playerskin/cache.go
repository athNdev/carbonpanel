// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playerskin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	cacheExt     = ".cache"
	cacheVersion = 1

	defaultTTL = 24 * time.Hour
	// maxTTL keeps a misconfigured caller from pinning stale textures forever.
	maxTTL = 7 * 24 * time.Hour
	// defaultNegativeTTL/minNegativeTTL make 204/404 entries short-lived.
	defaultNegativeTTL = 15 * time.Minute
	minNegativeTTL     = 100 * time.Millisecond

	// staleTempAge bounds orphaned scratch files left by a crashed writer.
	staleTempAge = 10 * time.Minute
)

// Overridable in tests only; production uses the fixed limit.
var (
	maxCacheEntries = 256
	janitorInterval = time.Hour
)

var cacheNamePattern = regexp.MustCompile(`^[0-9a-f]{64}\.cache$`)

// cacheEntry is the on-disk representation. []byte is marshalled as base64 by
// encoding/json, so the file is plain JSON with no binary framing.
type cacheEntry struct {
	Version     int       `json:"version"`
	Kind        string    `json:"kind"`
	Data        []byte    `json:"data,omitempty"`
	ContentType string    `json:"content_type"`
	Source      string    `json:"source"`
	ObservedAt  time.Time `json:"observed_at"`
	Confidence  string    `json:"confidence"`
	Note        string    `json:"note,omitempty"`
	Negative    bool      `json:"negative,omitempty"`
}

func (e cacheEntry) asset() Asset {
	return Asset{
		Data:        e.Data,
		ContentType: e.ContentType,
		Source:      e.Source,
		ObservedAt:  e.ObservedAt,
		Confidence:  e.Confidence,
		Note:        e.Note,
	}
}

// pathFor derives every path from sha256(uuid|kind) and verifies confinement to
// the cache directory. uuid/kind are normally already validated; the extra
// checks make path traversal structurally impossible even if a caller changes.
func (c *Cache) pathFor(uuid, kind string) (string, error) {
	sum := sha256.Sum256([]byte(uuid + "|" + kind))
	name := hex.EncodeToString(sum[:]) + cacheExt
	if !cacheNamePattern.MatchString(name) {
		return "", errors.New("playerskin: invalid cache key")
	}
	path := filepath.Join(c.dir, name)
	if filepath.Dir(path) != c.dir || filepath.Base(path) != name {
		return "", errors.New("playerskin: cache path escaped its directory")
	}
	return path, nil
}

func (c *Cache) negativeTTL() time.Duration {
	n := c.ttl / 4
	if n > defaultNegativeTTL {
		n = defaultNegativeTTL
	}
	if n < minNegativeTTL {
		n = minNegativeTTL
	}
	return n
}

// read returns a live cache entry. Expired, corrupt, future-dated and
// wrong-version entries are treated as misses.
func (c *Cache) read(uuid, kind string) (cacheEntry, bool) {
	if c.initErr != nil {
		return cacheEntry{}, false
	}
	path, err := c.pathFor(uuid, kind)
	if err != nil {
		return cacheEntry{}, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	raw, err := os.ReadFile(path)
	if err != nil {
		return cacheEntry{}, false
	}
	var e cacheEntry
	if json.Unmarshal(raw, &e) != nil || e.Version != cacheVersion || e.Kind != kind {
		return cacheEntry{}, false
	}
	life := c.ttl
	if e.Negative {
		life = c.negativeTTL()
	}
	now := time.Now().UTC()
	if e.ObservedAt.After(now) || now.Sub(e.ObservedAt) >= life {
		return cacheEntry{}, false
	}
	return e, true
}

// write atomically replaces the entry via a temp file in the same directory,
// then applies oldest-first eviction to keep the cache bounded.
func (c *Cache) write(uuid, kind string, e cacheEntry) {
	if c.initErr != nil {
		return
	}
	path, err := c.pathFor(uuid, kind)
	if err != nil {
		return
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	key := strings.TrimSuffix(filepath.Base(path), cacheExt)
	tmp, err := os.CreateTemp(c.dir, "."+key+".*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		// Some platforms refuse to replace an existing destination; retry
		// explicitly. On Unix the first rename is already atomic.
		_ = os.Remove(path)
		if err := os.Rename(tmpName, path); err != nil {
			return
		}
	}
	committed = true
	c.evictLocked()
}

// evictLocked removes the oldest entries by modification time until at most
// maxCacheEntries remain. The caller holds c.mu.
func (c *Cache) evictLocked() {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type item struct {
		path string
		mod  time.Time
	}
	items := make([]item, 0, len(entries))
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), cacheExt) {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		items = append(items, item{path: filepath.Join(c.dir, de.Name()), mod: info.ModTime()})
	}
	if len(items) <= maxCacheEntries {
		return
	}
	slices.SortFunc(items, func(a, b item) int { return a.mod.Compare(b.mod) })
	for i := 0; i < len(items)-maxCacheEntries; i++ {
		_ = os.Remove(items[i].path)
	}
}

// sweepExpired removes expired entries and stale scratch files. It is invoked
// by the owned janitor and directly by tests.
func (c *Cache) sweepExpired() {
	if c.initErr != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if strings.HasSuffix(name, ".tmp") {
			if info, err := de.Info(); err == nil && now.Sub(info.ModTime()) > staleTempAge {
				_ = os.Remove(filepath.Join(c.dir, name))
			}
			continue
		}
		if !strings.HasSuffix(name, cacheExt) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(c.dir, name))
		if err != nil {
			continue
		}
		var e cacheEntry
		if json.Unmarshal(raw, &e) != nil || e.Version != cacheVersion {
			_ = os.Remove(filepath.Join(c.dir, name))
			continue
		}
		life := c.ttl
		if e.Negative {
			life = c.negativeTTL()
		}
		if e.ObservedAt.After(now) || now.Sub(e.ObservedAt) >= life {
			_ = os.Remove(filepath.Join(c.dir, name))
		}
	}
}

// janitor owns the periodic sweep so Close has real work to stop.
func (c *Cache) janitor() {
	defer close(c.done)
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			c.sweepExpired()
		}
	}
}
