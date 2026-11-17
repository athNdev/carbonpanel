// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package serverruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAdmissionCoordinatorDifferentNodesRunInParallel(t *testing.T) {
	c := NewAdmissionCoordinator()
	releaseA, err := c.Acquire(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()

	done := make(chan func(), 1)
	go func() {
		releaseB, err := c.Acquire(context.Background(), "node-b")
		if err != nil {
			t.Error(err)
			done <- nil
			return
		}
		done <- releaseB
	}()
	select {
	case releaseB := <-done:
		if releaseB != nil {
			releaseB()
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a different node was blocked by an unrelated start")
	}
}

func TestAdmissionCoordinatorSameNodeSerializesAndReleasesOnce(t *testing.T) {
	c := NewAdmissionCoordinator()
	releaseFirst, err := c.Acquire(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan func(), 1)
	go func() {
		release, err := c.Acquire(context.Background(), "node-a")
		if err != nil {
			t.Error(err)
			acquired <- nil
			return
		}
		acquired <- release
	}()
	select {
	case <-acquired:
		t.Fatal("a second start on the same node entered the gate concurrently")
	case <-time.After(80 * time.Millisecond):
	}

	// Idempotent release must not corrupt the keyed lock.
	releaseFirst()
	releaseFirst()

	select {
	case release := <-acquired:
		if release == nil {
			t.Fatal("second acquire failed")
		}
		release()
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire never entered after release")
	}

	// The refcount dropped to zero, so the key was removed and a fresh holder
	// can enter again (no stale token blocking).
	release, err := c.Acquire(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestAdmissionCoordinatorCancellationNeverEntersGate(t *testing.T) {
	c := NewAdmissionCoordinator()
	release, err := c.Acquire(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := c.Acquire(ctx, "node-a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked acquire error = %v, want deadline", err)
	}

	preCancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := c.Acquire(preCancelled, "node-b"); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled acquire error = %v, want canceled", err)
	}

	// A cancelled waiter must not leak a reference: after the holder releases,
	// the next acquire on the same key succeeds promptly.
	release()
	next, err := c.Acquire(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("post-cancellation acquire failed: %v", err)
	}
	next()
}

// raceCancelContext is cancelled on Done, but its Err reports nil on the first
// read and Canceled afterwards. This forces the exact race where the gate token
// and cancellation are both ready when Acquire selects.
type raceCancelContext struct {
	done    chan struct{}
	mu      sync.Mutex
	errRead bool
}

func newRaceCancelContext() *raceCancelContext {
	ctx := &raceCancelContext{done: make(chan struct{})}
	close(ctx.done)
	return ctx
}
func (c *raceCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *raceCancelContext) Done() <-chan struct{}       { return c.done }
func (c *raceCancelContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.errRead {
		return context.Canceled
	}
	c.errRead = true
	return nil
}
func (c *raceCancelContext) Value(any) any { return nil }

func TestAdmissionCoordinatorCancelRacingReleaseDoesNotEnterGate(t *testing.T) {
	for i := 0; i < 200; i++ {
		c := NewAdmissionCoordinator()
		ctx := newRaceCancelContext()
		release, err := c.Acquire(ctx, "node-race")
		if release != nil {
			t.Fatal("cancelled acquire returned a gate")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("race acquire error = %v, want canceled", err)
		}
		// The token and the key entry must be fully restored for the next caller.
		next, err := c.Acquire(context.Background(), "node-race")
		if err != nil {
			t.Fatalf("post-race acquire failed: %v", err)
		}
		next()
	}
}

func TestCanonicalNodeKeyFoldsAliases(t *testing.T) {
	for _, alias := range []string{"", "local", "LOCAL", "default"} {
		if got := canonicalNodeKey(alias); got != "local" {
			t.Fatalf("canonicalNodeKey(%q) = %q, want local", alias, got)
		}
	}
	if got := canonicalNodeKey(" Node-A "); got != "node-a" {
		t.Fatalf("canonicalNodeKey = %q", got)
	}
}

func TestNilCoordinatorFailsClosed(t *testing.T) {
	var c *AdmissionCoordinator
	if _, err := c.Acquire(context.Background(), "node-a"); err == nil {
		t.Fatal("nil coordinator must fail closed")
	}
}
