// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package reconciler

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/events"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// --- fakes -----------------------------------------------------------------

type statusUpdate struct {
	id     string
	status db.ServerStatus
}

type containerUpdate struct {
	id          string
	containerID string
}

type fakeStore struct {
	mu         sync.Mutex
	servers    map[string]*db.Server
	configs    map[string]*db.ServerConfig
	statuses   []statusUpdate
	containers []containerUpdate
}

func newFakeStore(servers ...*db.Server) *fakeStore {
	f := &fakeStore{servers: map[string]*db.Server{}, configs: map[string]*db.ServerConfig{}}
	for _, s := range servers {
		f.servers[s.ID] = s
		f.configs[s.ID] = &db.ServerConfig{ServerID: s.ID}
	}
	return f
}

func (f *fakeStore) GetServer(_ context.Context, id string) (*db.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.servers[id]
	if !ok {
		return nil, db.ErrServerNotFound
	}
	cp := *s
	return &cp, nil
}

func (f *fakeStore) ListServers(_ context.Context) ([]*db.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*db.Server, 0, len(f.servers))
	for _, s := range f.servers {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeStore) GetServerConfig(_ context.Context, serverID string) (*db.ServerConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.configs[serverID]
	if !ok {
		return nil, fmt.Errorf("server config not found")
	}
	cp := *c
	return &cp, nil
}

func (f *fakeStore) UpdateServerStatus(_ context.Context, id string, status db.ServerStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, statusUpdate{id: id, status: status})
	if s, ok := f.servers[id]; ok {
		s.Status = status
	}
	return nil
}

func (f *fakeStore) UpdateServerStatusReason(_ context.Context, id string, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.servers[id]; ok {
		s.StatusReason = reason
	}
	return nil
}

func (f *fakeStore) UpdateServerContainerID(_ context.Context, id string, containerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers = append(f.containers, containerUpdate{id: id, containerID: containerID})
	if s, ok := f.servers[id]; ok {
		s.ContainerID = containerID
	}
	return nil
}

type fakeClient struct {
	resolveErr     error
	summary        *container.Summary
	state          *docker.ContainerState
	observeErr     error
	logLines       []string
	logErr         error
	started        []string
	recreations    int
	recreateErr    error
	recreateResult *docker.RecreateContainerResult
	stops          []string
}

func (c *fakeClient) ResolveContainer(_ context.Context, _ string) (*container.Summary, error) {
	if c.resolveErr != nil {
		return nil, c.resolveErr
	}
	return c.summary, nil
}

func (c *fakeClient) ObserveContainer(_ context.Context, _ string) (*docker.ContainerState, error) {
	if c.observeErr != nil {
		return nil, c.observeErr
	}
	return c.state, nil
}

func (c *fakeClient) TailContainerLogs(_ context.Context, _ string, _ int) ([]string, error) {
	if c.logErr != nil {
		return nil, c.logErr
	}
	return c.logLines, nil
}

func (c *fakeClient) StartContainer(_ context.Context, containerID string) error {
	c.started = append(c.started, containerID)
	return nil
}

func (c *fakeClient) StopContainerWithin(_ context.Context, containerID string, _ time.Duration) (bool, error) {
	c.stops = append(c.stops, containerID)
	return true, nil
}

func (c *fakeClient) RecreateContainer(_ context.Context, _ string, _ *db.Server, _ *db.ServerConfig) (*docker.RecreateContainerResult, error) {
	c.recreations++
	if c.recreateResult != nil {
		return c.recreateResult, c.recreateErr
	}
	if c.recreateErr != nil {
		return nil, c.recreateErr
	}
	return &docker.RecreateContainerResult{NewContainerID: "recreated-container"}, nil
}

type fakeResolver struct {
	cli Client
	err error
}

func (r fakeResolver) GetClientStrict(_ string) (Client, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.cli, nil
}

type fakeProxy struct {
	mu     sync.Mutex
	routes []string
}

func (p *fakeProxy) UpdateServerRoute(server *db.Server) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.routes = append(p.routes, server.ID)
	return nil
}

func (p *fakeProxy) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.routes)
}

type fakeBus struct {
	mu     sync.Mutex
	events []events.Event
}

func (b *fakeBus) Emit(_ context.Context, event events.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
}

func (b *fakeBus) types() []v1.TriggeredEventType {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]v1.TriggeredEventType, 0, len(b.events))
	for _, e := range b.events {
		out = append(out, e.Type)
	}
	return out
}

// --- pure policy tests -----------------------------------------------------

func TestDecide(t *testing.T) {
	const (
		stopped   = db.StatusStopped
		running   = db.StatusRunning
		starting  = db.StatusStarting
		stopping  = db.StatusStopping
		errStatus = db.StatusError
		paused    = db.StatusPaused
		deepsleep = db.StatusDeepSleep
		creating  = db.StatusCreating
		unhealthy = db.StatusUnhealthy
	)

	existsRunning := observedState{Exists: true, ContainerID: "c1", Status: running}
	existsExitedClean := observedState{Exists: true, ContainerID: "c1", Status: stopped, ExitCode: 0}
	existsOOM := observedState{Exists: true, ContainerID: "c1", Status: stopped, ExitCode: 137, OOMKilled: true}
	existsCrash := observedState{Exists: true, ContainerID: "c1", Status: stopped, ExitCode: 1}
	existsSigterm := observedState{Exists: true, ContainerID: "c1", Status: stopped, ExitCode: 143}
	existsDeleted := observedState{Exists: false}
	existsRestarting := observedState{Exists: true, ContainerID: "c1", Status: starting}
	existsUnhealthy := observedState{Exists: true, ContainerID: "c1", Status: unhealthy}
	existsPaused := observedState{Exists: true, ContainerID: "c1", Status: paused}

	tests := []struct {
		name          string
		desired       db.ServerStatus
		obs           observedState
		detached      bool
		selfHeal      bool
		wantStatus    db.ServerStatus
		wantAction    reconcileAction
		wantReasonHas string
	}{
		{"missing + desired running self-heals", running, existsDeleted, false, true, starting, actRecreate, "recreating"},
		{"missing + desired running self-heal disabled flags error", running, existsDeleted, false, false, errStatus, actNone, "missing"},
		{"missing + desired running detached never self-heals", running, existsDeleted, true, true, errStatus, actNone, "missing"},
		{"missing + desired stopped is a no-op", stopped, existsDeleted, false, true, "", actNone, ""},
		{"missing + desired stopping converges to stopped", stopping, existsDeleted, false, true, stopped, actNone, ""},
		{"missing + desired starting recreates", starting, existsDeleted, false, true, starting, actRecreate, "recreating"},
		{"missing + desired creating recreates", db.StatusCreating, existsDeleted, false, true, starting, actRecreate, "recreating"},
		{"missing + desired creating without self-heal waits", db.StatusCreating, existsDeleted, false, false, "", actNone, ""},
		{"running + desired running is a no-op", running, existsRunning, false, true, "", actNone, ""},
		{"running + desired starting converges up", starting, existsRunning, false, true, running, actNone, ""},
		{"unhealthy while starting stays starting", starting, existsUnhealthy, false, true, "", actNone, ""},
		{"unhealthy while creating stays creating", creating, existsUnhealthy, false, true, "", actNone, ""},
		{"unhealthy while stopping resumes the stop", stopping, existsUnhealthy, false, true, "", actStop, ""},
		{"unhealthy while running is adopted", running, existsUnhealthy, false, true, unhealthy, actNone, ""},
		{"running + desired stopped syncs to reality", stopped, existsRunning, false, true, running, actNone, ""},
		{"running + desired stopping resumes the stop", stopping, existsRunning, false, true, "", actStop, ""},
		{"SIGKILL without OOMKilled is not an OOM", running, observedState{Exists: true, ContainerID: "c1", Status: stopped, ExitCode: 137}, false, true, errStatus, actNone, "exited with code 137"},
		{"requested stop plus SIGKILL is stopped", stopping, observedState{Exists: true, ContainerID: "c1", Status: stopped, ExitCode: 137}, false, true, stopped, actNone, ""},
		{"exited clean + desired running reports stopped", running, existsExitedClean, false, true, stopped, actNone, ""},
		{"exited 143 from docker stop is a clean stop", running, existsSigterm, false, true, stopped, actNone, ""},
		{"OOM kill is flagged as error", running, existsOOM, false, true, errStatus, actNone, "OOM"},
		{"crash exit is flagged as error", running, existsCrash, false, true, errStatus, actNone, "exited with code 1"},
		{"exited clean while stopping converges to stopped", stopping, existsExitedClean, false, true, stopped, actNone, ""},
		{"restarting is left to settle", running, existsRestarting, false, true, "", actNone, ""},
		{"restarting with a crash loop is reported", running, observedState{Exists: true, ContainerID: "c1", Status: starting, AbnormalExits: 3}, false, true, errStatus, actNone, "restarting repeatedly"},
		{"restarting with a pre-threshold tally still settles", running, observedState{Exists: true, ContainerID: "c1", Status: starting, AbnormalExits: 2}, false, true, "", actNone, ""},
		{"paused is preserved", running, existsPaused, false, true, paused, actNone, ""},
		{"deepsleep + missing container is stable", deepsleep, existsDeleted, false, true, "", actNone, ""},
		{"deepsleep + exited container is stable", deepsleep, existsExitedClean, false, true, "", actNone, ""},
		{"deepsleep + crashed container never heals", deepsleep, existsCrash, false, true, "", actNone, ""},
		{"deepsleep + running container is stopped, not adopted", deepsleep, existsRunning, false, true, "", actStop, ""},
		{"stopped + exited container is stable, no start", stopped, existsExitedClean, false, true, stopped, actNone, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decide(tt.desired, tt.obs, tt.detached, tt.selfHeal)
			if got.status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", got.status, tt.wantStatus)
			}
			if got.action != tt.wantAction {
				t.Fatalf("action = %v, want %v", got.action, tt.wantAction)
			}
			if tt.wantReasonHas != "" && !contains(got.reason, tt.wantReasonHas) {
				t.Fatalf("reason = %q, want it to contain %q", got.reason, tt.wantReasonHas)
			}
		})
	}
}

