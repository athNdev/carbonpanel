// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// session binds an opaque map key to one user and one concrete world. The key
// is not a credential: every proxied request still authenticates with the
// panel cookie/bearer token and re-checks world_view/read for the server.
type session struct {
	Key      string
	ServerID string
	WorldKey string
	UserID   string
	Expires  time.Time
	native   nativeScope
}

// SessionRegistry is a mutex-protected, in-memory registry with lazy expiry
// cleanup. Lazy cleanup avoids an unowned background goroutine while expired
// keys are still removed on every Issue and Resolve.
type SessionRegistry struct {
	mu    sync.Mutex
	byKey map[string]*session
	now   func() time.Time
}

func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{byKey: make(map[string]*session), now: time.Now}
}

// Issue creates a new key. serverID/userID must be non-empty; callers derive
// them from the authenticated RPC context rather than from request input.
func (r *SessionRegistry) Issue(serverID, worldKey, userID string, ttl time.Duration) (string, time.Time) {
	return r.issue(serverID, worldKey, userID, ttl, nativeScope{})
}

func (r *SessionRegistry) issue(serverID, worldKey, userID string, ttl time.Duration, native nativeScope) (string, time.Time) {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is a hard failure; return an empty key so the
		// caller reports an unavailable session rather than a predictable one.
		return "", time.Time{}
	}
	key := base64.RawURLEncoding.EncodeToString(buf)
	now := r.currentTime()
	s := &session{
		Key: key, ServerID: serverID, WorldKey: worldKey,
		UserID: userID, Expires: now.Add(ttl), native: native,
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanupLocked(now)
	r.byKey[key] = s
	return key, s.Expires
}

// Resolve returns a live session or ok=false. Expired keys are removed and
// never cause an upstream request.
func (r *SessionRegistry) Resolve(key string) (session, bool) {
	if key == "" {
		return session{}, false
	}
	now := r.currentTime()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanupLocked(now)
	s, ok := r.byKey[key]
	if !ok || !now.Before(s.Expires) {
		delete(r.byKey, key)
		return session{}, false
	}
	return *s, true
}

// Len is used by tests and diagnostics only.
func (r *SessionRegistry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byKey)
}

func (r *SessionRegistry) currentTime() time.Time {
	if r == nil || r.now == nil {
		return time.Now()
	}
	return r.now()
}

func (r *SessionRegistry) cleanupLocked(now time.Time) {
	if r.byKey == nil {
		return
	}
	for key, s := range r.byKey {
		if !now.Before(s.Expires) {
			delete(r.byKey, key)
		}
	}
}
