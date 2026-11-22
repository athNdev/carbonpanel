// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// TestBeginServerLifecycleTransitionErrors guards the regression where a
// missing server id produced the misleading "server transition already in
// progress" (TryBeginServerTransition("") fails), wasting integrators' time.
func TestBeginServerLifecycleTransitionErrors(t *testing.T) {
	for _, id := range []string{"", "   "} {
		_, err := beginServerLifecycleTransition(id)
		if err == nil {
			t.Fatalf("id %q: want error", id)
		}
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Fatalf("id %q: code = %v, want invalid_argument", id, got)
		}
	}

	transition, err := beginServerLifecycleTransition("srv-guard")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	defer transition.Release()

	_, err = beginServerLifecycleTransition("srv-guard")
	if err == nil {
		t.Fatal("contended claim: want error")
	}
	if got := connect.CodeOf(err); got != connect.CodeAborted {
		t.Fatalf("contended claim: code = %v, want aborted", got)
	}
}

// TestAwaitInFlightStopProceedsWhenSettled covers the start-behind-stop wait:
// a stop that has finished (or never held the transition) lets the start
// proceed without touching the transition waiter.
func TestAwaitInFlightStopProceedsWhenSettled(t *testing.T) {
	waited := false
	err := awaitInFlightStop(context.Background(), "srv-wait", 5*time.Second, time.Millisecond,
		func() bool { return false },
		func(context.Context) { waited = true },
		func() bool { return true })
	if err != nil {
		t.Fatalf("settled wait: %v", err)
	}
	if waited {
		t.Fatal("transition waiter must not run when nothing is in flight")
	}
}

// TestAwaitInFlightStopTimesOutWithClearMessage covers a stop that never
// finishes: the start is refused with aborted and a message naming the wait,
// after blocking on the in-flight transition.
func TestAwaitInFlightStopTimesOutWithClearMessage(t *testing.T) {
	waited := false
	err := awaitInFlightStop(context.Background(), "srv-wait", 40*time.Millisecond, time.Millisecond,
		func() bool { return true },
		func(context.Context) { waited = true },
		func() bool { return false })
	if err == nil {
		t.Fatal("unsettled stop: want timeout error")
	}
	if got := connect.CodeOf(err); got != connect.CodeAborted {
		t.Fatalf("code = %v, want aborted", got)
	}
	if !strings.Contains(err.Error(), "server is still stopping") || !strings.Contains(err.Error(), "waited") {
		t.Fatalf("message must explain the unfinished stop: %v", err)
	}
	if !waited {
		t.Fatal("transition waiter was not invoked while the stop held the lock")
	}
}

// TestWaitForInFlightStopIgnoresNonStoppingServers keeps the wait scoped to a
// server whose recorded status is stopping.
func TestWaitForInFlightStopIgnoresNonStoppingServers(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	svc := &ServerService{store: store, config: &config.Config{}, log: logger.New()}
	for _, status := range []storage.ServerStatus{storage.StatusRunning, storage.StatusStopped, storage.StatusPaused} {
		server := &storage.Server{ID: "srv-" + string(status), Status: status}
		if err := svc.waitForInFlightStop(context.Background(), server); err != nil {
			t.Fatalf("status %s: %v", status, err)
		}
	}
}
