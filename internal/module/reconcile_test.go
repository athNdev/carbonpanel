// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// The decision table is the contract every daemon interaction is built on, so
// it is asserted without Docker at all.
func TestDecideModule(t *testing.T) {
	running := &storage.Server{ID: "srv", Status: storage.StatusRunning}
	stopped := &storage.Server{ID: "srv", Status: storage.StatusStopped}

	allReady := reconcileGates{DependenciesReady: true, HostPortsReady: true}

	cases := []struct {
		name   string
		mod    *storage.Module
		parent *storage.Server
		obs    ModuleObservation
		gates  reconcileGates
		want   moduleAction
	}{
		{
			name: "no record",
			mod:  nil,
			want: actionNone,
		},
		{
			name:   "running module whose container vanished under a running parent is recreated",
			mod:    &storage.Module{Status: storage.ModuleStatusRunning, ContainerID: "old"},
			parent: running,
			obs:    ModuleObservation{Exists: false},
			want:   actionRecreate,
		},
		{
			name:   "running module whose container vanished under a stopped parent is not resurrected",
			mod:    &storage.Module{Status: storage.ModuleStatusRunning, ContainerID: "old"},
			parent: stopped,
			obs:    ModuleObservation{Exists: false},
			want:   actionMarkGone,
		},
		{
			name:   "running module whose parent row is gone is not resurrected",
			mod:    &storage.Module{Status: storage.ModuleStatusRunning, ContainerID: "old"},
			parent: nil,
			obs:    ModuleObservation{Exists: false},
			want:   actionMarkGone,
		},
		{
			name: "stopped module whose container vanished is recorded as gone",
			mod:  &storage.Module{Status: storage.ModuleStatusStopped, ContainerID: "old"},
			obs:  ModuleObservation{Exists: false},
			want: actionMarkGone,
		},
		{
			name: "stopped module with no container id has nothing to do",
			mod:  &storage.Module{Status: storage.ModuleStatusStopped},
			obs:  ModuleObservation{Exists: false},
			want: actionNone,
		},
		{
			name:   "a drifted container id is adopted",
			mod:    &storage.Module{Status: storage.ModuleStatusRunning, ContainerID: "old"},
			parent: running,
			obs:    ModuleObservation{Exists: true, ContainerID: "new", Running: true},
			want:   actionAdopt,
		},
		{
			name:   "a container that exited while the row says running is adopted",
			mod:    &storage.Module{Status: storage.ModuleStatusRunning, ContainerID: "same"},
			parent: running,
			obs:    ModuleObservation{Exists: true, ContainerID: "same", Running: false},
			want:   actionAdopt,
		},
		{
			name:   "a container that started while the row says stopped is adopted",
			mod:    &storage.Module{Status: storage.ModuleStatusStopped, ContainerID: "same"},
			parent: running,
			obs:    ModuleObservation{Exists: true, ContainerID: "same", Running: true},
			want:   actionAdopt,
		},
		{
			name:   "an agreeing observation needs no work",
			mod:    &storage.Module{Status: storage.ModuleStatusRunning, ContainerID: "same"},
			parent: running,
			obs:    ModuleObservation{Exists: true, ContainerID: "same", Running: true},
			gates:  allReady,
			want:   actionNone,
		},
		{
			name: "a missing container is not recreated while a dependency is stopped",
			mod: &storage.Module{
				Status: storage.ModuleStatusRunning, ContainerID: "old",
				Dependencies: []*v1.ModuleDependency{{ModuleId: "dep-a"}},
			},
			parent: running,
			obs:    ModuleObservation{Exists: false},
			gates:  reconcileGates{DependenciesReady: false, HostPortsReady: true},
			want:   actionWaitForDependency,
		},
		{
			name: "a missing container is recreated when dependencies are running",
			mod: &storage.Module{
				Status: storage.ModuleStatusRunning, ContainerID: "old",
				Dependencies: []*v1.ModuleDependency{{ModuleId: "dep-a"}},
			},
			parent: running,
			obs:    ModuleObservation{Exists: false},
			gates:  allReady,
			want:   actionRecreate,
		},
		{
			name: "an agreeing observation is not in sync while a dependency is stopped",
			mod: &storage.Module{
				Status: storage.ModuleStatusRunning, ContainerID: "same",
				Dependencies: []*v1.ModuleDependency{{ModuleId: "dep-a"}},
			},
			parent: running,
			obs:    ModuleObservation{Exists: true, ContainerID: "same", Running: true},
			gates:  reconcileGates{DependenciesReady: false, HostPortsReady: true},
			want:   actionWaitForDependency,
		},
		{
			name: "recreate waits on host port reservations",
			mod: &storage.Module{
				Status: storage.ModuleStatusRunning, ContainerID: "old",
				Ports: []*v1.ModulePort{{Name: "web", HostPort: 8100, ContainerPort: 8100}},
			},
			parent: running,
			obs:    ModuleObservation{Exists: false},
			gates:  reconcileGates{DependenciesReady: true, HostPortsReady: false},
			want:   actionWaitForPorts,
		},
		{
			name:   "a stopped parent still prevents resurrection when dependencies are ready",
			mod:    &storage.Module{Status: storage.ModuleStatusRunning, ContainerID: "old"},
			parent: stopped,
			obs:    ModuleObservation{Exists: false},
			gates:  allReady,
			want:   actionMarkGone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gates := tc.gates
			if gates == (reconcileGates{}) {
				gates = allReady
			}
			if got := decideModule(tc.mod, tc.parent, tc.obs, gates); got != tc.want {
				t.Fatalf("action = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestApplyReconcileGates(t *testing.T) {
	running := &storage.Module{Status: storage.ModuleStatusRunning}
	stopped := &storage.Module{Status: storage.ModuleStatusStopped}

	if got := applyReconcileGates(running, actionRecreate, reconcileGates{DependenciesReady: false, HostPortsReady: true}); got != actionWaitForDependency {
		t.Fatalf("recreate with blocked dependency = %s, want %s", got, actionWaitForDependency)
	}
	if got := applyReconcileGates(running, actionRecreate, reconcileGates{DependenciesReady: true, HostPortsReady: false}); got != actionWaitForPorts {
		t.Fatalf("recreate with blocked port = %s, want %s", got, actionWaitForPorts)
	}
	if got := applyReconcileGates(running, actionNone, reconcileGates{DependenciesReady: false, HostPortsReady: true}); got != actionWaitForDependency {
		t.Fatalf("in sync with blocked dependency = %s, want %s", got, actionWaitForDependency)
	}
	if got := applyReconcileGates(stopped, actionRecreate, reconcileGates{DependenciesReady: false, HostPortsReady: false}); got != actionRecreate {
		t.Fatalf("stopped module should ignore gates, got %s", got)
	}
}

func TestModulePublishesHostPort(t *testing.T) {
	if !modulePublishesHostPort(&v1.ModulePort{HostPort: 8080, ProxyEnabled: false}) {
		t.Fatal("direct host bind should publish")
	}
	if modulePublishesHostPort(&v1.ModulePort{HostPort: 8080, ProxyEnabled: true}) {
		t.Fatal("proxy-only port should not publish on the host")
	}
	if !modulePublishesHostPort(&v1.ModulePort{HostPort: 19132, ProxyEnabled: true, ExposePublic: true}) {
		t.Fatal("public proxied port should publish on the host")
	}
}

// newModuleReconcileFixture wires a manager against a fake daemon that answers
// the module label lookup and container inspect.
func newModuleReconcileFixture(t *testing.T, handler http.HandlerFunc) (*Manager, *storage.Store) {
	t.Helper()
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)

	store := setupTestStore(t)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	cli, err := docker.NewClient(strings.Replace(api.URL, "http://", "tcp://", 1), logger.New())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := cli.Close(); err != nil {
			t.Errorf("close docker client: %v", err)
		}
	})
	return NewManager(store, cli, nil, nil, nil, logger.New()), store
}

func TestReconcileModuleRecordsAMissingContainerAsStopped(t *testing.T) {
	manager, store := newModuleReconcileFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
			return
		}
		http.NotFound(w, r)
	})
	ctx := context.Background()
	server := &storage.Server{ID: "srv-missing", Name: "srv", Status: storage.StatusRunning, NodeID: "local"}
	if err := store.DB().Create(server).Error; err != nil {
		t.Fatal(err)
	}
	mod := &storage.Module{
		ID: "mod-missing", Name: "sidecar", ServerID: server.ID, NodeID: "local",
		ContainerID: "gone", Status: storage.ModuleStatusStopped,
	}
	if err := store.DB().Create(mod).Error; err != nil {
		t.Fatal(err)
	}

	outcome, err := manager.ReconcileModule(ctx, mod.ID)
	if err != nil {
		t.Fatalf("ReconcileModule: %v", err)
	}
	if outcome != ReconcileStopped {
		t.Fatalf("outcome = %s, want stopped", outcome)
	}
	got, err := store.GetModule(ctx, mod.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContainerID != "" {
		t.Errorf("container id = %q, want it cleared", got.ContainerID)
	}
	if got.Status != storage.ModuleStatusStopped {
		t.Errorf("status = %s, want stopped", got.Status)
	}
}

