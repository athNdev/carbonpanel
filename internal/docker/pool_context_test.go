// SPDX-License-Identifier: MPL-2.0
package docker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	models "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/moby/moby/api/types/container"
)

func TestContainerNetworkRepairUsesOperationContext(t *testing.T) {
	for _, action := range []string{"start", "create"} {
		t.Run(action, func(t *testing.T) {
			entered := make(chan struct{})
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/_ping":
					w.Header().Set("Api-Version", "1.45")
					w.Header().Set("Ostype", "linux")
				case strings.HasSuffix(r.URL.Path, "/networks"):
					close(entered)
					<-r.Context().Done()
				default:
					http.Error(w, `{"message":"network carbon-panel-network not found"}`, http.StatusNotFound)
				}
			}))
			defer api.Close()
			client, err := NewClient(fakeDockerHost(t, api), logger.New())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				if action == "start" {
					err = client.StartContainer(ctx, "fixture")
				} else {
					_, err = client.createContainerWithConflictRetry(ctx, &container.Config{Image: "fixture"}, &container.HostConfig{}, nil, "fixture", nil)
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("network repair did not start")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("network repair lost cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("network repair ignored cancellation")
			}
		})
	}
}

func TestColdNodeResolutionCancelsDuringVersionNegotiation(t *testing.T) {
	for _, throughStorage := range []bool{false, true} {
		t.Run(map[bool]string{false: "pool", true: "storage transport"}[throughStorage], func(t *testing.T) {
			entered := make(chan struct{})
			var once sync.Once
			var otherCalls atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/_ping" {
					otherCalls.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				once.Do(func() { close(entered) })
				<-r.Context().Done()
			}))
			defer api.Close()
			store := newMockNodeStore(&models.Node{ID: "remote", Host: fakeDockerHost(t, api)})
			pool := NewClientPool(store, nil, logger.New())
			defer func() { _ = pool.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			dataRoot := t.TempDir()
			go func() {
				var err error
				if throughStorage {
					_, err = NewStorageTransport(pool, logger.New(), "fixture-worker").Open(ctx, nodestorage.NodeInfo{ID: "remote", Name: "Remote", StorageRoot: "/remote-world"}, dataRoot)
				} else {
					_, err = pool.GetClientStrictContext(ctx, "remote")
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("cold client did not enter negotiation")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("canceled lookup waited for the three-second negotiation budget")
			}
			pool.mu.RLock()
			cached := pool.clients["remote"]
			pool.mu.RUnlock()
			if cached != nil || otherCalls.Load() != 0 {
				t.Fatalf("canceled construction cached a client or started network setup: cached=%v calls=%d", cached != nil, otherCalls.Load())
			}
		})
	}
}

type contextBlockedNodeStore struct {
	*mockNodeStore
	entered chan struct{}
}

func (s *contextBlockedNodeStore) GetNode(ctx context.Context, _ string) (*models.Node, error) {
	close(s.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestNodeEndpointLookupUsesOperationContext(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold", true: "cached endpoint validation"}[cached], func(t *testing.T) {
			store := &contextBlockedNodeStore{mockNodeStore: newMockNodeStore(), entered: make(chan struct{})}
			pool := NewClientPool(store, nil, logger.New())
			if cached {
				pool.clients["remote"] = &Client{}
				pool.nodeConfigs["remote"] = &models.Node{ID: "remote", Host: "tcp://fixture.invalid:2375"}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := pool.GetClientStrictContext(ctx, "remote"); done <- err }()
			select {
			case <-store.entered:
			case <-time.After(time.Second):
				t.Fatal("lookup did not enter store")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lost store cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("store lookup ignored cancellation")
			}
		})
	}
}

func TestLocatorCancellationDoesNotRetryOnLocalClient(t *testing.T) {
	for _, executor := range []bool{false, true} {
		t.Run(map[bool]string{false: "client", true: "executor"}[executor], func(t *testing.T) {
			store := &contextBlockedNodeStore{mockNodeStore: newMockNodeStore(), entered: make(chan struct{})}
			pool := NewClientPool(store, &Client{}, logger.New())
			locator := NewLocator(pool, &Client{}, store)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if executor {
					value, err := locator.ExecutorFor(ctx, "remote")
					if value != nil {
						t.Error("canceled lookup returned executor")
					}
					done <- err
				} else {
					value, err := locator.For(ctx, "remote")
					if value != nil {
						t.Error("canceled lookup returned client")
					}
					done <- err
				}
			}()
			select {
			case <-store.entered:
			case <-time.After(time.Second):
				t.Fatal("locator did not enter node lookup")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("locator lost cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("locator ignored cancellation")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	locator := NewLocator(nil, &Client{}, nil)
	if value, err := locator.For(ctx, "local"); value != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled local client=%v error=%v", value, err)
	}
	if value, err := locator.ExecutorFor(ctx, "local"); value != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled local executor=%v error=%v", value, err)
	}
}
