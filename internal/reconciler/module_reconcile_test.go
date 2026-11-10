// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package reconciler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/module"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// recordingModules is a ModuleReconciler that records every pass.
type recordingModules struct {
	mu      sync.Mutex
	seen    []string
	err     error
	outcome string
}

func (r *recordingModules) ReconcileModule(_ context.Context, moduleID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, moduleID)
	if r.err != nil {
		return "", r.err
	}
	if r.outcome == "" {
		return "in_sync", nil
	}
	return r.outcome, nil
}

func (r *recordingModules) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func (r *recordingModules) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = nil
}

// moduleAwareStore adds ListModules to the test store, which is how the sweep
// discovers modules.
type moduleAwareStore struct {
	Store
	modules             []*db.Module
	omitServersFromList bool
}

func (s *moduleAwareStore) ListModules(context.Context) ([]*db.Module, error) {
	return s.modules, nil
}

// ListServers is how the startup sweep finds parent servers. Tests that assert
// a specific ReconcileServer pass can hide those rows so the sweep cannot
// adopt or recreate first and swallow the transition.
func (s *moduleAwareStore) ListServers(ctx context.Context) ([]*db.Server, error) {
	if s.omitServersFromList {
		return nil, nil
	}
	return s.Store.ListServers(ctx)
}

func callsInclude(calls []string, id string) bool {
	for _, c := range calls {
		if c == id {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not reached within 2s")
}

func TestModuleEventsScheduleAModulePass(t *testing.T) {
	modules := &recordingModules{}
	r := New(&fakeStore{}, &fakeResolver{}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
	})
	r.SetModuleReconciler(modules)

	events := make(chan ContainerEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx, events)
	defer r.Stop()

	events <- ContainerEvent{ModuleID: "mod-1", ContainerID: "abc"}

	waitFor(t, func() bool { return len(modules.calls()) == 1 })
	if got := modules.calls()[0]; got != "mod-1" {
		t.Fatalf("reconciled %q, want mod-1", got)
	}
}

func TestAStageEventForAModuleIsNotTreatedAsAServer(t *testing.T) {
	modules := &recordingModules{}
	r := New(&fakeStore{}, &fakeResolver{}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
	})
	r.SetModuleReconciler(modules)

	events := make(chan ContainerEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx, events)
	defer r.Stop()

	events <- ContainerEvent{ModuleID: "mod-2", ServerID: ""}

	waitFor(t, func() bool { return len(modules.calls()) == 1 })
	if modules.calls()[0] != "mod-2" {
		t.Fatalf("calls = %v, want the module pass", modules.calls())
	}
}

func TestTheSweepAlsoCoversModulesWithAContainer(t *testing.T) {
	modules := &recordingModules{}
	store := &moduleAwareStore{
		Store: &fakeStore{},
		modules: []*db.Module{
			{ID: "mod-with-container", ContainerID: "abc", Status: db.ModuleStatusRunning},
			{ID: "mod-without-container", ContainerID: "", Status: db.ModuleStatusStopped},
			{ID: "mod-running-no-container", ContainerID: "", Status: db.ModuleStatusRunning},
		},
	}
	r := New(store, &fakeResolver{}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
	})
	r.SetModuleReconciler(modules)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx, nil)
	defer r.Stop()

	waitFor(t, func() bool { return len(modules.calls()) >= 2 })
	calls := modules.calls()
	want := map[string]bool{"mod-with-container": false, "mod-running-no-container": false}
	for _, id := range calls {
		if _, ok := want[id]; ok {
			want[id] = true
		}
	}
	for id, seen := range want {
		if !seen {
			t.Fatalf("calls = %v, want sweep to include %s", calls, id)
		}
	}
}

