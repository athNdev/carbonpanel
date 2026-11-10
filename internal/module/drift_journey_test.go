// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package module

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// moduleDaemon is a stateful fake Engine for the drift/reconnect journey.
// It records creates and volume-deleting removes so a pass cannot hide a
// duplicate sidecar or a casual purge.
type moduleDaemon struct {
	mu            sync.Mutex
	containers    []map[string]any
	inspect       map[string]string
	creates       int
	volumeDeletes int
	deletes       int
	nextCreateID  string
	imagePresent  bool
}

func newModuleDaemon() *moduleDaemon {
	return &moduleDaemon{
		inspect:      map[string]string{},
		nextCreateID: "recreated-sidecar",
		imagePresent: true,
	}
}

func (d *moduleDaemon) setRunning(id, name string, hostPort int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.containers = []map[string]any{{
		"Id":    id,
		"Names": []string{name},
		"State": "running",
		"Ports": []map[string]any{{"PublicPort": hostPort, "PrivatePort": hostPort, "Type": "udp"}},
	}}
	d.inspect[id] = fmt.Sprintf(`{"Id":%q,"Name":%q,"State":{"Status":"running","Running":true,"ExitCode":0},"Config":{"Labels":{}},"NetworkSettings":{"Ports":{"19132/udp":[{"HostIp":"0.0.0.0","HostPort":"%d"}]}}}`, id, name, hostPort)
}

func (d *moduleDaemon) setEmpty() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.containers = nil
}

func (d *moduleDaemon) snapshot() (creates, deletes, volumeDeletes int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.creates, d.deletes, d.volumeDeletes
}

func (d *moduleDaemon) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		switch {
		case r.URL.Path == "/_ping":
			w.Header().Set("Api-Version", "1.45")
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			if d.containers == nil {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_ = json.NewEncoder(w).Encode(d.containers)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			if d.imagePresent {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"Id":"sha256:module-fixture"}`))
				return
			}
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/containers/create"):
			d.creates++
			id := d.nextCreateID
			d.containers = []map[string]any{{
				"Id":    id,
				"Names": []string{"/carbon-panel-module-created"},
				"State": "created",
			}}
			d.inspect[id] = fmt.Sprintf(`{"Id":%q,"Name":"/carbon-panel-module-created","State":{"Status":"created","Running":false},"Config":{"Labels":{}}}`, id)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"Id":%q}`, id)))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/stop"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
			d.deletes++
			if r.URL.Query().Get("v") == "1" || r.URL.Query().Get("v") == "true" {
				d.volumeDeletes++
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			for id, body := range d.inspect {
				if strings.Contains(r.URL.Path, "/containers/"+id+"/json") {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(body))
					return
				}
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}
}

func newDriftFixture(t *testing.T, daemon *moduleDaemon) (*Manager, *storage.Store) {
	t.Helper()
	api := httptest.NewServer(daemon.handler())
	t.Cleanup(api.Close)
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	cli, err := docker.NewClient(strings.Replace(api.URL, "http://", "tcp://", 1), logger.New())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return NewManager(store, cli, nil, nil, nil, logger.New()), store
}

func seedRunningSidecar(t *testing.T, store *storage.Store, moduleID, containerID string) (*storage.Server, *storage.Module) {
	t.Helper()
	ctx := context.Background()
	server := &storage.Server{
		ID: "srv-drift", Name: "Survival", Status: storage.StatusRunning, NodeID: "local",
		ModLoader: storage.ModLoaderVanilla, MCVersion: "1.21", DataPath: t.TempDir(),
	}
	if err := store.DB().Create(server).Error; err != nil {
		t.Fatal(err)
	}
	tmpl := &storage.ModuleTemplate{
		ID: "tmpl-drift", Name: "sidecar", DockerImage: "example/sidecar:test", Type: storage.ModuleTemplateTypeBuiltin,
	}
	if err := store.DB().Create(tmpl).Error; err != nil {
		t.Fatal(err)
	}
	mod := &storage.Module{
		ID: moduleID, Name: "geyser", ServerID: server.ID, NodeID: "local",
		TemplateID: tmpl.ID, ContainerID: containerID, Status: storage.ModuleStatusRunning,
		Ports: []*v1.ModulePort{{Name: "Bedrock", HostPort: 19132, ContainerPort: 19132, Protocol: "udp", ExposePublic: true}},
	}
	if err := store.DB().Create(mod).Error; err != nil {
		t.Fatal(err)
	}
	_ = ctx
	return server, mod
}

