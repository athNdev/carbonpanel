// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package playerskin provides a server-side, SSRF-safe skin/cape proxy with a
// bounded on-disk cache and a stdlib-only 2D face renderer.
//
// The UI must never issue a third-party request: callers pass only a canonical
// UUID and a kind, and every upstream URL is rebuilt from compile-time
// constants after the payload has been validated. No inbound request headers,
// cookies or tokens are ever forwarded upstream. Unavailable skins are
// represented by a generated neutral silhouette with an actionable Note and a
// nil error, so an RPC can always return a usable image.
package playerskin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// nilUUID is the canonical offline/unresolved sentinel. It is handled locally
// with no network call and no cache write.
const nilUUID = "00000000-0000-0000-0000-000000000000"

// Source and confidence values use the same vocabulary as the player proto.
const (
	SourceIdentityService = "identity_service"
	SourcePanelDB         = "panel_db"

	ConfidenceDetected = "detected"
	ConfidenceUnknown  = "unknown"

	KindFace = "face"
	KindSkin = "skin"
	KindCape = "cape"
	// KindPortrait is a derived front view for global account lookup.
	KindPortrait = "portrait"
)

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

	errInvalidUUID = errors.New("playerskin: invalid uuid: expected a canonical 36-character UUID")
	errInvalidKind = errors.New("playerskin: invalid kind: expected face, skin, cape or portrait")
)

// Asset is one same-origin skin/cape asset. Data is always a complete PNG: a
// fetched skin/cape, a rendered 96x96 face, or a generated neutral silhouette.
type Asset struct {
	Data        []byte
	ContentType string
	FromCache   bool
	Source      string // "identity_service" on a fresh fetch, "panel_db" on a cache hit.
	ObservedAt  time.Time
	Confidence  string // "detected" for a real texture, "unknown" when unavailable.
	Note        string // Actionable explanation when unavailable; empty otherwise.
}

// cacheClock abstracts the clock used for cache TTL decisions. Production uses
// realClock (wall time, unchanged); tests inject a controllable clock through
// the same constructor so TTL boundaries can be asserted deterministically
// without sleeping or depending on scheduler timing.
type cacheClock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// Cache is a bounded, on-disk skin cache. It is safe for concurrent use.
// All paths are derived from a sha256 key; no caller-controlled string ever
// becomes a path component.
type Cache struct {
	dir     string
	ttl     time.Duration
	client  *http.Client
	clock   cacheClock
	initErr error

	mu        sync.Mutex
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// NewCache creates a cache under dir. ttl bounds positive entries; a non-positive
// ttl selects 24h and values above seven days are clamped. client may be nil
// (http.DefaultTransport is used); it is never mutated, its cookie jar is
// ignored, and only a plain *http.Transport is honoured as the base transport
// so a wrapper cannot re-inject credentials. Network calls are bounded by ctx
// as well as by the client timeout.
func NewCache(dir string, ttl time.Duration, client *http.Client) *Cache {
	return newCache(dir, ttl, client, realClock{})
}

// newCache is the single constructor. NewCache supplies the real wall clock;
// tests supply a controllable clock over the identical production path, so no
// product behavior or TTL value is special-cased for tests.
func newCache(dir string, ttl time.Duration, client *http.Client, clock cacheClock) *Cache {
	c := &Cache{
		client: client,
		clock:  clock,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	if ttl <= 0 {
		ttl = defaultTTL
	}
	if ttl > maxTTL {
		ttl = maxTTL
	}
	c.ttl = ttl

	if dir == "" {
		c.initErr = errors.New("playerskin: empty cache directory")
	} else if abs, err := filepath.Abs(dir); err != nil {
		c.initErr = fmt.Errorf("playerskin: resolve cache directory: %w", err)
	} else {
		c.dir = filepath.Clean(abs)
		if err := os.MkdirAll(c.dir, 0o700); err != nil {
			c.initErr = fmt.Errorf("playerskin: create cache directory: %w", err)
		}
	}

	go c.janitor()
	return c
}

// Close stops the owned janitor goroutine. It is safe to call more than once
// and on a nil receiver.
func (c *Cache) Close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		close(c.stop)
		<-c.done
	})
}

// Get returns the asset for uuid/kind, using a fresh positive or negative
// cache entry when one exists. uuid must be canonical (case-insensitive); kind
// must be face, skin, cape or portrait. Invalid identifiers return an error; all upstream
// problems return an unavailable Asset with a nil error.
func (c *Cache) Get(ctx context.Context, uuid, kind string) (Asset, error) {
	u, k, err := validateInputs(uuid, kind)
	if err != nil {
		return Asset{}, err
	}
	if c.initErr != nil {
		return Asset{}, c.initErr
	}
	if u == nilUUID {
		return unavailable("skin unavailable: offline or unresolved player has no linked identity",
			SourcePanelDB, false, time.Now().UTC()), nil
	}
	if e, ok := c.read(u, k); ok {
		if e.Negative {
			return unavailable(e.Note, SourcePanelDB, true, e.ObservedAt), nil
		}
		a := e.asset()
		a.FromCache = true
		a.Source = SourcePanelDB
		return a, nil
	}
	return c.fetchAndCache(withContext(ctx), u, k), nil
}

// Refresh bypasses both positive and negative cache entries and forces a new
// upstream lookup. The freshly fetched result replaces the cached entry.
func (c *Cache) Refresh(ctx context.Context, uuid, kind string) (Asset, error) {
	u, k, err := validateInputs(uuid, kind)
	if err != nil {
		return Asset{}, err
	}
	if c.initErr != nil {
		return Asset{}, c.initErr
	}
	if u == nilUUID {
		return unavailable("skin unavailable: offline or unresolved player has no linked identity",
			SourcePanelDB, false, time.Now().UTC()), nil
	}
	return c.fetchAndCache(withContext(ctx), u, k), nil
}

func validateInputs(uuid, kind string) (string, string, error) {
	if !uuidPattern.MatchString(uuid) {
		return "", "", errInvalidUUID
	}
	u := strings.ToLower(uuid)
	switch kind {
	case KindFace, KindSkin, KindCape, KindPortrait:
		return u, kind, nil
	default:
		return "", "", errInvalidKind
	}
}

func withContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// unavailable always carries a real, generated PNG so callers never render a
// blank or broken image when a texture cannot be obtained.
func unavailable(note, source string, fromCache bool, observed time.Time) Asset {
	if observed.IsZero() {
		observed = time.Now().UTC()
	}
	return Asset{
		Data:        silhouettePNG(),
		ContentType: contentTypePNG,
		FromCache:   fromCache,
		Source:      source,
		ObservedAt:  observed,
		Confidence:  ConfidenceUnknown,
		Note:        note,
	}
}
