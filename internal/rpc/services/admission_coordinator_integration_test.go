// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/events"
	"github.com/athNdev/carbon-panel/internal/serverruntime"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// admissionStartGate blocks one Docker start so a test can hold a start inside
// the admission gate while another start races it.
type admissionStartGate struct {
	blockID string
	entered chan string
	release chan struct{}
	starts  atomic.Int32
}

func (g *admissionStartGate) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/_ping":
			w.Header().Set("Api-Version", "1.45")
			w.Header().Set("Ostype", "linux")
		case strings.HasSuffix(r.URL.Path, "/info"):
			_, _ = w.Write([]byte(`{"MemTotal":4294967296}`))
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

func newAdmissionTestService(t *testing.T, gate *admissionStartGate, nodes ...string) (*ServerService, *admissionStartGate) {
	t.Helper()
	svc, _ := newDockerPrivilegeTestService(t)
	_ = svc.docker.Close()

	api := httptest.NewServer(gate.handler())
	t.Cleanup(api.Close)
	client, err := docker.NewClient("tcp://"+strings.TrimPrefix(api.URL, "http://"), svc.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// Register every fixture node on the one daemon stub so node resolution is
	// deterministic and no test opens a non-loopback listener.
	pool := docker.NewClientPool(svc.store, client, svc.log)
	for _, nodeID := range nodes {
		pool.RegisterClient(nodeID, client)
	}
	svc.docker, svc.pool = client, pool
	t.Cleanup(func() { _ = pool.Close() })
	svc.hostMemoryMB = func(context.Context, string) (int64, error) { return 4096, nil }
	svc.SetAdmissionCoordinator(serverruntime.NewAdmissionCoordinator())
	return svc, gate
}

func addAdmissionServer(t *testing.T, svc *ServerService, id, nodeID, containerID string, memory int) *db.Server {
	t.Helper()
	server := &db.Server{
		ID: id, Name: id, NodeID: nodeID, ContainerID: containerID,
		Port: 25700 + len(id), Memory: memory,
		Status: db.StatusStopped, DataPath: t.TempDir(),
	}
	if err := svc.store.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	return server
}

// TestConcurrentStartsOnSameNodeCannotOvercommit holds one start inside the
// shared admission gate and proves a second same-node start re-checks persisted
// capacity after the first becomes starting, instead of overcommitting.
func TestConcurrentStartsOnSameNodeCannotOvercommit(t *testing.T) {
	ctx := context.Background()
	gate := &admissionStartGate{
		blockID: "container-a",
		entered: make(chan string, 1),
		release: make(chan struct{}),
	}
	// A defer (not t.Cleanup) runs before the httptest server closes, so a
	// blocked Docker handler is always released before Close waits on it.
	defer func() {
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	}()
	svc, gate := newAdmissionTestService(t, gate, "test-node")
	serverA := addAdmissionServer(t, svc, "admission-a", "test-node", "container-a", 3072)
	serverB := addAdmissionServer(t, svc, "admission-b", "test-node", "container-b", 3072)

	bus := events.NewBus(svc.log)
	var eventsMu sync.Mutex
	var seen []events.Event
	bus.Subscribe(func(_ context.Context, e events.Event) {
		eventsMu.Lock()
		seen = append(seen, e)
		eventsMu.Unlock()
	})
	svc.bus = bus

	type outcome struct{ err error }
	aDone := make(chan outcome, 1)
	bDone := make(chan outcome, 1)
	go func() {
		_, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: serverA.ID}))
		aDone <- outcome{err}
	}()
	// Wait until A holds the gate inside Docker before launching B, so the
	// contention order is deterministic rather than a scheduler race.
	select {
	case id := <-gate.entered:
		if id != "container-a" {
			t.Fatalf("blocked container = %q, want container-a", id)
		}
	case got := <-aDone:
		t.Fatalf("first start never reached the Docker gate: %v", got.err)
	case <-time.After(5 * time.Second):
		t.Fatal("first start never reached the Docker gate")
	}
	go func() {
		_, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: serverB.ID}))
		bDone <- outcome{err}
	}()
	// While the first start holds the gate, the second must not have reached
	// Docker or returned a response.
	select {
	case got := <-bDone:
		t.Fatalf("second same-node start completed while the gate was held: %v", got.err)
	default:
	}
	if n := gate.starts.Load(); n != 1 {
		t.Fatalf("Docker start calls while gate held = %d, want 1", n)
	}

	close(gate.release)

	select {
	case got := <-aDone:
		if got.err != nil {
			t.Fatalf("first start failed: %v", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first start did not finish after the Docker gate opened")
	}
	select {
	case got := <-bDone:
		if got.err == nil {
			t.Fatal("second same-node start was admitted and overcommitted the node")
		}
		connErr, ok := got.err.(*connect.Error)
		if !ok || connErr.Code() != connect.CodeResourceExhausted {
			t.Fatalf("second start error = %v, want CodeResourceExhausted", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second start did not settle after the first released the gate")
	}

	gotA, err := svc.store.GetServer(ctx, serverA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotA.Status != db.StatusStarting || gotA.LastStarted == nil {
		t.Fatalf("admitted start state = %+v, want starting with LastStarted", gotA)
	}
	gotB, err := svc.store.GetServer(ctx, serverB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotB.Status != db.StatusStopped || gotB.LastStarted != nil {
		t.Fatalf("refused start changed server state = %+v", gotB)
	}
	if n := gate.starts.Load(); n != 1 {
		t.Fatalf("Docker start calls = %d, want only the admitted start", n)
	}

	// Journal kinds and event ownership stay with their existing owners: the
	// admitted start writes exactly one "start" row; the refused start writes
	// none, and the RPC emits no SERVER_START for either (the reconciler owns
	// that announcement on the normal start path).
	ops, err := svc.store.ListServerOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	startRows := map[string]int{}
	for _, op := range ops {
		if op.Kind == "start" {
			startRows[op.ServerID]++
		}
	}
	if startRows[serverA.ID] != 1 || startRows[serverB.ID] != 0 {
		t.Fatalf("start journal rows = %v, want one for A and none for B", startRows)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	for _, e := range seen {
		if e.Type == v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_START {
			t.Fatalf("RPC emitted SERVER_START for %s; that event is reconciler-owned", e.ServerID)
		}
	}
}

// TestConcurrentStartsOnDifferentNodesProceed proves the per-node gate does not
// serialize unrelated nodes: a second-node start finishes while a first-node
// start is still blocked inside Docker.
func TestConcurrentStartsOnDifferentNodesProceed(t *testing.T) {
	ctx := context.Background()
	gate := &admissionStartGate{
		blockID: "container-a",
		entered: make(chan string, 1),
		release: make(chan struct{}),
	}
	// A defer (not t.Cleanup) runs before the httptest server closes, so a
	// blocked Docker handler is always released before Close waits on it.
	defer func() {
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	}()
	svc, gate := newAdmissionTestService(t, gate, "test-node", "other-node")
	if err := svc.store.CreateNode(ctx, &db.Node{ID: "other-node", Name: "Other", Host: "tcp://127.0.0.1:2375", Enabled: true, Status: db.NodeStatusOnline}); err != nil {
		t.Fatal(err)
	}
	serverA := addAdmissionServer(t, svc, "parallel-a", "test-node", "container-a", 3072)
	serverB := addAdmissionServer(t, svc, "parallel-b", "other-node", "container-b", 3072)

	aDone := make(chan error, 1)
	bDone := make(chan error, 1)
	go func() {
		_, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: serverA.ID}))
		aDone <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first-node start never reached the Docker gate")
	}
	go func() {
		_, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: serverB.ID}))
		bDone <- err
	}()

	select {
	case err := <-bDone:
		if err != nil {
			t.Fatalf("different-node start was blocked or refused: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("different-node start waited on an unrelated node's gate")
	}
	// The different-node start finished while this gate is still held; only
	// now let the first-node start finish.
	close(gate.release)
	select {
	case err := <-aDone:
		if err != nil {
			t.Fatalf("first-node start failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first-node start did not settle")
	}

	for _, id := range []string{serverA.ID, serverB.ID} {
		got, err := svc.store.GetServer(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != db.StatusStarting || got.LastStarted == nil {
			t.Fatalf("server %s state = %+v, want starting with LastStarted", id, got)
		}
	}
}

// TestAdmissionWaitCancellationRefusesBeforeDockerMutation proves a request
// that is cancelled while queued on the shared gate never reaches Docker or
// mutates server state, and maps the cancellation to a Connect deadline.
func TestAdmissionWaitCancellationRefusesBeforeDockerMutation(t *testing.T) {
	ctx := context.Background()
	gate := &admissionStartGate{
		blockID: "container-a",
		entered: make(chan string, 1),
		release: make(chan struct{}),
	}
	// A defer (not t.Cleanup) runs before the httptest server closes, so a
	// blocked Docker handler is always released before Close waits on it.
	defer func() {
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	}()
	svc, gate := newAdmissionTestService(t, gate, "test-node")
	serverA := addAdmissionServer(t, svc, "admission-holder", "test-node", "container-a", 1024)
	serverB := addAdmissionServer(t, svc, "admission-waiter", "test-node", "container-b", 1024)

	holderDone := make(chan error, 1)
	go func() {
		_, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: serverA.ID}))
		holderDone <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("holder start never reached the Docker gate")
	}

	waitCtx, cancel := context.WithTimeout(ctxWithRole("operator"), 120*time.Millisecond)
	defer cancel()
	_, err := svc.StartServer(waitCtx, connect.NewRequest(&v1.StartServerRequest{Id: serverB.ID}))
	if err == nil {
		t.Fatal("cancelled waiter succeeded")
	}
	connErr, ok := err.(*connect.Error)
	if !ok || connErr.Code() != connect.CodeDeadlineExceeded {
		t.Fatalf("cancelled waiter error = %v, want CodeDeadlineExceeded", err)
	}
	if n := gate.starts.Load(); n != 1 {
		t.Fatalf("Docker start calls = %d, want only the holder", n)
	}
	got, getErr := svc.store.GetServer(ctx, serverB.ID)
	if getErr != nil || got.Status != db.StatusStopped || got.LastStarted != nil {
		t.Fatalf("cancelled waiter changed server = %+v, %v", got, getErr)
	}

	close(gate.release)
	select {
	case err := <-holderDone:
		if err != nil {
			t.Fatalf("holder start failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("holder start did not settle")
	}
}