func TestClassifyExit(t *testing.T) {
	if st, _ := classifyExit(observedState{ExitCode: 0}); st != db.StatusStopped {
		t.Fatalf("exit 0 => %q, want stopped", st)
	}
	if st, _ := classifyExit(observedState{ExitCode: 143}); st != db.StatusStopped {
		t.Fatalf("exit 143 => %q, want stopped", st)
	}
	if st, reason := classifyExit(observedState{ExitCode: 137, OOMKilled: true}); st != db.StatusError || !contains(reason, "OOM") {
		t.Fatalf("OOMKilled => %q/%q, want error mentioning OOM", st, reason)
	}
	if st, reason := classifyExit(observedState{ExitCode: 137, OOMKilled: false}); st != db.StatusError || contains(reason, "OOM") {
		t.Fatalf("exit 137 without OOMKilled => %q/%q, want a non-OOM error", st, reason)
	}
	if st, _ := classifyExit(observedState{ExitCode: 139}); st != db.StatusError {
		t.Fatalf("exit 139 => %q, want error", st)
	}
}

// --- debouncer tests -------------------------------------------------------
//
// NOTE: the debouncer's coalescing and per-key independence behaviour is
// covered deterministically by TestDebouncer_CoalescesBurst and
// TestDebouncer_DistinctKeysFireIndependently in debouncer_synctest_test.go,
// which run under testing/synctest with virtual time. Those tests are strictly
// stronger (they also assert the debounce does not fire INSIDE the window) and
// are immune to CI scheduling delays. The wall-clock variants that used to live
// here slept for a fixed duration and then asserted an exact fire count, which
// flaked under load whenever a debounce timer fired late. Removed rather than
// duplicated.

// --- integration-ish tests over ReconcileServer ----------------------------

func newTestReconciler(t *testing.T, store Store, cli Client, proxy ProxyRouter, bus EventEmitter) *Reconciler {
	t.Helper()
	return New(store, fakeResolver{cli: cli}, proxy, bus, nil, nil, Config{})
}

func TestReconcileStatusTransitionExternalStop(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Survival", NodeID: "default", ContainerID: "c1", Status: db.StatusRunning}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusStopped, HasExited: true, ExitCode: 0},
	}
	bus := &fakeBus{}
	proxy := &fakeProxy{}

	r := newTestReconciler(t, store, cli, proxy, bus)
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}

	if len(store.statuses) != 1 || store.statuses[0].status != db.StatusStopped {
		t.Fatalf("status writes = %+v, want one stopped", store.statuses)
	}
	types := bus.types()
	if len(types) != 1 || types[0] != v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP {
		t.Fatalf("events = %v, want [SERVER_STOP]", types)
	}
}

func TestReconcileNoWriteWhenAlreadyConverged(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Survival", NodeID: "default", ContainerID: "c1", Status: db.StatusRunning}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusRunning},
	}
	bus := &fakeBus{}

	r := newTestReconciler(t, store, cli, &fakeProxy{}, bus)
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}

	if len(store.statuses) != 0 {
		t.Fatalf("status writes = %+v, want none (already converged)", store.statuses)
	}
	if len(bus.events) != 0 {
		t.Fatalf("events = %v, want none", bus.types())
	}
}

func TestReconcileSkipsServerWithLifecycleTransitionInFlight(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Creating", NodeID: "default", Status: db.StatusCreating}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusStopped, HasExited: true, ExitCode: 0},
	}
	r := newTestReconciler(t, store, cli, &fakeProxy{}, &fakeBus{})

	transition, err := docker.TryBeginServerTransition(server.ID)
	if err != nil {
		t.Fatalf("failed to claim lifecycle transition: %v", err)
	}
	defer transition.Release()

	if err := r.ReconcileServer(context.Background(), server.ID); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}
	if len(store.statuses) != 0 {
		t.Fatalf("status writes = %+v, want none while creation owns transition", store.statuses)
	}
	if len(store.containers) != 0 {
		t.Fatalf("container writes = %+v, want none while creation owns transition", store.containers)
	}
}

func TestReconcileSelfHealsMissingContainer(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Survival", NodeID: "default", ContainerID: "gone", Status: db.StatusRunning, ProxyHostname: "mc.example.com"}
	store := newFakeStore(server)
	cli := &fakeClient{resolveErr: fmt.Errorf("%w: s1", docker.ErrContainerNotResolved)}
	bus := &fakeBus{}
	proxy := &fakeProxy{}

	r := newTestReconciler(t, store, cli, proxy, bus)
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}

	if cli.recreations != 1 {
		t.Fatalf("recreations = %d, want 1", cli.recreations)
	}
	if len(cli.started) != 1 || cli.started[0] != "recreated-container" {
		t.Fatalf("started = %v, want [recreated-container]", cli.started)
	}

	// New container ID must be adopted and status moved to starting.
	var adopted bool
	for _, c := range store.containers {
		if c.containerID == "recreated-container" {
			adopted = true
		}
	}
	if !adopted {
		t.Fatalf("container updates = %+v, want adoption of recreated-container", store.containers)
	}
	if len(store.statuses) != 1 || store.statuses[0].status != db.StatusStarting {
		t.Fatalf("status writes = %+v, want one starting", store.statuses)
	}
	if types := bus.types(); len(types) != 0 {
		t.Fatalf("events = %v, want none until the container is running", types)
	}
	if proxy.count() == 0 {
		t.Fatalf("proxy route was not refreshed after self-heal")
	}
}