func TestAServerAdoptionSchedulesChildModules(t *testing.T) {
	server := &db.Server{
		ID: "srv-parent", Name: "Survival", NodeID: "default",
		ContainerID: "old-container", Status: db.StatusRunning,
	}
	modules := &recordingModules{}
	store := &moduleAwareStore{
		Store: newFakeStore(server),
		modules: []*db.Module{
			{ID: "sidecar", ServerID: "srv-parent", ContainerID: "mod-c1", Status: db.ModuleStatusRunning},
			{ID: "other-server", ServerID: "srv-other", ContainerID: "mod-c2", Status: db.ModuleStatusRunning},
		},
		omitServersFromList: true,
	}
	cli := &fakeClient{
		summary: &container.Summary{ID: "new-container"},
		state:   &docker.ContainerState{ContainerID: "new-container", Status: db.StatusRunning},
	}
	r := New(store, fakeResolver{cli: cli}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
		SweepInterval:  time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	r.SetModuleReconciler(modules)
	r.Start(ctx, nil)
	t.Cleanup(func() {
		r.Stop()
		cancel()
	})
	waitFor(t, func() bool {
		calls := modules.calls()
		return callsInclude(calls, "sidecar") && callsInclude(calls, "other-server")
	})
	modules.reset()

	if err := r.ReconcileServer(ctx, "srv-parent"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}

	waitFor(t, func() bool {
		calls := modules.calls()
		return callsInclude(calls, "sidecar") && !callsInclude(calls, "other-server")
	})
}

func TestAServerSelfHealRecreateSchedulesChildModules(t *testing.T) {
	server := &db.Server{
		ID: "srv-parent", Name: "Survival", NodeID: "default",
		ContainerID: "gone", Status: db.StatusRunning,
	}
	modules := &recordingModules{}
	store := &moduleAwareStore{
		Store: newFakeStore(server),
		modules: []*db.Module{
			{ID: "sidecar", ServerID: "srv-parent", ContainerID: "", Status: db.ModuleStatusRunning},
		},
		omitServersFromList: true,
	}
	cli := &fakeClient{resolveErr: fmt.Errorf("%w: srv-parent", docker.ErrContainerNotResolved)}
	r := New(store, fakeResolver{cli: cli}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
		SweepInterval:  time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	r.SetModuleReconciler(modules)
	r.Start(ctx, nil)
	t.Cleanup(func() {
		r.Stop()
		cancel()
	})
	waitFor(t, func() bool {
		return callsInclude(modules.calls(), "sidecar")
	})
	modules.reset()

	if err := r.ReconcileServer(ctx, "srv-parent"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}

	waitFor(t, func() bool {
		return callsInclude(modules.calls(), "sidecar")
	})
}

func TestAServerStatusTransitionSchedulesChildModules(t *testing.T) {
	cases := []struct {
		name     string
		initial  db.ServerStatus
		observed db.ServerStatus
	}{
		{name: "running", initial: db.StatusStarting, observed: db.StatusRunning},
		{name: "stopped", initial: db.StatusRunning, observed: db.StatusStopped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := &db.Server{
				ID: "srv-parent", Name: "Survival", NodeID: "default",
				ContainerID: "same-container", Status: tc.initial,
			}
			modules := &recordingModules{}
			store := &moduleAwareStore{
				Store: newFakeStore(server),
				modules: []*db.Module{
					{ID: "sidecar", ServerID: "srv-parent", ContainerID: "mod-c1", Status: db.ModuleStatusRunning},
					{ID: "other-server", ServerID: "srv-other", ContainerID: "mod-c2", Status: db.ModuleStatusRunning},
				},
				omitServersFromList: true,
			}
			cli := &fakeClient{
				summary: &container.Summary{ID: "same-container"},
				state:   &docker.ContainerState{ContainerID: "same-container", Status: tc.observed},
			}
			r := New(store, fakeResolver{cli: cli}, nil, nil, nil, logger.New(), Config{
				DebounceWindow: 10 * time.Millisecond,
				SweepInterval:  time.Hour,
			})
			ctx, cancel := context.WithCancel(context.Background())
			r.SetModuleReconciler(modules)
			r.Start(ctx, nil)
			t.Cleanup(func() {
				r.Stop()
				cancel()
			})
			waitFor(t, func() bool {
				calls := modules.calls()
				return callsInclude(calls, "sidecar") && callsInclude(calls, "other-server")
			})
			modules.reset()

			if err := r.ReconcileServer(ctx, "srv-parent"); err != nil {
				t.Fatalf("ReconcileServer: %v", err)
			}

			waitFor(t, func() bool {
				calls := modules.calls()
				return callsInclude(calls, "sidecar") && !callsInclude(calls, "other-server")
			})
		})
	}
}

// hideServersFromSweep keeps the startup sweep from reconciling the parent
// before the test drives ReconcileServer itself.
type hideServersFromSweep struct {
	*db.Store
	mu          sync.Mutex
	listModules bool
}

func (s *hideServersFromSweep) ListServers(context.Context) ([]*db.Server, error) {
	return nil, nil
}

func (s *hideServersFromSweep) ListModules(ctx context.Context) ([]*db.Module, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.listModules {
		return nil, nil
	}
	return s.Store.ListModules(ctx)
}

type recordingManagerPass struct {
	manager *module.Manager
	mu      sync.Mutex
	seen    []string
	last    string
}

func (p *recordingManagerPass) ReconcileModule(ctx context.Context, moduleID string) (string, error) {
	outcome, err := p.manager.ReconcileModule(ctx, moduleID)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, moduleID)
	p.last = string(outcome)
	return string(outcome), err
}

