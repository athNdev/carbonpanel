// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playerskin

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// fakeClock is a test-only cacheClock. Time stands still until a test advances
// it explicitly, so TTL boundary assertions never depend on wall time, sleeps,
// or scheduler noise. Production still uses realClock via NewCache.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t.UTC()} }

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// newTestCacheWithClock builds a Cache over the same production constructor as
// NewCache, substituting only the clock. No TTL or cache semantics are changed.
func newTestCacheWithClock(t *testing.T, ttl time.Duration, client *http.Client, clock cacheClock) *Cache {
	t.Helper()
	c := newCache(t.TempDir(), ttl, client, clock)
	t.Cleanup(c.Close)
	return c
}

// TestNewCacheUsesRealClock keeps the production default explicit: the public
// constructor must still use wall time, while only tests inject a fake clock.
func TestNewCacheUsesRealClock(t *testing.T) {
	c := NewCache(t.TempDir(), time.Hour, nil)
	t.Cleanup(c.Close)
	if _, ok := c.clock.(realClock); !ok {
		t.Fatalf("production cache clock = %T; want realClock", c.clock)
	}
}