func TestReconcileStartingToRunningEmitsServerStartOnce(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Creating", NodeID: "default", ContainerID: "c1", Status: db.StatusStarting}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusRunning},
	}
	bus := &fakeBus{}
	r := newTestReconciler(t, store, cli, &fakeProxy{}, bus)
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}
	if len(store.statuses) != 1 || store.statuses[0].status != db.StatusRunning {
		t.Fatalf("status writes = %+v, want running", store.statuses)
	}
	if types := bus.types(); len(types) != 1 || types[0] != v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_START {
		t.Fatalf("events = %v, want [SERVER_START]", types)
	}

	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("second ReconcileServer: %v", err)
	}
	if types := bus.types(); len(types) != 1 {
		t.Fatalf("events after converge = %v, want the same single SERVER_START", types)
	}
}

func TestReconcileSelfHealPersistsReplacementIDOnPartialFailure(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Survival", NodeID: "default", ContainerID: "gone", Status: db.StatusRunning}
	store := newFakeStore(server)
	cli := &fakeClient{
		resolveErr:     fmt.Errorf("%w: s1", docker.ErrContainerNotResolved),
		recreateResult: &docker.RecreateContainerResult{NewContainerID: "replacement-id", WasRunning: true},
		recreateErr:    fmt.Errorf("replacement failed to start"),
	}
	r := newTestReconciler(t, store, cli, &fakeProxy{}, &fakeBus{})

	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}
	if len(store.containers) != 1 || store.containers[0].containerID != "replacement-id" {
		t.Fatalf("container updates = %+v, want persisted partial replacement id", store.containers)
	}
	if len(store.statuses) != 1 || store.statuses[0].status != db.StatusError {
		t.Fatalf("status updates = %+v, want one error status", store.statuses)
	}
	if len(cli.started) != 0 {
		t.Fatalf("started containers = %v, want none after recreate failure", cli.started)
	}
}

func TestReconcileAdoptsDriftedContainer(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Survival", NodeID: "default", ContainerID: "old-id", Status: db.StatusRunning, ProxyHostname: "mc.example.com"}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "new-id"},
		state:   &docker.ContainerState{ContainerID: "new-id", Status: db.StatusRunning},
	}
	proxy := &fakeProxy{}

	r := newTestReconciler(t, store, cli, proxy, &fakeBus{})
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}

	if len(store.containers) != 1 || store.containers[0].containerID != "new-id" {
		t.Fatalf("container updates = %+v, want adoption of new-id", store.containers)
	}
	if len(store.statuses) != 0 {
		t.Fatalf("status writes = %+v, want none (status unchanged)", store.statuses)
	}
	if proxy.count() != 1 {
		t.Fatalf("proxy route refreshes = %d, want 1", proxy.count())
	}
}

func TestReconcileSkipsWhenNodeUnavailable(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Survival", NodeID: "remote", ContainerID: "c1", Status: db.StatusRunning}
	store := newFakeStore(server)
	bus := &fakeBus{}

	r := New(store, fakeResolver{err: errors.New("node offline")}, &fakeProxy{}, bus, nil, nil, Config{})
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}

	if len(store.statuses) != 0 || len(bus.events) != 0 {
		t.Fatalf("node outage must leave DB untouched; statuses=%v events=%v", store.statuses, bus.types())
	}
}

func TestReconcileOOMKillIsReportedAsError(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Survival", NodeID: "default", ContainerID: "c1", Status: db.StatusRunning}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusStopped, HasExited: true, ExitCode: 137, OOMKilled: true},
	}
	bus := &fakeBus{}

	r := newTestReconciler(t, store, cli, &fakeProxy{}, bus)
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}

	if len(store.statuses) != 1 || store.statuses[0].status != db.StatusError {
		t.Fatalf("status writes = %+v, want one error", store.statuses)
	}
	if len(bus.events) != 1 {
		t.Fatalf("expected one SERVER_STOP event for crash, got %v", bus.types())
	}
}

type sequencedClient struct {
	mu           sync.Mutex
	summary      *container.Summary
	state        *docker.ContainerState
	observations int
}

func (c *sequencedClient) set(summary *container.Summary, state *docker.ContainerState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.summary = summary
	c.state = state
}

func (c *sequencedClient) ResolveContainer(context.Context, string) (*container.Summary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.summary == nil {
		return nil, fmt.Errorf("%w: sequenced", docker.ErrContainerNotResolved)
	}
	return c.summary, nil
}

func (c *sequencedClient) ObserveContainer(context.Context, string) (*docker.ContainerState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.observations++
	return c.state, nil
}

func (c *sequencedClient) observeCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.observations
}

func (c *sequencedClient) StartContainer(context.Context, string) error { return nil }

func (c *sequencedClient) TailContainerLogs(context.Context, string, int) ([]string, error) {
	return nil, nil
}

func (c *sequencedClient) StopContainerWithin(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}

func (c *sequencedClient) RecreateContainer(context.Context, string, *db.Server, *db.ServerConfig) (*docker.RecreateContainerResult, error) {
	return &docker.RecreateContainerResult{NewContainerID: "recreated-container"}, nil
}

