// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeepSleepManager_PlayerWakeDoesNotStartIntentionalStop is the O22
// in-repo seam: idle → deep sleep → operator stop → external player wake
// must leave the container stopped even when AutoDeepSleep stays enabled.
func TestDeepSleepManager_PlayerWakeDoesNotStartIntentionalStop(t *testing.T) {
	ctx := context.Background()
	controller, commander, store := newDeepSleepHarness()
	mgr := NewDeepSleepManager(controller, store, commander, nil, nil)

	require.NoError(t, mgr.SleepServer(ctx, "srv-sleep"))
	srv, err := store.GetServer(ctx, "srv-sleep")
	require.NoError(t, err)
	require.Equal(t, db.StatusDeepSleep, srv.Status)

	require.NoError(t, store.UpdateServerStatus(ctx, "srv-sleep", db.StatusStopped))
	controller.mu.Lock()
	startsBefore := len(controller.starts)
	controller.mu.Unlock()

	err = mgr.WakeServer(ctx, "srv-sleep")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-deepsleep status")

	controller.mu.Lock()
	defer controller.mu.Unlock()
	assert.Equal(t, startsBefore, len(controller.starts), "player wake must not StartContainer after an intentional stop")
	assert.Equal(t, db.StatusStopped, controller.status["cnt-sleep"])
}

func TestDeepSleepManager_PlayerWakeStatusPolicy(t *testing.T) {
	tests := []struct {
		name        string
		status      db.ServerStatus
		autoSleep   bool
		wantStart   bool
		errContains string
	}{
		{name: "deep sleep wakes", status: db.StatusDeepSleep, autoSleep: true, wantStart: true},
		{name: "deep sleep wakes with auto sleep off", status: db.StatusDeepSleep, autoSleep: false, wantStart: true},
		{name: "stopped with auto sleep stays stopped", status: db.StatusStopped, autoSleep: true, errContains: "non-deepsleep status stopped"},
		{name: "stopped without auto sleep stays stopped", status: db.StatusStopped, autoSleep: false, errContains: "non-deepsleep status stopped"},
		{name: "creating is not a player wake", status: db.StatusCreating, autoSleep: true, errContains: "non-deepsleep status creating"},
		{name: "paused is not a deep-sleep wake", status: db.StatusPaused, autoSleep: true, errContains: "non-deepsleep status paused"},
		{name: "error is not a player wake", status: db.StatusError, autoSleep: true, errContains: "non-deepsleep status error"},
		{name: "already running is idempotent", status: db.StatusRunning, autoSleep: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			controller, commander, store := newDeepSleepHarness()
			store.servers["srv-sleep"].Status = tt.status
			store.servers["srv-sleep"].AutoDeepSleep = tt.autoSleep
			controller.status["cnt-sleep"] = db.StatusStopped
			if tt.status == db.StatusRunning {
				controller.status["cnt-sleep"] = db.StatusRunning
			}

			mgr := NewDeepSleepManager(controller, store, commander, nil, nil)
			err := mgr.WakeServer(ctx, "srv-sleep")
			controller.mu.Lock()
			starts := append([]string(nil), controller.starts...)
			controller.mu.Unlock()

			if tt.wantStart {
				require.NoError(t, err)
				assert.Contains(t, starts, "cnt-sleep")
				return
			}
			if tt.errContains != "" {
				require.Error(t, err)
				assert.True(t, strings.Contains(err.Error(), tt.errContains), "error %q should contain %q", err, tt.errContains)
			} else {
				require.NoError(t, err)
			}
			assert.NotContains(t, starts, "cnt-sleep")
		})
	}
}
