// SPDX-License-Identifier: MPL-2.0
package datawrite

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSharedWritersCoexistAndRefuseAnExclusiveClaimUntilTheyFinish(t *testing.T) {
	const server = "gate-shared"
	first, err := TryShared(server)
	if err != nil {
		t.Fatalf("first shared lease: %v", err)
	}
	second, err := TryShared(server)
	if err != nil {
		t.Fatalf("second shared lease: %v", err)
	}

	acquired := make(chan *Lease, 1)
	acquireErr := make(chan error, 1)
	go func() {
		lease, err := AcquireExclusive(context.Background(), server)
		if err != nil {
			acquireErr <- err
			return
		}
		acquired <- lease
	}()

	// Wait until the exclusive claim is draining, so the next shared attempt
	// observes the fence rather than racing the goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for {
		gate := gateFor(server)
		gate.mu.Lock()
		draining := gate.draining
		gate.mu.Unlock()
		if draining {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exclusive claim did not start draining")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := TryShared(server); !errors.Is(err, ErrExclusiveHeld) {
		t.Fatalf("shared lease during drain = %v, want ErrExclusiveHeld", err)
	}

	first.Release()
	select {
	case err := <-acquireErr:
		t.Fatalf("exclusive claim failed early: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	second.Release()
	var exclusive *Lease
	select {
	case exclusive = <-acquired:
	case err := <-acquireErr:
		t.Fatalf("exclusive claim: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("exclusive claim did not drain shared writers")
	}
	if _, err := TryShared(server); !errors.Is(err, ErrExclusiveHeld) {
		t.Fatalf("shared lease during publication = %v, want ErrExclusiveHeld", err)
	}
	exclusive.Release()
	exclusive.Release()

	after, err := TryShared(server)
	if err != nil {
		t.Fatalf("shared lease after publication: %v", err)
	}
	after.Release()
}

func TestSecondExclusiveClaimIsRefused(t *testing.T) {
	const server = "gate-exclusive"
	first, err := AcquireExclusive(context.Background(), server)
	if err != nil {
		t.Fatalf("exclusive lease: %v", err)
	}
	defer first.Release()
	if _, err := AcquireExclusive(context.Background(), server); !errors.Is(err, ErrExclusiveBusy) {
		t.Fatalf("second exclusive = %v, want ErrExclusiveBusy", err)
	}
}

func TestExclusiveWaitHonorsContext(t *testing.T) {
	const server = "gate-cancel"
	shared, err := TryShared(server)
	if err != nil {
		t.Fatalf("shared lease: %v", err)
	}
	defer shared.Release()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := AcquireExclusive(ctx, server)
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		gate := gateFor(server)
		gate.mu.Lock()
		draining := gate.draining
		gate.mu.Unlock()
		if draining {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exclusive claim did not start draining")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled exclusive = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled exclusive claim did not return")
	}
	// A failed waiter must not leave the gate draining forever.
	gate := gateFor(server)
	gate.mu.Lock()
	draining := gate.draining
	gate.mu.Unlock()
	if draining {
		t.Fatal("canceled exclusive claim left the gate draining")
	}
	shared.Release()
	lease, err := AcquireExclusive(context.Background(), server)
	if err != nil {
		t.Fatalf("exclusive after cancel and shared release: %v", err)
	}
	lease.Release()
}

func TestGatesArePerServer(t *testing.T) {
	first, err := AcquireExclusive(context.Background(), "gate-server-a")
	if err != nil {
		t.Fatalf("exclusive a: %v", err)
	}
	defer first.Release()
	other, err := TryShared("gate-server-b")
	if err != nil {
		t.Fatalf("shared b: %v", err)
	}
	other.Release()
}