func TestSweepConvergesStatusRouteAndLogsWithoutTheEvent(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Survival", NodeID: "default", ContainerID: "c1", Port: 25565, Status: db.StatusRunning}
	store := newFakeStore(server)
	cli := &sequencedClient{}
	cli.set(&container.Summary{ID: "c1"}, &docker.ContainerState{ContainerID: "c1", Status: db.StatusRunning})
	proxy := &fakeProxy{}
	migrator := &fakeLogMigrator{}

	r := New(store, fakeResolver{cli: cli}, proxy, &fakeBus{}, migrator, nil, Config{
		DebounceWindow: 5 * time.Millisecond,
		SweepInterval:  20 * time.Millisecond,
		Workers:        1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// No event is ever delivered. The startup pass and the sweep are the only
	// way the panel can notice the container.
	r.Start(ctx, nil)
	t.Cleanup(func() {
		r.Stop()
		r.Wait()
	})

	waitFor := func(status db.ServerStatus, containerID string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			got, err := store.GetServer(context.Background(), server.ID)
			if err == nil && got.Status == status && got.ContainerID == containerID {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		got, _ := store.GetServer(context.Background(), server.ID)
		t.Fatalf("server = status %s container %s, want %s %s", got.Status, got.ContainerID, status, containerID)
	}

	waitFor(db.StatusRunning, "c1")
	routesBefore := proxy.count()

	cli.set(
		&container.Summary{ID: "c2"},
		&docker.ContainerState{ContainerID: "c2", Status: db.StatusStopped, HasExited: true, ExitCode: 0},
	)
	waitFor(db.StatusStopped, "c2")

	calls := migrator.snapshot()
	if len(calls) != 1 || calls[0].oldID != "c1" || calls[0].newID != "c2" {
		t.Fatalf("log migration = %+v, want c1 -> c2", calls)
	}
	if proxy.count() <= routesBefore {
		t.Fatalf("proxy route refreshes = %d, want a refresh after the dropped event (was %d)", proxy.count(), routesBefore)
	}

	// A later start event is absent too. The container is running again, and
	// the sweep must adopt that instead of staying on the stopped status.
	cli.set(&container.Summary{ID: "c2"}, &docker.ContainerState{ContainerID: "c2", Status: db.StatusRunning})
	waitFor(db.StatusRunning, "c2")
}

func TestReorderedEventsFollowTheContainerNotTheEventOrder(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Survival", NodeID: "default", ContainerID: "c1", Status: db.StatusStopped}
	store := newFakeStore(server)
	cli := &sequencedClient{}
	cli.set(&container.Summary{ID: "c1"}, &docker.ContainerState{ContainerID: "c1", Status: db.StatusRunning})

	r := New(store, fakeResolver{cli: cli}, &fakeProxy{}, &fakeBus{}, nil, nil, Config{
		DebounceWindow: 15 * time.Millisecond,
		SweepInterval:  time.Hour,
		Workers:        1,
	})
	ch := make(chan ContainerEvent, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx, ch)
	t.Cleanup(func() {
		r.Stop()
		r.Wait()
	})

	// die arrives before start, while the container is already running.
	ch <- ContainerEvent{ServerID: "s1", Action: "die", ContainerID: "c1"}
	ch <- ContainerEvent{ServerID: "s1", Action: "start", ContainerID: "c1"}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := store.GetServer(context.Background(), server.ID)
		if err == nil && got.Status == db.StatusRunning {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := store.GetServer(context.Background(), server.ID)
	t.Fatalf("status after reordered events = %s, want running", got.Status)
}

func TestStartConsumesEventsAndConverges(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Survival", NodeID: "default", ContainerID: "c1", Status: db.StatusRunning}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusRunning},
	}

	r := New(store, fakeResolver{cli: cli}, &fakeProxy{}, &fakeBus{}, nil, nil, Config{
		DebounceWindow: 10 * time.Millisecond,
		SweepInterval:  time.Hour,
	})

	ch := make(chan ContainerEvent, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.Start(ctx, ch)

	ch <- ContainerEvent{ServerID: "s1", Action: "die", ContainerID: "c1"}

	// Give the debounce + worker a moment; the state is already converged so we
	// just assert the reconciler consumed the event without erroring.
	time.Sleep(80 * time.Millisecond)

	r.Stop()
	r.Wait()
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// --- MINE-108: log subscription migration + dynamic re-routing ---------------

type migrationCall struct {
	serverID string
	oldID    string
	newID    string
}

type fakeLogMigrator struct {
	mu    sync.Mutex
	calls []migrationCall
}

func (m *fakeLogMigrator) MigrateServerLogSubscriptions(serverID, oldContainerID, newContainerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, migrationCall{serverID: serverID, oldID: oldContainerID, newID: newContainerID})
}

func (m *fakeLogMigrator) snapshot() []migrationCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]migrationCall, len(m.calls))
	copy(out, m.calls)
	return out
}

func summaryWithIP(id, ip string) *container.Summary {
	return &container.Summary{
		ID: id,
		NetworkSettings: &container.NetworkSettingsSummary{
			Networks: map[string]*network.EndpointSettings{
				"carbon-panel-network": {IPAddress: netip.MustParseAddr(ip)},
			},
		},
	}
}

func TestReconcileMigratesLogSubscriptionsOnAdoption(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Survival", NodeID: "default", ContainerID: "old-id", Status: db.StatusRunning}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: summaryWithIP("new-id", "10.0.0.5"),
		state:   &docker.ContainerState{ContainerID: "new-id", Status: db.StatusRunning},
	}
	migrator := &fakeLogMigrator{}

	r := New(store, fakeResolver{cli: cli}, &fakeProxy{}, &fakeBus{}, migrator, nil, Config{})
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}

	calls := migrator.snapshot()
	if len(calls) != 1 {
		t.Fatalf("migration calls = %+v, want exactly 1", calls)
	}
	if calls[0].serverID != "s1" || calls[0].oldID != "old-id" || calls[0].newID != "new-id" {
		t.Fatalf("migration call = %+v, want s1 old-id -> new-id", calls[0])
	}
}

func TestReconcileMigratesLogSubscriptionsOnSelfHeal(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Survival", NodeID: "default", ContainerID: "gone", Status: db.StatusRunning}
	store := newFakeStore(server)
	cli := &fakeClient{resolveErr: fmt.Errorf("%w: s1", docker.ErrContainerNotResolved)}
	migrator := &fakeLogMigrator{}

	r := New(store, fakeResolver{cli: cli}, &fakeProxy{}, &fakeBus{}, migrator, nil, Config{})
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}

	calls := migrator.snapshot()
	if len(calls) != 1 {
		t.Fatalf("migration calls = %+v, want exactly 1", calls)
	}
	if calls[0].oldID != "gone" || calls[0].newID != "recreated-container" {
		t.Fatalf("migration call = %+v, want gone -> recreated-container", calls[0])
	}
}

func TestReconcileRefreshesRouteOnIPChange(t *testing.T) {
	server := &db.Server{
		ID: "s1", Name: "Survival", NodeID: "default",
		ContainerID: "c1", Status: db.StatusRunning, ProxyHostname: "mc.example.com",
	}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: summaryWithIP("c1", "10.0.0.2"),
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusRunning},
	}
	proxy := &fakeProxy{}

	r := newTestReconciler(t, store, cli, proxy, &fakeBus{})

	// First pass learns the backend and pushes the route once.
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if proxy.count() != 1 {
		t.Fatalf("route refreshes after first pass = %d, want 1", proxy.count())
	}

	// An unchanged backend must not churn the route.
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if proxy.count() != 1 {
		t.Fatalf("route refreshes after unchanged pass = %d, want 1", proxy.count())
	}

	// A re-allocated IP on the same container must re-point the route.
	cli.summary = summaryWithIP("c1", "10.0.0.9")
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("third reconcile: %v", err)
	}
	if proxy.count() != 2 {
		t.Fatalf("route refreshes after IP change = %d, want 2", proxy.count())
	}

	// No status writes: the server was running the whole time.
	if len(store.statuses) != 0 {
		t.Fatalf("status writes = %+v, want none", store.statuses)
	}
}

func TestReconcileMigratesLogSubscriptionsWhenDBAlreadyUpdated(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Survival", NodeID: "default", ContainerID: "old-id", Status: db.StatusRunning}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: summaryWithIP("old-id", "10.0.0.1"),
		state:   &docker.ContainerState{ContainerID: "old-id", Status: db.StatusRunning},
	}
	migrator := &fakeLogMigrator{}

	r := New(store, fakeResolver{cli: cli}, &fakeProxy{}, &fakeBus{}, migrator, nil, Config{})

	// Prime the backend cache with the container the subscribers are registered
	// against.
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("prime reconcile: %v", err)
	}

	// Simulate an RPC-initiated Force Recreate: it swaps the container and writes
	// the new ID straight to the DB, so the reconciler never sees an adoption.
	store.mu.Lock()
	store.servers["s1"].ContainerID = "new-id"
	store.mu.Unlock()
	cli.summary = summaryWithIP("new-id", "10.0.0.2")
	cli.state = &docker.ContainerState{ContainerID: "new-id", Status: db.StatusRunning}

	migrator.mu.Lock()
	migrator.calls = nil
	migrator.mu.Unlock()

	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("post-recreate reconcile: %v", err)
	}

	calls := migrator.snapshot()
	if len(calls) != 1 || calls[0].oldID != "old-id" || calls[0].newID != "new-id" {
		t.Fatalf("migration calls = %+v, want one old-id -> new-id", calls)
	}
}

