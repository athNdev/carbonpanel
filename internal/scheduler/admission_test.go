// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

func TestScheduledLifecycleMemoryAdmission(t *testing.T) {
	for _, tc := range []struct {
		name      string
		restart   bool
		memory    int
		limit     int64
		infoFails bool
		wantError string
	}{
		{name: "oversized start", memory: 6144, wantError: "lacks memory capacity"},
		{name: "oversized restart refuses before stopping", restart: true, memory: 6144, wantError: "lacks memory capacity"},
		{name: "operator node limit", memory: 3072, limit: 2048, wantError: "lacks memory capacity"},
		{name: "unverifiable capacity", memory: 1024, infoFails: true, wantError: "cannot verify memory"},
		{name: "admitted start", memory: 1024},
		{name: "restart counts existing reservation once", restart: true, memory: 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := setupTestStore(t)
			t.Cleanup(func() { _ = store.Close() })
			if err := store.CreateNode(ctx, &storage.Node{ID: "node", Name: "Node", IsLocal: true, Enabled: true, MaxMemoryMB: tc.limit}); err != nil {
				t.Fatal(err)
			}
			status := storage.StatusStopped
			if tc.restart {
				status = storage.StatusRunning
			}
			server := &storage.Server{ID: "srv", NodeID: "node", Name: "Server", ContainerID: "container", Memory: tc.memory, Status: status}
			if err := store.CreateServer(ctx, server); err != nil {
				t.Fatal(err)
			}
			var mutations atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/_ping":
					w.Header().Set("Api-Version", "1.45")
					w.Header().Set("Ostype", "linux")
				case strings.HasSuffix(r.URL.Path, "/info"):
					if tc.infoFails {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"MemTotal":4294967296}`))
				case strings.HasSuffix(r.URL.Path, "/json"):
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"Id":"container","State":{"Running":true,"Status":"running"}}`))
				case strings.HasSuffix(r.URL.Path, "/wait"):
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"StatusCode":0}`))
				case r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/start") || strings.HasSuffix(r.URL.Path, "/stop")):
					mutations.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(api.Close)
			client, err := docker.NewClient("tcp://"+strings.TrimPrefix(api.URL, "http://"), logger.New())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			s := NewScheduler(store, client, nil, nil, nil, logger.New())
			var out string
			if tc.restart {
				out, err = s.executeRestartTask(ctx, server, nil)
			} else {
				out, err = s.executeStartTask(ctx, server, nil)
			}
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("output=%q error=%v, want %q", out, err, tc.wantError)
				}
				if mutations.Load() != 0 {
					t.Fatal("rejected admission mutated Docker")
				}
				got, getErr := store.GetServer(ctx, server.ID)
				if getErr != nil || got.Status != status || got.LastStarted != nil {
					t.Fatalf("rejected admission changed server: %+v, %v", got, getErr)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				want := int32(1)
				if tc.restart {
					want = 2
				}
				if mutations.Load() != want {
					t.Fatalf("mutations=%d, want %d", mutations.Load(), want)
				}
			}
		})
	}
}
