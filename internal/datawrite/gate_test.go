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

func TestExclusiveDrainReservationRefusesNewSharedWriters(t *testing.T) {
	for attempt := 0; attempt < 200; attempt++ {
		const server = "gate-drain-reservation"
		shared, err := TryShared(server)
		if err != nil {
			t.Fatalf("shared lease: %v", err)
		}

		acquired := make(chan *Lease, 1)
		failed := make(chan error, 1)
		go func() {
			lease, err := AcquireExclusive(context.Background(), server)
			if err != nil {
				failed <- err
				return
			}
			acquired <- lease
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

		shared.Release()
		for {
			select {
			case lease := <-acquired:
				lease.Release()
			case err := <-failed:
				t.Fatalf("exclusive claim: %v", err)
			default:
				// Once draining starts, a shared writer may not slip in before
				// the exclusive claim actually owns the path.
				if lease, err := TryShared(server); err == nil {
					lease.Release()
					select {
					case exclusive := <-acquired:
						exclusive.Release()
					case <-failed:
					default:
						t.Fatal("a shared writer entered after the drain completed but before exclusive ownership")
					}
				}
				if time.Now().After(deadline) {
					t.Fatal("exclusive claim did not finish after the shared writer released")
				}
				continue
			}
			break
		}
	}
}

// TestGateEntriesAreRemovedAfterLastRelease guards the map lifetime: an idle
// server must not retain a gate entry after its last lease, including refusals
// that only observed an occupied path.
func TestGateEntriesAreRemovedAfterLastRelease(t *testing.T) {
	const server = "gate-lifetime-cleanup"
	for attempt := 0; attempt < 5; attempt++ {
		shared, err := TryShared(server)
		if err != nil {
			t.Fatalf("shared lease: %v", err)
		}
		shared.Release()

		exclusive, err := AcquireExclusive(context.Background(), server)
		if err != nil {
			t.Fatalf("exclusive lease: %v", err)
		}
		if _, err := TryShared(server); !errors.Is(err, ErrExclusiveHeld) {
			t.Fatalf("shared lease during publication = %v, want ErrExclusiveHeld", err)
		}
		exclusive.Release()
	}

	gates.Lock()
	_, retained := gates.byServer[server]
	retainedCount := len(gates.byServer)
	gates.Unlock()
	if retained {
		t.Fatalf("released gate entry retained (%d total entries)", retainedCount)
	}
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