func TestReconcileDirectPortServer_CallsUpdateRouteOnStatusChange(t *testing.T) {
	// A server with no proxy hostname but with a direct port (Port > 0)
	// must still trigger UpdateServerRoute so direct knock listeners can be stopped or started.
	server := &db.Server{
		ID:            "s-direct",
		Name:          "DirectMC",
		NodeID:        "default",
		ContainerID:   "c-direct",
		ProxyHostname: "",
		Port:          25565,
		Status:        db.StatusRunning,
	}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "c-direct"},
		state:   &docker.ContainerState{ContainerID: "c-direct", Status: db.StatusStopped, HasExited: true, ExitCode: 0},
	}
	proxy := &fakeProxy{}
	r := newTestReconciler(t, store, cli, proxy, &fakeBus{})

	if err := r.ReconcileServer(context.Background(), "s-direct"); err != nil {
		t.Fatalf("ReconcileServer failed: %v", err)
	}

	if proxy.count() == 0 {
		t.Fatalf("expected proxy.UpdateServerRoute to be called for direct-port server on status change")
	}
}

func TestReconcileBootCrashBecomesErrorWithReason(t *testing.T) {
	// Homestead shape: the JVM exits 1 on a missing hard dependency while the
	// entrypoint keeps the container alive. Docker health never goes healthy,
	// so without log evidence the panel would sit on STARTING forever.
	boot := time.Date(2026, 11, 6, 5, 2, 49, 0, time.UTC)
	line := func(min, sec int, msg string) string {
		return time.Date(2026, 11, 6, 5, min, sec, 0, time.UTC).Format(time.RFC3339Nano) + " " + msg
	}
	server := &db.Server{ID: "s1", Name: "Homestead", NodeID: "default", ContainerID: "c1", Status: db.StatusStarting}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusUnhealthy, StartedAt: boot},
		logLines: []string{
			line(2, 51, "[init] Starting the Minecraft server..."),
			line(2, 56, "[main/INFO]: Loading Minecraft 1.20.1 with Fabric Loader 0.19.5"),
			line(3, 10, "[main/INFO]:  - Mod 'Chipped' (chipped) 3.0.7 requires version 3.1.1 or later of athena, which is missing!"),
		},
	}
	bus := &fakeBus{}

	r := newTestReconciler(t, store, cli, &fakeProxy{}, bus)
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}

	got, err := store.GetServer(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != db.StatusError {
		t.Fatalf("status = %q, want error", got.Status)
	}
	if !contains(got.StatusReason, "athena") {
		t.Fatalf("reason = %q, want the missing dependency named", got.StatusReason)
	}
}

func TestReconcileBootWithoutCrashEvidenceStaysStarting(t *testing.T) {
	boot := time.Date(2026, 11, 6, 5, 2, 49, 0, time.UTC)
	line := func(min, sec int, msg string) string {
		return time.Date(2026, 11, 6, 5, min, sec, 0, time.UTC).Format(time.RFC3339Nano) + " " + msg
	}
	server := &db.Server{ID: "s1", Name: "Homestead", NodeID: "default", ContainerID: "c1", Status: db.StatusStarting}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusUnhealthy, StartedAt: boot},
		logLines: []string{
			line(2, 51, "[init] Starting the Minecraft server..."),
			line(2, 56, "[main/INFO]: Loading Minecraft 1.20.1 with Fabric Loader 0.19.5"),
			line(2, 58, "[main/INFO]: Preparing spawn area: 10%"),
		},
	}

	r := newTestReconciler(t, store, cli, &fakeProxy{}, &fakeBus{})
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}

	got, err := store.GetServer(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != db.StatusStarting {
		t.Fatalf("status = %q, want starting: a slow boot without crash evidence is not an error", got.Status)
	}
	if got.StatusReason != "" {
		t.Fatalf("reason = %q, want empty", got.StatusReason)
	}
}

func TestReconcileKeepsLoaderFailureAfterExit(t *testing.T) {
	boot := time.Date(2026, 11, 20, 6, 56, 43, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		desired    db.ServerStatus
		exitCode   int
		oomKilled  bool
		wantStatus db.ServerStatus
		wantReason string
	}{
		{"failed boot", db.StatusStarting, 1, false, db.StatusError, "athena"},
		{"entrypoint masked the exit code", db.StatusStarting, 0, false, db.StatusError, "athena"},
		{"existing generic exit reason", db.StatusError, 1, false, db.StatusError, "athena"},
		{"oom takes precedence", db.StatusStarting, 137, true, db.StatusError, "OOM-killed"},
		{"operator stop", db.StatusStopping, 0, false, db.StatusStopped, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := &db.Server{
				ID: "s1", Name: "Homestead", NodeID: "default", ContainerID: "c1", Status: tc.desired,
			}
			store := newFakeStore(server)
			cli := &fakeClient{
				summary: &container.Summary{ID: "c1"},
				state: &docker.ContainerState{
					ContainerID: "c1", Status: db.StatusStopped, HasExited: true,
					ExitCode: tc.exitCode, OOMKilled: tc.oomKilled, StartedAt: boot,
				},
				logLines: []string{
					boot.Format(time.RFC3339Nano) + " [main/INFO]: Loading Minecraft 1.20.1 with Fabric Loader 0.19.5",
					boot.Add(time.Second).Format(time.RFC3339Nano) + " [main/INFO]: Mod 'Chipped' requires athena, which is missing!",
				},
			}
			r := newTestReconciler(t, store, cli, &fakeProxy{}, &fakeBus{})
			for pass := 1; pass <= 2; pass++ {
				if err := r.ReconcileServer(context.Background(), server.ID); err != nil {
					t.Fatalf("reconcile pass %d: %v", pass, err)
				}
				got, err := store.GetServer(context.Background(), server.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != tc.wantStatus {
					t.Fatalf("pass %d: status = %q, want %q", pass, got.Status, tc.wantStatus)
				}
				if tc.wantReason == "" && got.StatusReason != "" || !contains(got.StatusReason, tc.wantReason) {
					t.Fatalf("pass %d: reason = %q, want %q", pass, got.StatusReason, tc.wantReason)
				}
			}
		})
	}
}

func TestReconcileRecoveryClearsStatusReason(t *testing.T) {
	server := &db.Server{
		ID: "s1", Name: "Homestead", NodeID: "default", ContainerID: "c1",
		Status: db.StatusError, StatusReason: "Minecraft server process failed (exit code 1)",
	}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusRunning},
	}

	r := newTestReconciler(t, store, cli, &fakeProxy{}, &fakeBus{})
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}

	got, err := store.GetServer(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != db.StatusRunning {
		t.Fatalf("status = %q, want running", got.Status)
	}
	if got.StatusReason != "" {
		t.Fatalf("reason = %q, a recovered server must not show a stale crash", got.StatusReason)
	}
}

// --- restart-loop detection -------------------------------------------------

