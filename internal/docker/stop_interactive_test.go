// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"context"
	"errors"
	"testing"
	"time"

	models "github.com/athNdev/carbon-panel/internal/db"
)

// TestInteractiveStopWindow guards the user-initiated stop default: short by
// default (the RPC holds the lifecycle transition for the whole window), but
// an explicit operator override is still honored up to MaxStopWindow.
func TestInteractiveStopWindow(t *testing.T) {
	if got := InteractiveStopWindow(nil); got != DefaultInteractiveStopWindow {
		t.Fatalf("nil config window = %s, want %s", got, DefaultInteractiveStopWindow)
	}
	if got := InteractiveStopWindow(&models.ServerConfig{}); got != DefaultInteractiveStopWindow {
		t.Fatalf("no override window = %s, want %s", got, DefaultInteractiveStopWindow)
	}
	zero := 0
	if got := InteractiveStopWindow(&models.ServerConfig{StopDuration: &zero}); got != DefaultInteractiveStopWindow {
		t.Fatalf("zero override window = %s, want %s", got, DefaultInteractiveStopWindow)
	}
	if DefaultInteractiveStopWindow >= DefaultStopWindow {
		t.Fatalf("interactive default %s must stay below the internal default %s", DefaultInteractiveStopWindow, DefaultStopWindow)
	}
	sixty := 60
	if got := InteractiveStopWindow(&models.ServerConfig{StopDuration: &sixty}); got != 60*time.Second {
		t.Fatalf("explicit override window = %s, want 60s", got)
	}
	huge := 9999
	if got := InteractiveStopWindow(&models.ServerConfig{StopDuration: &huge}); got != MaxStopWindow {
		t.Fatalf("override cap = %s, want %s", got, MaxStopWindow)
	}
}

// TestApplyStopLadderEscalatesAfterShortWindow verifies the stop ladder still
// escalates to ForceKill when DockerStop fails within the shorter interactive
// window — the shorter default must not weaken the escalation.
func TestApplyStopLadderEscalatesAfterShortWindow(t *testing.T) {
	rec := &recordingStop{stopErr: errors.New("injected docker stop failure")}
	if err := applyStopLadder(context.Background(), rec, "c1", DefaultInteractiveStopWindow); err != nil {
		t.Fatal(err)
	}
	if rec.timeout != int(DefaultInteractiveStopWindow/time.Second) {
		t.Fatalf("docker timeout = %d, want %d", rec.timeout, int(DefaultInteractiveStopWindow/time.Second))
	}
	if rec.kills != 1 {
		t.Fatalf("kill calls = %d, want 1 after docker stop failure", rec.kills)
	}
}