func TestReconcileModuleAdoptsTheDaemonStatus(t *testing.T) {
	manager, store := newModuleReconcileFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":"live-container","Names":["/carbon-panel-module-mod-live"],"State":"running","Status":"Up 2 minutes"}]`))
		case strings.Contains(r.URL.Path, "/containers/live-container/json"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"live-container","Name":"/carbon-panel-module-mod-live","State":{"Status":"running","Running":true,"ExitCode":0},"Config":{"Labels":{}}}`))
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	server := &storage.Server{ID: "srv-live", Name: "srv", Status: storage.StatusRunning, NodeID: "local"}
	if err := store.DB().Create(server).Error; err != nil {
		t.Fatal(err)
	}
	mod := &storage.Module{
		ID: "mod-live", Name: "sidecar", ServerID: server.ID, NodeID: "local",
		ContainerID: "recorded-container", Status: storage.ModuleStatusStopped,
	}
	if err := store.DB().Create(mod).Error; err != nil {
		t.Fatal(err)
	}

	outcome, err := manager.ReconcileModule(ctx, mod.ID)
	if err != nil {
		t.Fatalf("ReconcileModule: %v", err)
	}
	if outcome != ReconcileAdopted {
		t.Fatalf("outcome = %s, want adopted", outcome)
	}
	got, err := store.GetModule(ctx, mod.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContainerID != "live-container" {
		t.Errorf("container id = %q, want the daemon's live-container", got.ContainerID)
	}
	if got.Status != storage.ModuleStatusRunning {
		t.Errorf("status = %s, want running", got.Status)
	}
}

func TestReconcileModuleLeavesTheRecordAloneWhenTheNodeIsUnreachable(t *testing.T) {
	manager, store := newModuleReconcileFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"daemon down"}`, http.StatusInternalServerError)
	})
	ctx := context.Background()
	mod := &storage.Module{
		ID: "mod-down", Name: "sidecar", ServerID: "srv-down", NodeID: "local",
		ContainerID: "kept", Status: storage.ModuleStatusRunning,
	}
	if err := store.DB().Create(mod).Error; err != nil {
		t.Fatal(err)
	}

	outcome, err := manager.ReconcileModule(ctx, mod.ID)
	if err == nil {
		t.Fatal("error = nil for an unreachable daemon; the record must not be reported as converged")
	}
	if outcome != ReconcileSkipped {
		t.Fatalf("outcome = %s, want skipped", outcome)
	}
	got, getErr := store.GetModule(ctx, mod.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if got.ContainerID != "kept" || got.Status != storage.ModuleStatusRunning {
		t.Errorf("record changed while the daemon was unreachable: %+v", got)
	}
}

func TestReconcileModuleIgnoresADeletedModule(t *testing.T) {
	manager, _ := newModuleReconcileFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	outcome, err := manager.ReconcileModule(context.Background(), "never-existed")
	if err != nil {
		t.Fatalf("ReconcileModule: %v", err)
	}
	if outcome != ReconcileSkipped {
		t.Fatalf("outcome = %s, want skipped", outcome)
	}
}
