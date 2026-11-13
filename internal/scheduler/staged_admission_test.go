// SPDX-License-Identifier: MPL-2.0
package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/staging"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

func TestStagedPublishRefusesBeforeLocalMutation(t *testing.T) {
	for _, fixture := range []struct {
		name            string
		remote, refused bool
		memory          int
	}{
		{"unresolvable remote", true, true, 6144},
		{"overcommitted local", false, true, 6144},
		{"admitted local", false, false, 1024},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			store := setupTestStore(t)
			t.Cleanup(func() { _ = store.Close() })
			if err := store.CreateNode(ctx, &db.Node{ID: "node", Name: "Node", IsLocal: !fixture.remote, Host: "tcp://remote.invalid:2375", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			server := &db.Server{ID: "server", Name: "Server", NodeID: "node", Memory: fixture.memory, ContainerID: "owned-container", Status: db.StatusRunning}
			if err := store.CreateServer(ctx, server); err != nil {
				t.Fatal(err)
			}
			var mutations atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/_ping":
					w.Header().Set("Api-Version", "1.45")
					w.Header().Set("Ostype", "linux")
				case strings.HasSuffix(r.URL.Path, "/info"):
					_, _ = w.Write([]byte(`{"MemTotal":4294967296}`))
				case strings.HasSuffix(r.URL.Path, "/json"):
					_, _ = w.Write([]byte(`{"Id":"owned-container","State":{"Running":true,"Status":"running"}}`))
				case strings.HasSuffix(r.URL.Path, "/wait"):
					_, _ = w.Write([]byte(`{"StatusCode":0}`))
				case r.Method == http.MethodPost:
					mutations.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer api.Close()
			client, err := docker.NewClient("tcp://"+strings.TrimPrefix(api.URL, "http://"), logger.New())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			scheduler := NewScheduler(store, client, nil, nil, nil, logger.New())
			err = scheduler.publishStagedBatch(ctx, server, staging.Plan{Restart: true, StartAfter: true})
			if fixture.refused && (err == nil || mutations.Load() != 0) {
				t.Fatalf("refused publish: error=%v local mutations=%d", err, mutations.Load())
			}
			if !fixture.refused && (err != nil || mutations.Load() == 0) {
				t.Fatalf("admitted publish: error=%v mutations=%d", err, mutations.Load())
			}
			got, err := store.GetServer(ctx, server.ID)
			if err != nil || got.Status != db.StatusRunning || got.LastStarted != nil {
				t.Fatalf("refusal changed server: %+v, %v", got, err)
			}
		})
	}
}