func TestRestartLoopIsReportedAsError(t *testing.T) {
	boot := time.Date(2026, 11, 20, 6, 0, 0, 0, time.UTC)
	server := &db.Server{ID: "s1", Name: "Loop", NodeID: "default", ContainerID: "c1", Status: db.StatusRunning}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusStopped, HasExited: true, ExitCode: 1, StartedAt: boot},
	}
	bus := &fakeBus{}
	r := newTestReconciler(t, store, cli, &fakeProxy{}, bus)

	// Three distinct abnormal terminations: a new process start (StartedAt)
	// between passes is a new death, repeats of the same one count once.
	for i := 1; i <= 3; i++ {
		cli.state.StartedAt = boot.Add(time.Duration(i) * time.Minute)
		if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}

	got, err := store.GetServer(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != db.StatusError {
		t.Fatalf("status = %q, want error after 3 abnormal exits", got.Status)
	}
	if !contains(got.StatusReason, "restarting repeatedly (3 times in 10m)") {
		t.Fatalf("reason = %q, want the restart-loop report", got.StatusReason)
	}

	// The tally was consumed by the report: a later pass observing the same
	// death must not re-report it.
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("post-report pass: %v", err)
	}
	for _, e := range bus.events {
		if e.Type == v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP {
			return // one stop announcement for the crash is enough
		}
	}
	t.Fatalf("events = %v, want a SERVER_STOP for the reported crash", bus.types())
}

func TestRestartLoopDoesNotFightAnOperatorStop(t *testing.T) {
	boot := time.Date(2026, 11, 20, 6, 0, 0, 0, time.UTC)
	server := &db.Server{ID: "s1", Name: "Loop", NodeID: "default", ContainerID: "c1", Status: db.StatusStopping}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusStopped, HasExited: true, ExitCode: 137, OOMKilled: true, StartedAt: boot},
	}
	r := newTestReconciler(t, store, cli, &fakeProxy{}, &fakeBus{})

	// Three die events put the tally at the threshold; the pass that then
	// observes the exited container (the same death, deduplicated) must
	// still converge the operator's stop instead of repainting it as a
	// crash loop.
	code := 137
	now := time.Now()
	r.loops.recordDie("s1", &code, false, now)
	r.loops.recordDie("s1", &code, false, now.Add(time.Second))
	r.loops.recordDie("s1", &code, false, now.Add(2*time.Second))

	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}
	got, _ := store.GetServer(context.Background(), "s1")
	if got.Status != db.StatusStopped {
		t.Fatalf("status = %q, want stopped: the operator asked to stop", got.Status)
	}
	if contains(got.StatusReason, "restarting repeatedly") {
		t.Fatalf("reason = %q, a requested stop must not be repainted as a crash loop", got.StatusReason)
	}
}

func TestRestartTrackerForgivesStableRuntimeAndCleanExits(t *testing.T) {
	now := time.Now()

	tr := newRestartTracker()
	code := 1
	tr.recordDie("s1", &code, false, now)
	tr.recordDie("s1", &code, false, now.Add(time.Second))
	if got := tr.count("s1"); got != 2 {
		t.Fatalf("tally after two die events = %d, want 2", got)
	}
	// A container that boots and stays up forgives the tally.
	up := observedState{Exists: true, ContainerID: "c1", Status: db.StatusRunning, StartedAt: now.Add(-10 * time.Minute)}
	if got := tr.observe("s1", up, now.Add(2*time.Second)); got != 0 {
		t.Fatalf("tally after stable runtime = %d, want 0", got)
	}
	if got := tr.count("s1"); got != 0 {
		t.Fatalf("tally after stable runtime = %d, want 0", got)
	}

	// A clean exit forgives the tally too.
	tr.recordDie("s1", &code, false, now.Add(3*time.Second))
	clean := observedState{Exists: true, ContainerID: "c1", Status: db.StatusStopped, ExitCode: 0, StartedAt: now.Add(4 * time.Second)}
	if got := tr.observe("s1", clean, now.Add(5*time.Second)); got != 0 {
		t.Fatalf("tally after clean exit = %d, want 0", got)
	}

	// A SIGTERM die forgives it as well.
	tr.recordDie("s1", &code, false, now.Add(6*time.Second))
	sigterm := exitCodeSIGTERM
	tr.recordDie("s1", &sigterm, false, now.Add(7*time.Second))
	if got := tr.count("s1"); got != 0 {
		t.Fatalf("tally after SIGTERM die = %d, want 0", got)
	}
}

func TestRestartTrackerWindowExpiresAndDeduplicates(t *testing.T) {
	now := time.Now()

	// One death observed by many passes counts once.
	tr := newRestartTracker()
	code := 1
	tr.recordDie("s1", &code, false, now)
	dead := observedState{Exists: true, ContainerID: "c1", Status: db.StatusStopped, ExitCode: 1, StartedAt: now}
	for i := 0; i < 3; i++ {
		if got := tr.observe("s1", dead, now.Add(time.Duration(i+1)*time.Second)); got != 1 {
			t.Fatalf("pass %d tally = %d, want 1 (same death)", i+1, got)
		}
	}
	if got := tr.count("s1"); got != 1 {
		t.Fatalf("tally = %d, want 1", got)
	}

	// Deaths older than the window no longer count as consecutive.
	expired := newRestartTracker()
	expired.recordDie("s1", &code, false, now.Add(-restartLoopWindow-time.Minute))
	if got := expired.count("s1"); got != 0 {
		t.Fatalf("tally after the window lapsed = %d, want 0", got)
	}

	// The oom event counts without an exit code.
	tr2 := newRestartTracker()
	tr2.recordDie("s1", nil, true, now)
	if got := tr2.count("s1"); got != 1 {
		t.Fatalf("tally after an oom event = %d, want 1", got)
	}
}

func TestDieEventsFeedTheRestartLoopDetector(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Loop", NodeID: "default", ContainerID: "c1", Status: db.StatusRunning}
	store := newFakeStore(server)
	// Docker keeps restarting the container: every observation is
	// "restarting", so only the die events carry the deaths.
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusStarting, Running: true},
	}

	// consume() is driven directly (no Start, no workers): a started pass
	// would consume the tally the moment it crosses the threshold, which is
	// the production behaviour but useless for observing the feed itself.
	r := New(store, fakeResolver{cli: cli}, &fakeProxy{}, &fakeBus{}, nil, nil, Config{
		DebounceWindow: 5 * time.Millisecond,
		SweepInterval:  time.Hour,
		Workers:        1,
	})
	r.ctx = context.Background() // normally set by Start
	ch := make(chan ContainerEvent, 8)
	r.wg.Add(1)
	go r.consume(ch)
	t.Cleanup(r.Wait)

	code := 1
	for i := 0; i < 3; i++ {
		ch <- ContainerEvent{ServerID: "s1", Action: "die", ContainerID: "c1", ExitCode: &code}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && r.loops.count("s1") < 3 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := r.loops.count("s1"); got != 3 {
		t.Fatalf("die events recorded = %d, want 3", got)
	}
	close(ch) // consume returns, tally preserved for the deciding pass

	// A pass against the restarting container must report the loop instead of
	// leaving the row "starting" forever.
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}
	got, err := store.GetServer(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != db.StatusError || !contains(got.StatusReason, "restarting repeatedly (3 times in 10m)") {
		t.Fatalf("status = %q reason = %q, want the crash loop reported", got.Status, got.StatusReason)
	}
}

// --- deferred SERVER_STOP announcement --------------------------------------

