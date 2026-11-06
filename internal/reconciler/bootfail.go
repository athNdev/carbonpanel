// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package reconciler

import (
	"context"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/minecraft"
)

// bootFailureLogTail bounds the one-shot log read crash detection performs
// per sweep for a non-healthy server.
const bootFailureLogTail = 300

// shouldScanBootFailure reports whether the game log is worth consulting for
// a crash. Docker-level status cannot see a dead JVM inside a live
// entrypoint, so a container that never gets healthy needs its logs read.
// Shutdown states are excluded: flagging a server someone is stopping would
// fight the operator with a red herring.
func shouldScanBootFailure(desired db.ServerStatus, obs observedState) bool {
	if !obs.Exists || obs.ContainerID == "" || obs.StartedAt.IsZero() {
		return false
	}
	switch desired {
	case db.StatusStarting,
		db.StatusCreating,
		db.StatusRunning,
		db.StatusUnhealthy,
		db.StatusError:
		// Boot states, steady-state health loss, and reason backfill.
	default:
		return false
	}
	return obs.Status == db.StatusStarting || obs.Status == db.StatusUnhealthy
}

// detectBootFailure reads the current boot's log tail for machine-precise
// fatal markers (a mod-loader hard-dependency failure, the image runner's
// exit-code report). A miss is not evidence of health, only of no proof.
func (r *Reconciler) detectBootFailure(ctx context.Context, cli Client, obs observedState) (string, bool) {
	lines, err := cli.TailContainerLogs(ctx, obs.ContainerID, bootFailureLogTail)
	if err != nil {
		if r.log != nil {
			r.log.Debug("reconciler: boot failure scan skipped: %v", err)
		}
		return "", false
	}
	return minecraft.DetectBootFailure(lines, obs.StartedAt)
}
