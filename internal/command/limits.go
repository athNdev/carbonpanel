// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package command

import (
	"errors"
	"sync"
	"time"
)

const (
	// maxCommandBytes matches the existing WebSocket console bound so the
	// generated RPC console path has the same limit as the browser console.
	maxCommandBytes = 4096
	// commandRateLimit is a generous per-principal (or per-server for
	// background jobs without a user) burst budget per sliding window.
	commandRateLimit  = 60
	commandRateWindow = time.Minute
)

var (
	ErrCommandTooLong     = errors.New("command exceeds maximum length")
	ErrCommandRateLimited = errors.New("command rate limit exceeded")
)

// commandLimiter is a bounded in-memory sliding-window limiter. It deliberately
// has no persistence: a controller restart clears a burst budget, which is
// preferable to a stale lease that could block legitimate operations.
type commandLimiter struct {
	mu     sync.Mutex
	now    func() time.Time
	events map[string][]time.Time
}

func newCommandLimiter() *commandLimiter {
	return &commandLimiter{
		now:    time.Now,
		events: make(map[string][]time.Time),
	}
}

func (l *commandLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-commandRateWindow)
	// Keys are validated server IDs or authenticated users, so the set is
	// normally small. Prune idle keys if it grows unusually large so a long
	//-lived controller cannot retain entries forever.
	if len(l.events) > 1024 {
		for idleKey, stamps := range l.events {
			keep := stamps[:0]
			for _, at := range stamps {
				if at.After(cutoff) {
					keep = append(keep, at)
				}
			}
			if len(keep) == 0 {
				delete(l.events, idleKey)
			} else {
				l.events[idleKey] = keep
			}
		}
	}
	recent := l.events[key][:0]
	for _, at := range l.events[key] {
		if at.After(cutoff) {
			recent = append(recent, at)
		}
	}
	if len(recent) >= commandRateLimit {
		l.events[key] = recent
		return false
	}
	l.events[key] = append(recent, now)
	return true
}