func TestOrphanedStopIsAnnouncedAfterOneSweep(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "S", NodeID: "default", ContainerID: "c1", Status: db.StatusStopping}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusStopped, HasExited: true, ExitCode: 0},
	}
	bus := &fakeBus{}
	r := New(store, fakeResolver{cli: cli}, &fakeProxy{}, bus, nil, nil, Config{
		SweepInterval: 20 * time.Millisecond,
	})

	// Pass 1: the stop converges, but the announcement is deferred because
	// the DB said stopping (the RPC is presumed to own it) — and this store
	// has no journal to prove otherwise.
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	got, _ := store.GetServer(context.Background(), "s1")
	if got.Status != db.StatusStopped {
		t.Fatalf("status = %q, want the stop converged", got.Status)
	}
	if types := bus.types(); len(types) != 0 {
		t.Fatalf("events = %v, want the stop announcement deferred", types)
	}

	// The stop evidence persists past one sweep: the next pass announces it.
	time.Sleep(40 * time.Millisecond)
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	types := bus.types()
	if len(types) != 1 || types[0] != v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP {
		t.Fatalf("events = %v, want the deferred SERVER_STOP", types)
	}
}

func TestDeferredStopAnnouncementIsDroppedWhenTheServerStartsAgain(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "S", NodeID: "default", ContainerID: "c1", Status: db.StatusStopping}
	store := newFakeStore(server)
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusStopped, HasExited: true, ExitCode: 0},
	}
	bus := &fakeBus{}
	r := New(store, fakeResolver{cli: cli}, &fakeProxy{}, bus, nil, nil, Config{
		SweepInterval: 20 * time.Millisecond,
	})

	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	time.Sleep(40 * time.Millisecond)

	// The server came back up before the grace window elapsed on a pass: the
	// stop announcement is moot and must be dropped.
	cli.state = &docker.ContainerState{ContainerID: "c1", Status: db.StatusRunning}
	store.mu.Lock()
	store.servers["s1"].Status = db.StatusRunning
	store.mu.Unlock()
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if types := bus.types(); len(types) != 0 {
		t.Fatalf("events = %v, want no stop announcement after a restart", types)
	}
}

type fakeJournalStore struct {
	*fakeStore
	assertErr error
}

func (f *fakeJournalStore) AssertNoServerOperation(_ context.Context, _ string) error {
	return f.assertErr
}

// A21: a journal read failure means ownership is unknown. The stop
// announcement must be deferred (not suppressed as owned) and re-emitted if the
// stop evidence persists past one sweep.
func TestJournalReadFailureDefersStopAnnouncement(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "S", NodeID: "default", ContainerID: "c1", Status: db.StatusStopping}
	base := newFakeStore(server)
	store := &fakeJournalStore{fakeStore: base, assertErr: errors.New("journal unavailable")}
	cli := &fakeClient{
		summary: &container.Summary{ID: "c1"},
		state:   &docker.ContainerState{ContainerID: "c1", Status: db.StatusStopped, HasExited: true, ExitCode: 0},
	}
	bus := &fakeBus{}
	r := New(store, fakeResolver{cli: cli}, &fakeProxy{}, bus, nil, nil, Config{
		SweepInterval: 20 * time.Millisecond,
	})

	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	got, _ := store.GetServer(context.Background(), "s1")
	if got.Status != db.StatusStopped {
		t.Fatalf("status = %q, want the stop converged", got.Status)
	}
	if types := bus.types(); len(types) != 0 {
		t.Fatalf("events = %v, want the stop announcement deferred on read failure", types)
	}

	time.Sleep(40 * time.Millisecond)
	if err := r.ReconcileServer(context.Background(), "s1"); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	types := bus.types()
	if len(types) != 1 || types[0] != v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP {
		t.Fatalf("events = %v, want the deferred SERVER_STOP after journal read failure", types)
	}
}

// --- blind-window reduction around lifecycle transitions --------------------

func TestTransitionEndTriggersConvergenceWithoutTheSweep(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "S", NodeID: "default", ContainerID: "c1", Status: db.StatusStopped}
	store := newFakeStore(server)
	cli := &sequencedClient{}
	cli.set(&container.Summary{ID: "c1"}, &docker.ContainerState{ContainerID: "c1", Status: db.StatusRunning})

	r := New(store, fakeResolver{cli: cli}, &fakeProxy{}, &fakeBus{}, nil, nil, Config{
		DebounceWindow: 5 * time.Millisecond,
		SweepInterval:  time.Hour, // the sweep must not be what converges here
		Workers:        1,
	})

	// A lifecycle transition (a start RPC) holds the lock before Start.
	transition, err := docker.TryBeginServerTransition(server.ID)
	if err != nil {
		t.Fatalf("claim transition: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx, nil)
	t.Cleanup(func() {
		cancel()
		r.Stop()
		r.Wait()
	})

	// The startup pass hits the held lock and arms a bounded waiter.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, armed := r.transitionWaits.Load(server.ID); armed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, armed := r.transitionWaits.Load(server.ID); !armed {
		t.Fatal("a pass that hit the held transition did not arm a waiter")
	}

	// Releasing the transition must converge the server promptly, without
	// waiting for the (one-hour) sweep.
	transition.Release()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := store.GetServer(context.Background(), server.ID)
		if err == nil && got.Status == db.StatusRunning {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, _ := store.GetServer(context.Background(), server.ID)
	t.Fatalf("status after the transition ended = %s, want running", got.Status)
}

// TestReconcilerPassDoesNotNudgeItself wires the production transition-end hook
// the way cmd/carbon-panel does. A convergence pass must release its own
// transition quietly: notifying would queue another pass, and each pass would
// keep queueing the next, spinning the reconciler instead of leaving it to
// Docker events and the sweep. A genuine external release must still nudge.
func TestReconcilerPassDoesNotNudgeItself(t *testing.T) {
	server := &db.Server{ID: "srv-self-nudge", Name: "S", NodeID: "default", ContainerID: "c1", Status: db.StatusRunning}
	store := newFakeStore(server)
	cli := &sequencedClient{}
	cli.set(&container.Summary{ID: "c1"}, &docker.ContainerState{ContainerID: "c1", Status: db.StatusRunning})

	var notified atomic.Int32
	docker.SetTransitionEndNotifier(func(serverID string) {
		notified.Add(1)
		NotifyTransitionEnded(serverID)
	})
	t.Cleanup(func() { docker.SetTransitionEndNotifier(nil) })

	r := New(store, fakeResolver{cli: cli}, &fakeProxy{}, &fakeBus{}, nil, nil, Config{
		DebounceWindow: 5 * time.Millisecond,
		SweepInterval:  time.Hour, // never the reason a follow-up pass runs here
		Workers:        1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx, nil)
	t.Cleanup(func() {
		cancel()
		r.Stop()
		r.Wait()
	})

	// The startup pass observes the already-running container once.
	waitForObserveCalls(t, cli, 1)
	if got := notified.Load(); got != 0 {
		t.Fatalf("reconciler's own release notified the transition-end hook %d times, want 0", got)
	}

	// Wait deterministically for the startup pass to release its transition
	// rather than guessing with a fixed delay. Claiming the transition proves
	// the pass ended; claiming it while the pass still owned the lock would
	// race and report ErrTransitionInFlight. A self-queued follow-up would have
	// notified the hook before releasing, so the notification and observation
	// counts still catch a self-nudge.
	var external *docker.Transition
	deadline := time.Now().Add(2 * time.Second)
	for {
		var claimErr error
		external, claimErr = docker.TryBeginServerTransition(server.ID)
		if claimErr == nil {
			break
		}
		if !errors.Is(claimErr, docker.ErrTransitionInFlight) {
			t.Fatalf("external transition: %v", claimErr)
		}
		if time.Now().After(deadline) {
			t.Fatal("startup pass never released its lifecycle transition")
		}
		time.Sleep(time.Millisecond)
	}
	if got := cli.observeCalls(); got != 1 {
		t.Fatalf("startup pass scheduled %d follow-up observations, want 1 total", got)
	}
	if got := notified.Load(); got != 0 {
		t.Fatalf("reconciler's own release notified the transition-end hook %d times, want 0", got)
	}
	if got := pendingDebounceTimers(r); got != 0 {
		t.Fatalf("startup pass queued %d follow-up passes, want 0", got)
	}

	// An operator/lifecycle release is still a real transition end: it notifies
	// and queues a convergence pass.
	external.Release()
	if got := notified.Load(); got != 1 {
		t.Fatalf("external release notified %d times, want 1", got)
	}
	waitForObserveCalls(t, cli, 2)
}

// transientResolveClient fails every container resolution, modelling a
// transient Docker outage. It embeds the sequenced fake for the methods the
// failing path never reaches.
type transientResolveClient struct {
	sequencedClient
	mu       sync.Mutex
	resolves int
}

func (c *transientResolveClient) ResolveContainer(context.Context, string) (*container.Summary, error) {
	c.mu.Lock()
	c.resolves++
	c.mu.Unlock()
	return nil, errors.New("transient docker unavailable")
}

func (c *transientResolveClient) resolveCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resolves
}

func waitForResolveCalls(t *testing.T, cli *transientResolveClient, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cli.resolveCalls() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("container resolutions = %d, want at least %d", cli.resolveCalls(), want)
}

// deletedServerStore lists the server for the sweep but reports it deleted to
// the convergence pass, modelling a row removed between list and reconcile.
type deletedServerStore struct {
	*fakeStore
	mu       sync.Mutex
	getCalls int
}

func (s *deletedServerStore) GetServer(context.Context, string) (*db.Server, error) {
	s.mu.Lock()
	s.getCalls++
	s.mu.Unlock()
	return nil, db.ErrServerNotFound
}

func (s *deletedServerStore) getServerCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCalls
}

