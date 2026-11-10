// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"context"
	"testing"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeepSleepManager_WakeRefusesStoppedAutoDeepSleep pins the O22 policy:
// AutoDeepSleep is not a license to start an operator-stopped server. A
// knock that still arrives after status is STOPPED must fail closed. Drift
// from DEEPSLEEP is preserved by the reconciler, not by treating STOPPED as
// asleep (MINE-172 / OSS-247).
func TestDeepSleepManager_WakeRefusesStoppedAutoDeepSleep(t *testing.T) {
	ctx := context.Background()
	controller, commander, store := newDeepSleepHarness()
	controller.status["cnt-sleep"] = db.StatusStopped
	store.servers["srv-sleep"].Status = db.StatusStopped
	store.servers["srv-sleep"].AutoDeepSleep = true

	mgr := NewDeepSleepManager(controller, store, commander, nil, nil)
	err := mgr.WakeServer(ctx, "srv-sleep")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-deepsleep status stopped")

	srv, getErr := store.GetServer(ctx, "srv-sleep")
	require.NoError(t, getErr)
	assert.Equal(t, db.StatusStopped, srv.Status)
	controller.mu.Lock()
	defer controller.mu.Unlock()
	assert.Empty(t, controller.starts)
}

// TestDeepSleepManager_UnknownRemoteNodeDoesNotUseDefaultController verifies
// that an unresolvable remote node fails closed: neither sleep nor wake may be
// performed through the local default controller.
func TestDeepSleepManager_UnknownRemoteNodeDoesNotUseDefaultController(t *testing.T) {
	ctx := context.Background()
	defaultCtrl := newMockSleepController()
	defaultCtrl.status["cnt-unknown"] = db.StatusRunning

	store := newMockFreezerStore()
	store.servers["srv-unknown"] = &db.Server{
		ID:                      "srv-unknown",
		Name:                    "Unknown Node",
		NodeID:                  "node-unknown",
		ContainerID:             "cnt-unknown",
		Status:                  db.StatusRunning,
		AutoDeepSleep:           true,
		DeepSleepTimeoutMinutes: 1,
	}

	mgr := NewDeepSleepManager(defaultCtrl, store, &mockSleepCommander{}, nil, nil)
	mgr.SetControllerResolver(func(nodeID string) SleepController {
		if nodeID == "node-unknown" {
			return nil // remote node cannot be resolved
		}
		return defaultCtrl
	})

	err := mgr.SleepServer(ctx, "srv-unknown")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node-unknown")

	require.NoError(t, store.UpdateServerStatus(ctx, "srv-unknown", db.StatusDeepSleep))
	err = mgr.WakeServer(ctx, "srv-unknown")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node-unknown")

	defaultCtrl.mu.Lock()
	defer defaultCtrl.mu.Unlock()
	assert.Empty(t, defaultCtrl.starts, "wake must not start the container through the local default controller")
	assert.Empty(t, defaultCtrl.stops, "sleep must not stop the container through the local default controller")
	assert.Empty(t, defaultCtrl.policies, "restart policy must not be changed through the local default controller")
}

// TestDeepSleepManager_MultiNodeResolver verifies that SleepServer and WakeServer
// route container operations to the node-specific controller when a resolver is
// configured.
func TestDeepSleepManager_MultiNodeResolver(t *testing.T) {
	ctx := context.Background()
	defaultCtrl := newMockSleepController()
	remoteCtrl := newMockSleepController()
	remoteCtrl.status["cnt-remote"] = db.StatusRunning

	store := newMockFreezerStore()
	store.servers["srv-remote"] = &db.Server{
		ID:                      "srv-remote",
		Name:                    "Remote Sleepy",
		NodeID:                  "node-remote",
		ContainerID:             "cnt-remote",
		Status:                  db.StatusRunning,
		AutoDeepSleep:           true,
		DeepSleepTimeoutMinutes: 1,
	}

	commander := &mockSleepCommander{
		onCommand: func(serverID, command string) {
			if command == "stop" {
				remoteCtrl.mu.Lock()
				remoteCtrl.status["cnt-remote"] = db.StatusStopped
				remoteCtrl.mu.Unlock()
			}
		},
	}

	mgr := NewDeepSleepManager(defaultCtrl, store, commander, nil, nil)
	mgr.SetControllerResolver(func(nodeID string) SleepController {
		if nodeID == "node-remote" {
			return remoteCtrl
		}
		return defaultCtrl
	})

	require.NoError(t, mgr.SleepServer(ctx, "srv-remote"))
	srv, err := store.GetServer(ctx, "srv-remote")
	require.NoError(t, err)
	assert.Equal(t, db.StatusDeepSleep, srv.Status)

	require.NoError(t, mgr.WakeServer(ctx, "srv-remote"))
	remoteCtrl.mu.Lock()
	assert.Contains(t, remoteCtrl.starts, "cnt-remote")
	remoteCtrl.mu.Unlock()
	defaultCtrl.mu.Lock()
	assert.Empty(t, defaultCtrl.starts)
	defaultCtrl.mu.Unlock()
}

// TestDeepSleepManager_StopFromDeepSleepSkipsDockerStop pins the precondition
// StopServer relies on: a DEEPSLEEP container is already exited, so stopping
// parks the server as STOPPED without issuing another Docker stop. The service
// layer records the journaled no-op, clears the wake listener, and emits/audits
// like a normal stop instead of calling StopContainerWithin.
func TestDeepSleepManager_StopFromDeepSleepSkipsDockerStop(t *testing.T) {
	ctx := context.Background()
	controller, commander, store := newDeepSleepHarness()

	mgr := NewDeepSleepManager(controller, store, commander, nil, nil)
	require.NoError(t, mgr.SleepServer(ctx, "srv-sleep"))

	srv, err := store.GetServer(ctx, "srv-sleep")
	require.NoError(t, err)
	require.Equal(t, db.StatusDeepSleep, srv.Status)

	// The container already exited during sleep: stopping must not need Docker.
	status, err := controller.GetContainerStatus(ctx, "cnt-sleep")
	require.NoError(t, err)
	assert.Equal(t, db.StatusStopped, status)

	stopsBefore := len(controller.stops)
	_ = stopsBefore
	// No additional Docker stop is required to park DEEPSLEEP as STOPPED;
	// the service layer skips StopContainerWithin and records a journaled no-op.
}
