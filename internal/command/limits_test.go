// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package command

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/auth"
)

// Oversized commands are refused before any transport is touched.
func TestSendCommandRejectsOversizedCommand(t *testing.T) {
	store := testStore(t)
	makeServer(t, store, "s-len", "c-len")
	exec := &fakeExecutor{output: "ok"}
	s := testSender(store, exec, nil)

	_, err := s.SendCommand(context.Background(), "s-len", strings.Repeat("x", maxCommandBytes+1))
	if !errors.Is(err, ErrCommandTooLong) {
		t.Fatalf("oversized command err = %v, want ErrCommandTooLong", err)
	}
	if exec.calls != 0 {
		t.Fatalf("oversized command reached executor %d times", exec.calls)
	}
}

// A command at the cap is legitimate traffic and must still be delivered
// through both the RCON attempt and the docker-exec fallback seam.
func TestSendCommandAllowsCommandAtLengthCap(t *testing.T) {
	store := testStore(t)
	makeServer(t, store, "s-cap", "c-cap")
	exec := &fakeExecutor{output: "ok"}
	s := testSender(store, exec, nil)

	if _, err := s.SendCommand(context.Background(), "s-cap", strings.Repeat("x", maxCommandBytes)); err != nil {
		t.Fatalf("command at cap was refused: %v", err)
	}
	if exec.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", exec.calls)
	}
}

// Flooding one server across the shared sender seam is refused after the
// bounded budget, and the bucket refills after the window passes.
func TestSendCommandFloodIsRateLimited(t *testing.T) {
	store := testStore(t)
	makeServer(t, store, "s-flood", "c-flood")
	exec := &fakeExecutor{output: "ok"}
	s := testSender(store, exec, nil)
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	s.limiter.now = func() time.Time { return now }

	for i := 0; i < commandRateLimit; i++ {
		if _, err := s.SendCommand(context.Background(), "s-flood", "list"); err != nil {
			t.Fatalf("legitimate command %d refused: %v", i, err)
		}
	}
	if _, err := s.SendCommand(context.Background(), "s-flood", "list"); !errors.Is(err, ErrCommandRateLimited) {
		t.Fatalf("flood err = %v, want ErrCommandRateLimited", err)
	}
	if exec.calls != commandRateLimit {
		t.Fatalf("executor calls = %d, want %d", exec.calls, commandRateLimit)
	}

	now = now.Add(commandRateWindow + time.Second)
	if _, err := s.SendCommand(context.Background(), "s-flood", "list"); err != nil {
		t.Fatalf("command after window refill refused: %v", err)
	}
	if exec.calls != commandRateLimit+1 {
		t.Fatalf("executor calls after refill = %d, want %d", exec.calls, commandRateLimit+1)
	}
}

// Unknown server IDs must not create limiter keys: the rate check runs only
// after the server lookup so arbitrary IDs cannot grow the key map.
func TestCommandRateLimitIgnoresUnknownServers(t *testing.T) {
	s := testSender(testStore(t), &fakeExecutor{output: "ok"}, nil)
	for i := 0; i < commandRateLimit+10; i++ {
		if _, err := s.SendCommand(context.Background(), "does-not-exist", "list"); err == nil {
			t.Fatal("unknown server unexpectedly succeeded")
		}
	}
	if len(s.limiter.events) != 0 {
		t.Fatalf("limiter created %d keys for unknown servers", len(s.limiter.events))
	}
}

// Idle limiter keys are pruned once the map grows beyond its normal bound.
func TestCommandLimiterPrunesIdleKeys(t *testing.T) {
	l := newCommandLimiter()
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	stale := now.Add(-2 * commandRateWindow)
	for i := 0; i < 1100; i++ {
		l.events[fmt.Sprintf("key-%d", i)] = []time.Time{stale}
	}
	if !l.allow("active") {
		t.Fatal("active key was refused")
	}
	if len(l.events) != 1 {
		t.Fatalf("limiter retained %d idle keys, want only the active one", len(l.events))
	}
}

// Distinct authenticated users have separate budgets on the same server.
func TestCommandRateLimitKeysByAuthenticatedUser(t *testing.T) {
	store := testStore(t)
	makeServer(t, store, "s-users", "c-users")
	exec := &fakeExecutor{output: "ok"}
	s := testSender(store, exec, nil)
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	s.limiter.now = func() time.Time { return now }

	ctxA := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "user-a"})
	ctxB := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "user-b"})
	for i := 0; i < commandRateLimit; i++ {
		if _, err := s.SendCommand(ctxA, "s-users", "list"); err != nil {
			t.Fatalf("user-a command %d refused: %v", i, err)
		}
	}
	if _, err := s.SendCommand(ctxB, "s-users", "list"); err != nil {
		t.Fatalf("user-b budget must be independent: %v", err)
	}
	if exec.calls != commandRateLimit+1 {
		t.Fatalf("executor calls = %d, want %d", exec.calls, commandRateLimit+1)
	}
}