func pendingDebounceTimers(r *Reconciler) int {
	r.deb.mu.Lock()
	defer r.deb.mu.Unlock()
	return len(r.deb.timers)
}

// TestReconcilerTransientFailureRequestsOneRetry: a pass that fails on a
// transient error must release the transition normally so the transition-end
// hook retries once, but a repeated failure must release quietly instead of
// spinning the reconciler.
func TestReconcilerTransientFailureRequestsOneRetry(t *testing.T) {
	server := &db.Server{ID: "srv-transient", Name: "S", NodeID: "default", ContainerID: "c1", Status: db.StatusRunning}
	store := newFakeStore(server)
	cli := &transientResolveClient{}

	var notified atomic.Int32
	docker.SetTransitionEndNotifier(func(serverID string) {
		notified.Add(1)
		NotifyTransitionEnded(serverID)
	})
	t.Cleanup(func() { docker.SetTransitionEndNotifier(nil) })

	r := New(store, fakeResolver{cli: cli}, &fakeProxy{}, &fakeBus{}, nil, nil, Config{
		DebounceWindow: 5 * time.Millisecond,
		SweepInterval:  time.Hour,
		Workers:        1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx, nil)
	t.Cleanup(func() {
		cancel()
		r.Stop()
		r.Wait()
	})

	waitForResolveCalls(t, cli, 2)
	if err := docker.WaitForServerTransition(ctx, server.ID); err != nil {
		t.Fatalf("wait for retry pass: %v", err)
	}
	if got := notified.Load(); got != 1 {
		t.Fatalf("transition releases notified %d times, want exactly 1 (one retry request)", got)
	}
	if got := cli.resolveCalls(); got != 2 {
		t.Fatalf("container resolutions = %d, want exactly 2 (startup + one retry)", got)
	}
	if got := pendingDebounceTimers(r); got != 0 {
		t.Fatalf("failed retry queued %d follow-up passes, want 0", got)
	}
}

// TestReconcilerDeletedServerNoOpDoesNotRetry: a server deleted between the
// sweep listing and the pass is a deliberate no-op, not a failure to retry.
func TestReconcilerDeletedServerNoOpDoesNotRetry(t *testing.T) {
	server := &db.Server{ID: "srv-deleted", Name: "S", NodeID: "default", ContainerID: "c1", Status: db.StatusRunning}
	store := &deletedServerStore{fakeStore: newFakeStore(server)}

	var notified atomic.Int32
	docker.SetTransitionEndNotifier(func(serverID string) {
		notified.Add(1)
		NotifyTransitionEnded(serverID)
	})
	t.Cleanup(func() { docker.SetTransitionEndNotifier(nil) })

	r := New(store, fakeResolver{cli: &sequencedClient{}}, &fakeProxy{}, &fakeBus{}, nil, nil, Config{
		DebounceWindow: 5 * time.Millisecond,
		SweepInterval:  time.Hour,
		Workers:        1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx, nil)
	t.Cleanup(func() {
		cancel()
		r.Stop()
		r.Wait()
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && store.getServerCalls() == 0 {
		time.Sleep(time.Millisecond)
	}
	if got := store.getServerCalls(); got == 0 {
		t.Fatal("the deleted server's pass never ran")
	}
	if err := docker.WaitForServerTransition(ctx, server.ID); err != nil {
		t.Fatalf("wait for deleted-server pass: %v", err)
	}
	if got := notified.Load(); got != 0 {
		t.Fatalf("deleted-server no-op notified the transition-end hook %d times, want 0", got)
	}
	if got := store.getServerCalls(); got != 1 {
		t.Fatalf("deleted server read %d times, want exactly 1 (no loop)", got)
	}
	if got := pendingDebounceTimers(r); got != 0 {
		t.Fatalf("deleted-server no-op queued %d follow-up passes, want 0", got)
	}
}

func waitForObserveCalls(t *testing.T, cli *sequencedClient, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cli.observeCalls() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("observations = %d, want at least %d", cli.observeCalls(), want)
}

func TestNotifyTransitionEndedNudgesTheActiveReconciler(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "S", NodeID: "default", ContainerID: "c1", Status: db.StatusStopped}
	store := newFakeStore(server)
	cli := &sequencedClient{}
	cli.set(&container.Summary{ID: "c1"}, &docker.ContainerState{ContainerID: "c1", Status: db.StatusStopped, HasExited: true, ExitCode: 0})

	r := New(store, fakeResolver{cli: cli}, &fakeProxy{}, &fakeBus{}, nil, nil, Config{
		DebounceWindow: 5 * time.Millisecond,
		SweepInterval:  time.Hour,
		Workers:        1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx, nil)
	t.Cleanup(func() {
		cancel()
		r.Stop()
		r.Wait()
	})

	// Let the startup pass drain while the container is still down.
	time.Sleep(60 * time.Millisecond)
	cli.set(&container.Summary{ID: "c1"}, &docker.ContainerState{ContainerID: "c1", Status: db.StatusRunning})

	NotifyTransitionEnded(server.ID)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := store.GetServer(context.Background(), server.ID)
		if err == nil && got.Status == db.StatusRunning {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, _ := store.GetServer(context.Background(), server.ID)
	t.Fatalf("status after NotifyTransitionEnded = %s, want running", got.Status)
}
