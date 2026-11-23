// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// fakeFreezer records the freeze requests the service forwards to the
// hibernation manager.
type fakeFreezer struct {
	calls []string
	err   error
}

func (f *fakeFreezer) HibernateServer(_ context.Context, serverID string) error {
	f.calls = append(f.calls, serverID)
	return f.err
}

// fakeSleeper records the deep-sleep requests the service forwards to the
// deep sleep manager.
type fakeSleeper struct {
	calls []string
	err   error
}

func (f *fakeSleeper) SleepServer(_ context.Context, serverID string) error {
	f.calls = append(f.calls, serverID)
	return f.err
}

func newPowerModeServer(t *testing.T, store *storage.Store, id string, status storage.ServerStatus) *storage.Server {
	t.Helper()
	ctx := context.Background()
	server := &storage.Server{
		ID: id, Name: id, NodeID: "local", Port: 25581, Memory: 2048, MaxPlayers: 20,
		ContainerID: "container-" + id, Status: status,
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	return server
}

func TestPauseServerFreezesRunningServer(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	server := newPowerModeServer(t, store, "pause-running", storage.StatusRunning)
	freezer := &fakeFreezer{}
	svc := &ServerService{store: store, log: logger.New()}
	svc.hibernation = freezer

	res, err := svc.PauseServer(ctx, connect.NewRequest(&v1.PauseServerRequest{
		Id: server.ID, RequestId: "req-pause-1",
	}))
	if err != nil {
		t.Fatalf("PauseServer: %v", err)
	}
	if res.Msg.Status != "paused" {
		t.Fatalf("PauseServer status = %q, want paused", res.Msg.Status)
	}
	if len(freezer.calls) != 1 || freezer.calls[0] != server.ID {
		t.Fatalf("freezer calls = %v, want one call for %s", freezer.calls, server.ID)
	}
}

func TestPauseServerRefusesServerThatIsNotRunning(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	server := newPowerModeServer(t, store, "pause-stopped", storage.StatusStopped)
	freezer := &fakeFreezer{}
	svc := &ServerService{store: store, log: logger.New()}
	svc.hibernation = freezer

	_, err := svc.PauseServer(ctx, connect.NewRequest(&v1.PauseServerRequest{
		Id: server.ID, RequestId: "req-pause-2",
	}))
	connErr, ok := err.(*connect.Error)
	if !ok || connErr.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("PauseServer error = %T %v, want CodeFailedPrecondition", err, err)
	}
	if len(freezer.calls) != 0 {
		t.Fatalf("freezer calls = %v, want no call", freezer.calls)
	}
}

func TestPauseServerFailsClosedWithoutManager(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	server := newPowerModeServer(t, store, "pause-unconfigured", storage.StatusRunning)
	svc := &ServerService{store: store, log: logger.New()}

	_, err := svc.PauseServer(ctx, connect.NewRequest(&v1.PauseServerRequest{
		Id: server.ID, RequestId: "req-pause-3",
	}))
	connErr, ok := err.(*connect.Error)
	if !ok || connErr.Code() != connect.CodeUnavailable {
		t.Fatalf("PauseServer error = %T %v, want CodeUnavailable", err, err)
	}
}

func TestPauseServerReportsFreezeFailure(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	server := newPowerModeServer(t, store, "pause-failure", storage.StatusRunning)
	freezer := &fakeFreezer{err: errors.New("cgroup freezer unavailable")}
	svc := &ServerService{store: store, log: logger.New()}
	svc.hibernation = freezer

	_, err := svc.PauseServer(ctx, connect.NewRequest(&v1.PauseServerRequest{
		Id: server.ID, RequestId: "req-pause-4",
	}))
	connErr, ok := err.(*connect.Error)
	if !ok || connErr.Code() != connect.CodeInternal {
		t.Fatalf("PauseServer error = %T %v, want CodeInternal", err, err)
	}
}

func TestDeepSleepServerParksRunningServer(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	server := newPowerModeServer(t, store, "sleep-running", storage.StatusRunning)
	sleeper := &fakeSleeper{}
	svc := &ServerService{store: store, log: logger.New()}
	svc.deepSleep = sleeper

	res, err := svc.DeepSleepServer(ctx, connect.NewRequest(&v1.DeepSleepServerRequest{
		Id: server.ID, RequestId: "req-sleep-1",
	}))
	if err != nil {
		t.Fatalf("DeepSleepServer: %v", err)
	}
	if res.Msg.Status != "deepsleep" {
		t.Fatalf("DeepSleepServer status = %q, want deepsleep", res.Msg.Status)
	}
	if len(sleeper.calls) != 1 || sleeper.calls[0] != server.ID {
		t.Fatalf("sleeper calls = %v, want one call for %s", sleeper.calls, server.ID)
	}
}

func TestDeepSleepServerRefusesServerThatIsNotRunning(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	server := newPowerModeServer(t, store, "sleep-paused", storage.StatusPaused)
	sleeper := &fakeSleeper{}
	svc := &ServerService{store: store, log: logger.New()}
	svc.deepSleep = sleeper

	_, err := svc.DeepSleepServer(ctx, connect.NewRequest(&v1.DeepSleepServerRequest{
		Id: server.ID, RequestId: "req-sleep-2",
	}))
	connErr, ok := err.(*connect.Error)
	if !ok || connErr.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("DeepSleepServer error = %T %v, want CodeFailedPrecondition", err, err)
	}
	if len(sleeper.calls) != 0 {
		t.Fatalf("sleeper calls = %v, want no call", sleeper.calls)
	}
}

func TestDeepSleepServerFailsClosedWithoutManager(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	server := newPowerModeServer(t, store, "sleep-unconfigured", storage.StatusRunning)
	svc := &ServerService{store: store, log: logger.New()}

	_, err := svc.DeepSleepServer(ctx, connect.NewRequest(&v1.DeepSleepServerRequest{
		Id: server.ID, RequestId: "req-sleep-3",
	}))
	connErr, ok := err.(*connect.Error)
	if !ok || connErr.Code() != connect.CodeUnavailable {
		t.Fatalf("DeepSleepServer error = %T %v, want CodeUnavailable", err, err)
	}
}

func TestPowerModeRequestsRejectUnknownServer(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	svc := &ServerService{store: store, log: logger.New()}
	svc.hibernation = &fakeFreezer{}
	svc.deepSleep = &fakeSleeper{}

	if _, err := svc.PauseServer(ctx, connect.NewRequest(&v1.PauseServerRequest{
		Id: "missing", RequestId: "req-pause-missing",
	})); err == nil {
		t.Fatal("PauseServer() error = nil for unknown server")
	} else if connErr, ok := err.(*connect.Error); !ok || connErr.Code() != connect.CodeNotFound {
		t.Fatalf("PauseServer error = %T %v, want CodeNotFound", err, err)
	}
	if _, err := svc.DeepSleepServer(ctx, connect.NewRequest(&v1.DeepSleepServerRequest{
		Id: "missing", RequestId: "req-sleep-missing",
	})); err == nil {
		t.Fatal("DeepSleepServer() error = nil for unknown server")
	} else if connErr, ok := err.(*connect.Error); !ok || connErr.Code() != connect.CodeNotFound {
		t.Fatalf("DeepSleepServer error = %T %v, want CodeNotFound", err, err)
	}
}
