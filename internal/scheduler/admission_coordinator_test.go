// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scheduler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/serverruntime"
	"github.com/athNdev/carbon-panel/internal/staging"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// scheduledStartGate blocks one Docker start so a scheduled task can hold the
// shared admission gate while a second task races it.
type scheduledStartGate struct {
	blockID string
	entered chan string
	release chan struct{}
	starts  atomic.Int32
}

func (g *scheduledStartGate) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/_ping":
			w.Header().Set("Api-Version", "1.45")
			w.Header().Set("Ostype", "linux")
		case strings.HasSuffix(r.URL.Path, "/info"):
			_, _ = w.Write([]byte(`{"MemTotal":4294967296}`))
		case strings.HasSuffix(r.URL.Path, "/json"):
			_, _ = w.Write([]byte(`{"Id":"container","State":{"Running":false,"Status":"exited"}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			parts := strings.Split(r.URL.Path, "/")
			containerID := parts[len(parts)-2]
			g.starts.Add(1)
			if containerID == g.blockID {
				select {
				case g.entered <- containerID:
				case <-r.Context().Done():
					return
				}
				select {
				case <-g.release:
				case <-r.Context().Done():
					return
				}
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
}

func newAdmissionScheduler(t *testing.T, gate *scheduledStartGate, coordinator *serverruntime.AdmissionCoordinator) (*Scheduler, *db.Store) {
	t.Helper()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateNode(context.Background(), &db.Node{ID: "node", Name: "Node", IsLocal: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(gate.handler())
	t.Cleanup(api.Close)
	client, err := docker.NewClient("tcp://"+strings.TrimPrefix(api.URL, "http://"), logger.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	s := NewScheduler(store, client, nil, nil, nil, logger.New(), Config{Admission: coordinator})
	return s, store
}

// TestScheduledStartsShareAdmissionGate proves two scheduled same-node starts
// cannot overcommit when they share the coordinator with RPC starts: the second
// task re-checks persisted capacity after the first becomes starting.
func TestScheduledStartsShareAdmissionGate(t *testing.T) {
	ctx := context.Background()
	gate := &scheduledStartGate{blockID: "container-a", entered: make(chan string, 1), release: make(chan struct{})}
	defer func() {
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	}()
	coordinator := serverruntime.NewAdmissionCoordinator()
	s, store := newAdmissionScheduler(t, gate, coordinator)

	serverA := &db.Server{ID: "sched-a", Name: "A", NodeID: "node", ContainerID: "container-a", Memory: 3072, Status: db.StatusStopped}
	serverB := &db.Server{ID: "sched-b", Name: "B", NodeID: "node", ContainerID: "container-b", Memory: 3072, Status: db.StatusStopped}
	for _, server := range []*db.Server{serverA, serverB} {
		if err := store.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}

	type outcome struct {
		out string
		err error
	}
	aDone := make(chan outcome, 1)
	bDone := make(chan outcome, 1)
	go func() {
		out, err := s.executeStartTask(ctx, serverA, nil)
		aDone <- outcome{out, err}
	}()
	select {
	case id := <-gate.entered:
		if id != "container-a" {
			t.Fatalf("blocked container = %q", id)
		}
	case got := <-aDone:
		t.Fatalf("first task never reached Docker: %v", got.err)
	case <-time.After(5 * time.Second):
		t.Fatal("first task never reached the Docker gate")
	}
	go func() {
		out, err := s.executeStartTask(ctx, serverB, nil)
		bDone <- outcome{out, err}
	}()

	close(gate.release)
	select {
	case got := <-aDone:
		if got.err != nil {
			t.Fatalf("first task failed: %v", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first task did not settle")
	}
	select {
	case got := <-bDone:
		if got.err == nil || !strings.Contains(got.err.Error(), "lacks memory capacity") {
			t.Fatalf("second same-node task error = %v, want capacity refusal", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second task did not settle")
	}

	gotA, err := store.GetServer(ctx, serverA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotA.Status != db.StatusStarting || gotA.LastStarted == nil {
		t.Fatalf("admitted task state = %+v", gotA)
	}
	gotB, err := store.GetServer(ctx, serverB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotB.Status != db.StatusStopped || gotB.LastStarted != nil {
		t.Fatalf("refused task changed server = %+v", gotB)
	}
	if n := gate.starts.Load(); n != 1 {
		t.Fatalf("Docker start calls = %d, want only the admitted task", n)
	}
}

// TestScheduledDifferentNodesDoNotContend proves the shared gate is keyed by
// node: a second-node task completes while a first-node task holds the gate.
func TestScheduledDifferentNodesDoNotContend(t *testing.T) {
	ctx := context.Background()
	gate := &scheduledStartGate{blockID: "container-a", entered: make(chan string, 1), release: make(chan struct{})}
	defer func() {
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	}()
	coordinator := serverruntime.NewAdmissionCoordinator()
	s, store := newAdmissionScheduler(t, gate, coordinator)
	if err := store.CreateNode(ctx, &db.Node{ID: "other", Name: "Other", Host: "tcp://127.0.0.1:2375", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// Register a second client on the fixture daemon so the remote node is
	// resolvable without a real daemon.
	client := s.docker
	pool := docker.NewClientPool(store, client, s.log)
	pool.RegisterClient("node", client)
	pool.RegisterClient("other", client)
	s.SetClientPool(pool)
	t.Cleanup(func() { _ = pool.Close() })

	serverA := &db.Server{ID: "sched-parallel-a", Name: "A", NodeID: "node", ContainerID: "container-a", Memory: 1024, Status: db.StatusStopped}
	serverB := &db.Server{ID: "sched-parallel-b", Name: "B", NodeID: "other", ContainerID: "container-b", Memory: 1024, Status: db.StatusStopped}
	for _, server := range []*db.Server{serverA, serverB} {
		if err := store.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}

	aDone := make(chan error, 1)
	bDone := make(chan error, 1)
	go func() {
		_, err := s.executeStartTask(ctx, serverA, nil)
		aDone <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first-node task never reached the Docker gate")
	}
	go func() {
		_, err := s.executeStartTask(ctx, serverB, nil)
		bDone <- err
	}()
	select {
	case err := <-bDone:
		if err != nil {
			t.Fatalf("different-node task was blocked or refused: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("different-node task waited on an unrelated node's gate")
	}
	close(gate.release)
	select {
	case err := <-aDone:
		if err != nil {
			t.Fatalf("first-node task failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first-node task did not settle")
	}
	if n := gate.starts.Load(); n != 2 {
		t.Fatalf("Docker start calls = %d, want 2", n)
	}
}

// TestStartServerForBootUsesAdmissionGate proves boot auto-start waits on the
// shared gate: a queued boot refuses on deadline without touching Docker, then
// succeeds once the holder releases.
func TestStartServerForBootUsesAdmissionGate(t *testing.T) {
	ctx := context.Background()
	gate := &scheduledStartGate{blockID: "never-blocked", entered: make(chan string, 1), release: make(chan struct{})}
	coordinator := serverruntime.NewAdmissionCoordinator()
	s, store := newAdmissionScheduler(t, gate, coordinator)
	server := &db.Server{ID: "boot-admission", Name: "Boot", NodeID: "node", ContainerID: "container-boot", Memory: 1024, Status: db.StatusStopped}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	release, err := coordinator.Acquire(ctx, "node")
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if err := s.StartServerForBoot(waitCtx, server); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued boot error = %v, want deadline exceeded", err)
	}
	if n := gate.starts.Load(); n != 0 {
		t.Fatalf("queued boot reached Docker %d times", n)
	}
	release()

	if err := s.StartServerForBoot(ctx, server); err != nil {
		t.Fatalf("boot after release failed: %v", err)
	}
	if n := gate.starts.Load(); n != 1 {
		t.Fatalf("Docker start calls = %d, want 1", n)
	}
	got, err := store.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != db.StatusStarting || got.LastStarted == nil {
		t.Fatalf("boot state = %+v, want starting with LastStarted", got)
	}
}

// TestStagedPublicationStartAfterUsesAdmissionGate proves a background staged
// plan that starts a server cannot admit while another start holds the gate.
func TestStagedPublicationStartAfterUsesAdmissionGate(t *testing.T) {
	ctx := context.Background()
	gate := &scheduledStartGate{blockID: "never-blocked", entered: make(chan string, 1), release: make(chan struct{})}
	coordinator := serverruntime.NewAdmissionCoordinator()
	s, store := newAdmissionScheduler(t, gate, coordinator)
	server := &db.Server{ID: "staged-admission", Name: "Staged", NodeID: "node", ContainerID: "container-staged", Memory: 1024, Status: db.StatusStopped}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	plan := staging.Plan{Restart: true, StartAfter: true}

	release, err := coordinator.Acquire(ctx, "node")
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if err := s.publishStagedBatch(waitCtx, server, plan); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued staged publication error = %v, want deadline exceeded", err)
	}
	if n := gate.starts.Load(); n != 0 {
		t.Fatalf("queued staged publication reached Docker %d times", n)
	}
	release()

	if err := s.publishStagedBatch(ctx, server, plan); err != nil {
		t.Fatalf("staged publication after release failed: %v", err)
	}
	if n := gate.starts.Load(); n != 1 {
		t.Fatalf("Docker start calls = %d, want 1", n)
	}
	got, err := store.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Status and LastStarted stay with the reconciler on this path; the gate
	// must not move them. A failed start would have written StatusError.
	if got.Status != db.StatusStopped || got.LastStarted != nil {
		t.Fatalf("staged publication changed owned state = %+v", got)
	}
}