func (p *recordingManagerPass) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

func (p *recordingManagerPass) lastOutcome() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

func TestAServerSelfHealDoesNotMarkRunningChildrenGone(t *testing.T) {
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Path:           ":memory:",
			AutoMigrate:    true,
			MaxConnections: 1,
		},
	}
	store, err := db.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	ctx := context.Background()
	server := &db.Server{
		ID: "srv-parent", Name: "Survival", NodeID: "local",
		ContainerID: "gone", Status: db.StatusRunning,
		ModLoader: db.ModLoaderVanilla, MCVersion: "1.21", DataPath: t.TempDir(),
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	mod := &db.Module{
		ID: "sidecar", Name: "sidecar", ServerID: server.ID, NodeID: "local",
		ContainerID: "mod-gone", Status: db.ModuleStatusRunning,
	}
	if err := store.DB().Create(mod).Error; err != nil {
		t.Fatal(err)
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(api.Close)
	cli, err := docker.NewClient(strings.Replace(api.URL, "http://", "tcp://", 1), logger.New())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := cli.Close(); err != nil {
			t.Errorf("close docker client: %v", err)
		}
	})
	manager := module.NewManager(store, cli, nil, nil, nil, logger.New())
	pass := &recordingManagerPass{manager: manager}
	gated := &hideServersFromSweep{Store: store}

	serverCLI := &fakeClient{resolveErr: fmt.Errorf("%w: srv-parent", docker.ErrContainerNotResolved)}
	r := New(gated, fakeResolver{cli: serverCLI}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
		SweepInterval:  time.Hour,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	r.SetModuleReconciler(pass)
	r.Start(runCtx, nil)
	t.Cleanup(func() {
		r.Stop()
		cancel()
	})

	gated.mu.Lock()
	gated.listModules = true
	gated.mu.Unlock()

	if err := r.ReconcileServer(runCtx, "srv-parent"); err != nil {
		t.Fatalf("ReconcileServer: %v", err)
	}
	gotServer, err := store.GetServer(ctx, "srv-parent")
	if err != nil {
		t.Fatal(err)
	}
	if gotServer.Status != db.StatusStarting {
		t.Fatalf("parent status = %s, want starting after self-heal", gotServer.Status)
	}

	waitFor(t, func() bool {
		return callsInclude(pass.calls(), "sidecar")
	})
	if got := pass.lastOutcome(); got != string(module.ReconcileWaiting) {
		t.Fatalf("child outcome = %s, want waiting while the parent is starting", got)
	}
	gotMod, err := store.GetModule(ctx, "sidecar")
	if err != nil {
		t.Fatal(err)
	}
	if gotMod.Status != db.ModuleStatusRunning || gotMod.ContainerID != "mod-gone" {
		t.Fatalf("child was marked gone while the parent is starting: %+v", gotMod)
	}
}

func TestAModulePassPropagatesToDependents(t *testing.T) {
	modules := &recordingModules{outcome: "adopted"}
	store := &moduleAwareStore{
		Store: &fakeStore{},
		modules: []*db.Module{
			{ID: "dep-upstream", ContainerID: "abc", Status: db.ModuleStatusRunning},
			{
				ID: "dep-downstream", ContainerID: "def", Status: db.ModuleStatusRunning,
				Dependencies: []*v1.ModuleDependency{{ModuleId: "dep-upstream"}},
			},
		},
	}
	r := New(store, &fakeResolver{}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
	})
	r.SetModuleReconciler(modules)

	events := make(chan ContainerEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx, events)
	defer r.Stop()

	events <- ContainerEvent{ModuleID: "dep-upstream", ContainerID: "abc"}

	waitFor(t, func() bool {
		calls := modules.calls()
		return len(calls) >= 2 && calls[len(calls)-1] == "dep-downstream"
	})
}

func TestModulePassesAreDisabledWithoutAModuleReconciler(t *testing.T) {
	r := New(&fakeStore{}, &fakeResolver{}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
	})
	events := make(chan ContainerEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx, events)
	defer r.Stop()

	// Neither of these may block or panic when the module system is absent.
	events <- ContainerEvent{ModuleID: "mod-3"}
	time.Sleep(80 * time.Millisecond)
}