func TestDriftJourneyExternalReplaceAdoptsIdentityAndPorts(t *testing.T) {
	daemon := newModuleDaemon()
	daemon.setRunning("replaced-live", "/carbon-panel-module-mod-drift", 19132)
	manager, store := newDriftFixture(t, daemon)
	_, mod := seedRunningSidecar(t, store, "mod-drift", "recorded-stale")

	outcome, err := manager.ReconcileModule(context.Background(), mod.ID)
	if err != nil {
		t.Fatalf("ReconcileModule: %v", err)
	}
	if outcome != ReconcileAdopted {
		t.Fatalf("outcome = %s, want adopted after an external replace", outcome)
	}
	got, err := store.GetModule(context.Background(), mod.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContainerID != "replaced-live" {
		t.Fatalf("container id = %q, want the daemon's replacement", got.ContainerID)
	}
	if got.Status != storage.ModuleStatusRunning {
		t.Fatalf("status = %s, want running", got.Status)
	}
	if len(got.Ports) != 1 || got.Ports[0].HostPort != 19132 {
		t.Fatalf("ports = %+v, want the recorded 19132 (no duplicate bind)", got.Ports)
	}
	creates, deletes, volumeDeletes := daemon.snapshot()
	if creates != 0 {
		t.Fatalf("creates = %d, want 0 so the replacement is not duplicated", creates)
	}
	if deletes != 0 || volumeDeletes != 0 {
		t.Fatalf("deletes=%d volumeDeletes=%d, replace must not purge sidecar data", deletes, volumeDeletes)
	}
}

func TestDriftJourneyLostContainerDoesNotPurgeVolumes(t *testing.T) {
	daemon := newModuleDaemon()
	daemon.setEmpty()
	manager, store := newDriftFixture(t, daemon)
	_, mod := seedRunningSidecar(t, store, "mod-lost", "vanished")

	outcome, err := manager.ReconcileModule(context.Background(), mod.ID)
	if err != nil {
		t.Fatalf("ReconcileModule: %v", err)
	}
	if outcome != ReconcileRecreated {
		t.Fatalf("outcome = %s, want recreated after the container vanished", outcome)
	}
	creates, deletes, volumeDeletes := daemon.snapshot()
	if creates < 1 {
		t.Fatal("expected a restore create after the container was lost")
	}
	if volumeDeletes != 0 {
		t.Fatalf("volumeDeletes = %d, a missing sidecar must not RemoveVolumes", volumeDeletes)
	}
	if deletes != 0 {
		t.Fatalf("deletes = %d, a gone container has nothing to DELETE", deletes)
	}
	got, err := store.GetModule(context.Background(), mod.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ports[0].HostPort != 19132 {
		t.Fatalf("host port drifted to %d during restore", got.Ports[0].HostPort)
	}
}

func TestDriftJourneyFailedHookRetryDoesNotDuplicate(t *testing.T) {
	daemon := newModuleDaemon()
	daemon.setRunning("hook-live", "/carbon-panel-module-mod-hook", 19132)
	manager, store := newDriftFixture(t, daemon)
	_, mod := seedRunningSidecar(t, store, "mod-hook", "hook-live")

	if err := manager.ensureModuleStarted(context.Background(), mod); err != nil {
		t.Fatalf("first hook start: %v", err)
	}
	createsAfterFirst, _, _ := daemon.snapshot()
	if err := manager.ensureModuleStarted(context.Background(), mod); err != nil {
		t.Fatalf("retry hook start: %v", err)
	}
	creates, deletes, volumeDeletes := daemon.snapshot()
	if creates != createsAfterFirst || creates != 0 {
		t.Fatalf("creates = %d (first pass %d), hook retry must not create a second sidecar", creates, createsAfterFirst)
	}
	if deletes != 0 || volumeDeletes != 0 {
		t.Fatalf("hook retry deleted sidecar data: deletes=%d volumeDeletes=%d", deletes, volumeDeletes)
	}
	got, err := store.GetModule(context.Background(), mod.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContainerID != "hook-live" {
		t.Fatalf("container id = %q, want hook-live", got.ContainerID)
	}
}

func TestDriftJourneyParentDeleteDoesNotRecreateOrPurge(t *testing.T) {
	daemon := newModuleDaemon()
	daemon.setEmpty()
	manager, store := newDriftFixture(t, daemon)
	server, mod := seedRunningSidecar(t, store, "mod-orphan", "child-gone")
	if err := store.DB().Delete(server).Error; err != nil {
		t.Fatal(err)
	}

	outcome, err := manager.ReconcileModule(context.Background(), mod.ID)
	if err != nil {
		t.Fatalf("ReconcileModule: %v", err)
	}
	if outcome != ReconcileStopped {
		t.Fatalf("outcome = %s, want stopped when the parent row is gone", outcome)
	}
	creates, deletes, volumeDeletes := daemon.snapshot()
	if creates != 0 {
		t.Fatalf("creates = %d, parent delete must not resurrect the sidecar", creates)
	}
	if volumeDeletes != 0 || deletes != 0 {
		t.Fatalf("parent-delete race purged volumes: deletes=%d volumeDeletes=%d", deletes, volumeDeletes)
	}
	got, err := store.GetModule(context.Background(), mod.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != storage.ModuleStatusStopped {
		t.Fatalf("status = %s, want stopped", got.Status)
	}
	if got.ContainerID != "" {
		t.Fatalf("container id = %q, want cleared without a Docker remove", got.ContainerID)
	}
}
