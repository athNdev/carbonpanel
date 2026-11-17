// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"testing"
	"time"
)

// TestSessionRegistryIssueResolveFixture covers the opaque key lifecycle. The
// key is random, short-lived and bound to a user/server/world tuple.
func TestSessionRegistryIssueResolveFixture(t *testing.T) {
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	r := NewSessionRegistry()
	now := start
	r.now = func() time.Time { return now }

	key, expires := r.Issue("srv-a", "world-a", "user-a", 5*time.Minute)
	if key == "" || expires.Sub(start) != 5*time.Minute {
		t.Fatalf("key=%q expires=%v", key, expires)
	}
	got, ok := r.Resolve(key)
	if !ok || got.ServerID != "srv-a" || got.UserID != "user-a" || got.WorldKey != "world-a" {
		t.Fatalf("resolve = %+v, %v", got, ok)
	}

	key2, _ := r.Issue("srv-b", "world-b", "user-b", 10*time.Minute)
	if key2 == key {
		t.Fatal("two issues produced the same key")
	}
	if r.Len() != 2 {
		t.Fatalf("registry len = %d, want 2", r.Len())
	}

	// No cleanup goroutine: expiry is enforced on Resolve and Issue.
	now = start.Add(6 * time.Minute)
	if _, ok := r.Resolve(key); ok {
		t.Fatal("expired key still resolved")
	}
	if r.Len() != 1 {
		t.Fatalf("registry len after expiry = %d, want 1", r.Len())
	}
	key3, _ := r.Issue("srv-c", "world-c", "user-c", time.Minute)
	if key3 == "" || r.Len() != 2 {
		t.Fatalf("issue after cleanup failed: key=%q len=%d", key3, r.Len())
	}
}

// TestSessionRegistryEmptyKeyFixture rejects unknown or empty keys without
// panicking; the proxy maps this to 403 with no upstream request.
func TestSessionRegistryEmptyKeyFixture(t *testing.T) {
	r := NewSessionRegistry()
	if _, ok := r.Resolve(""); ok {
		t.Fatal("empty key resolved")
	}
	if _, ok := r.Resolve("not-a-real-key"); ok {
		t.Fatal("unknown key resolved")
	}
}