func TestAModuleFailureIsLoggedAndDoesNotStopTheWorker(t *testing.T) {
	modules := &recordingModules{err: errors.New("daemon unavailable")}
	r := New(&fakeStore{}, &fakeResolver{}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
	})
	r.SetModuleReconciler(modules)

	events := make(chan ContainerEvent, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx, events)
	defer r.Stop()

	events <- ContainerEvent{ModuleID: "mod-4"}
	waitFor(t, func() bool { return len(modules.calls()) == 1 })
	events <- ContainerEvent{ModuleID: "mod-5"}
	waitFor(t, func() bool { return len(modules.calls()) == 2 })

	if got := modules.calls(); got[1] != "mod-5" {
		t.Fatalf("calls = %v, want the worker to keep processing after an error", got)
	}
}

func TestReconnectEventAdoptsReplacedModuleIdentity(t *testing.T) {
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Path:           ":memory:",
			AutoMigrate:    true,
			MaxConnections: 1,
		},
	}
	store, err := db.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	ctx := context.Background()
	server := &db.Server{
		ID: "srv-reconnect", Name: "Survival", NodeID: "local",
		ContainerID: "game", Status: db.StatusRunning,
		ModLoader: db.ModLoaderVanilla, MCVersion: "1.21", DataPath: t.TempDir(),
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	mod := &db.Module{
		ID: "sidecar-reconnect", Name: "sidecar", ServerID: server.ID, NodeID: "local",
		ContainerID: "stale-before-restart", Status: db.ModuleStatusRunning,
		Ports: []*v1.ModulePort{{Name: "Bedrock", HostPort: 19132, ContainerPort: 19132, Protocol: "udp"}},
	}
	if err := store.DB().Create(mod).Error; err != nil {
		t.Fatal(err)
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":"after-reconnect","Names":["/carbon-panel-module-sidecar-reconnect"],"State":"running","Ports":[{"PublicPort":19132,"PrivatePort":19132,"Type":"udp"}]}]`))
		case strings.Contains(r.URL.Path, "/containers/after-reconnect/json"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"after-reconnect","Name":"/carbon-panel-module-sidecar-reconnect","State":{"Status":"running","Running":true,"ExitCode":0},"Config":{"Labels":{}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	cli, err := docker.NewClient(strings.Replace(api.URL, "http://", "tcp://", 1), logger.New())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	manager := module.NewManager(store, cli, nil, nil, nil, logger.New())
	pass := &recordingManagerPass{manager: manager}
	gated := &hideServersFromSweep{Store: store}
	r := New(gated, &fakeResolver{}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
		SweepInterval:  time.Hour,
	})
	events := make(chan ContainerEvent, 1)
	runCtx, cancel := context.WithCancel(context.Background())
	r.SetModuleReconciler(pass)
	r.Start(runCtx, events)
	t.Cleanup(func() {
		r.Stop()
		cancel()
	})

	events <- ContainerEvent{ModuleID: "sidecar-reconnect", ContainerID: "after-reconnect"}
	waitFor(t, func() bool { return callsInclude(pass.calls(), "sidecar-reconnect") })
	if got := pass.lastOutcome(); got != string(module.ReconcileAdopted) {
		t.Fatalf("outcome = %s, want adopted after reconnect", got)
	}
	got, err := store.GetModule(ctx, "sidecar-reconnect")
	if err != nil {
		t.Fatal(err)
	}
	if got.ContainerID != "after-reconnect" {
		t.Fatalf("container id = %q, want the identity observed after reconnect", got.ContainerID)
	}
	if got.Status != db.ModuleStatusRunning {
		t.Fatalf("status = %s, want running", got.Status)
	}
	if len(got.Ports) != 1 || got.Ports[0].HostPort != 19132 {
		t.Fatalf("ports = %+v, want the recorded 19132", got.Ports)
	}
}

func TestLastSweepAtAdvancesWithASweep(t *testing.T) {
	r := New(&fakeStore{}, &fakeResolver{}, nil, nil, nil, logger.New(), Config{
		DebounceWindow: 10 * time.Millisecond,
	})
	if got := r.LastSweepAt(); !got.IsZero() {
		t.Fatalf("LastSweepAt before any sweep = %v, want the zero time", got)
	}

	before := time.Now()
	r.enqueueAll(context.Background())
	after := r.LastSweepAt()

	if after.IsZero() {
		t.Fatal("LastSweepAt is zero after a sweep")
	}
	if after.Before(before.Add(-time.Second)) || after.After(time.Now().Add(time.Second)) {
		t.Fatalf("LastSweepAt = %v, want a time around now", after)
	}
}
