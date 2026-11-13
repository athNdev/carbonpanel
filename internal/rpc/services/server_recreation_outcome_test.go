// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/events"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func TestRestartFallbackHonorsStoppedReplacementStartIntent(t *testing.T) {
	for _, failure := range []string{"", "start", "canceled start", "status save"} {
		name := failure
		if name == "" {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(ctxWithRole("operator"))
			defer cancel()
			var replacementStarts atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/_ping":
					w.Header().Set("Api-Version", "1.45")
					w.Header().Set("Ostype", "linux")
				case strings.HasSuffix(r.URL.Path, "/info"):
					_, _ = w.Write([]byte(`{"MemTotal":17179869184}`))
				case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
					_, _ = w.Write([]byte(`{"Id":"sha256:fixture"}`))
				case strings.HasSuffix(r.URL.Path, "/containers/json"):
					_, _ = w.Write([]byte(`[]`))
				case strings.HasSuffix(r.URL.Path, "/containers/original/json"):
					_, _ = w.Write([]byte(`{"Id":"original","Name":"/original","State":{"Running":false,"Status":"exited"}}`))
				case strings.HasSuffix(r.URL.Path, "/containers/create"):
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"Id":"replacement"}`))
				case strings.HasSuffix(r.URL.Path, "/containers/original/start"):
					http.Error(w, `{"message":"original start failed"}`, http.StatusInternalServerError)
				case strings.HasSuffix(r.URL.Path, "/containers/replacement/start"):
					replacementStarts.Add(1)
					if failure == "canceled start" {
						cancel()
					}
					if failure == "start" || failure == "canceled start" {
						http.Error(w, `{"message":"replacement start failed"}`, http.StatusInternalServerError)
					} else {
						w.WriteHeader(http.StatusNoContent)
					}
				case strings.HasSuffix(r.URL.Path, "/wait"):
					_, _ = w.Write([]byte(`{"StatusCode":0}`))
				case r.Method == http.MethodPost || r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer api.Close()
			svc, _ := newDockerPrivilegeTestService(t)
			_ = svc.docker.Close()
			client, err := docker.NewClient(strings.Replace(api.URL, "http://", "tcp://", 1), svc.log)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			svc.docker, svc.pool = client, nil
			ensureLocalTestNode(t, svc.store, "test-node")
			started := time.Now().Add(-time.Hour).Truncate(time.Second)
			server := &storage.Server{ID: "server", Name: "Server", NodeID: "test-node", ContainerID: "original", Port: 25751, Memory: 1024, Status: storage.StatusStopped, DataPath: t.TempDir(), LastStarted: &started}
			if err := svc.store.CreateServer(ctx, server); err != nil {
				t.Fatal(err)
			}
			if err := svc.store.SaveServerConfig(ctx, svc.store.CreateDefaultServerConfig(server.ID)); err != nil {
				t.Fatal(err)
			}
			if failure == "status save" {
				if err := svc.store.DB().Exec(`CREATE TRIGGER fail_starting BEFORE UPDATE OF status ON servers WHEN NEW.status = 'starting' BEGIN SELECT RAISE(ABORT, 'status save unavailable'); END`).Error; err != nil {
					t.Fatal(err)
				}
			}
			emitted := 0
			svc.bus = events.NewBus(svc.log)
			svc.bus.Subscribe(func(_ context.Context, e events.Event) {
				if e.Type == v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_RESTART {
					emitted++
				}
			})
			response, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID}))
			if replacementStarts.Load() != 1 {
				t.Fatalf("replacement start calls=%d, want1", replacementStarts.Load())
			}
			got, getErr := svc.store.GetServer(context.Background(), server.ID)
			if getErr != nil || got.ContainerID != "replacement" {
				t.Fatalf("replacement identity lost: %+v %v", got, getErr)
			}
			if failure == "" {
				if err != nil || response == nil || response.Msg.GetStatus() != "starting" || got.Status != storage.StatusStarting || !got.LastStarted.After(started) || emitted != 1 {
					t.Fatalf("successful fallback: response=%v state=%+v events=%d error=%v", response, got, emitted, err)
				}
			} else {
				if err == nil || emitted != 0 || !got.LastStarted.Equal(started) {
					t.Fatalf("failed fallback reported success: events=%d state=%+v error=%v", emitted, got, err)
				}
				if (failure == "start" || failure == "canceled start") && got.Status != storage.StatusError {
					t.Fatalf("failed start status=%s", got.Status)
				}
				if failure == "start" && (!strings.Contains(err.Error(), "original start failed") || !strings.Contains(err.Error(), "replacement start failed")) {
					t.Fatalf("lost fallback failure details: %v", err)
				}
			}
		})
	}
}

func TestLifecycleRPCPersistsPartialRecreationAfterCancellation(t *testing.T) {
	for _, operation := range []string{"update", "recreate", "restart fallback", "staged start", "staged restart", "proxy"} {
		for _, cancelRequest := range []bool{false, true} {
			name := operation
			if cancelRequest {
				name += " canceled"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(ctxWithRole("operator"))
				defer cancel()
				var created atomic.Bool
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch {
					case r.URL.Path == "/_ping":
						w.Header().Set("Api-Version", "1.45")
						w.Header().Set("Ostype", "linux")
					case strings.HasSuffix(r.URL.Path, "/info"):
						_, _ = w.Write([]byte(`{"MemTotal":17179869184}`))
					case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
						_, _ = w.Write([]byte(`{"Id":"sha256:fixture"}`))
					case strings.HasSuffix(r.URL.Path, "/containers/json"):
						_, _ = w.Write([]byte(`[]`))
					case strings.HasSuffix(r.URL.Path, "/containers/original/json"):
						_, _ = w.Write([]byte(`{"Id":"original","Name":"/original","State":{"Running":true,"Status":"running"}}`))
					case strings.HasSuffix(r.URL.Path, "/containers/create"):
						created.Store(true)
						w.WriteHeader(http.StatusCreated)
						_, _ = w.Write([]byte(`{"Id":"partial"}`))
					case strings.HasSuffix(r.URL.Path, "/containers/partial/start"):
						if cancelRequest {
							cancel()
						}
						http.Error(w, `{"message":"partial start failed"}`, http.StatusInternalServerError)
					case strings.HasSuffix(r.URL.Path, "/containers/original/start"):
						http.Error(w, `{"message":"original start failed"}`, http.StatusInternalServerError)
					case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/partial"):
						http.Error(w, `{"message":"partial removal failed"}`, http.StatusInternalServerError)
					case strings.HasSuffix(r.URL.Path, "/wait"):
						_, _ = w.Write([]byte(`{"StatusCode":0}`))
					case r.Method == http.MethodPost:
						w.WriteHeader(http.StatusNoContent)
					default:
						http.NotFound(w, r)
					}
				}))
				defer api.Close()
				svc, _ := newDockerPrivilegeTestService(t)
				_ = svc.docker.Close()
				client, err := docker.NewClient(strings.Replace(api.URL, "http://", "tcp://", 1), svc.log)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = client.Close() }()
				svc.docker, svc.pool = client, nil
				ensureLocalTestNode(t, svc.store, "test-node")
				started := time.Now().Add(-time.Hour).Truncate(time.Second)
				server := &storage.Server{ID: "server", Name: "Server", NodeID: "test-node", ContainerID: "original", Port: 25751, Memory: 1024, Status: storage.StatusRunning, DataPath: t.TempDir(), LastStarted: &started}
				if operation == "staged start" {
					server.Status = storage.StatusStopped
				}
				if operation == "proxy" {
					server.ProxyHostname = "old.example"
				}
				if err := svc.store.CreateServer(ctx, server); err != nil {
					t.Fatal(err)
				}
				if err := svc.store.SaveServerConfig(ctx, svc.store.CreateDefaultServerConfig(server.ID)); err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(operation, "staged") {
					if _, err := svc.store.StageConfigChange(ctx, server.ID, map[string]any{"motd": "new spec"}, storage.StagedApplyOnRestart, ""); err != nil {
						t.Fatal(err)
					}
				}
				switch operation {
				case "update":
					_, err = svc.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{Id: server.ID, MaxPlayers: 30}))
				case "recreate":
					_, err = svc.RecreateServer(ctx, connect.NewRequest(&v1.RecreateServerRequest{Id: server.ID}))
				case "staged start":
					_, err = svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID}))
				case "proxy":
					pool := docker.NewClientPool(svc.store, client, svc.log)
					pool.RegisterClient(server.NodeID, client)
					proxy := NewProxyService(svc.store, client, pool, nil, svc.config, nil, svc.log)
					_, err = proxy.UpdateServerRouting(ctx, connect.NewRequest(&v1.UpdateServerRoutingRequest{ServerId: server.ID}))
				default:
					_, err = svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID}))
				}
				if err == nil || !created.Load() {
					t.Fatalf("expected failed partial creation, created=%v error=%v", created.Load(), err)
				}
				got, err := svc.store.GetServer(context.Background(), server.ID)
				if err != nil || got.ContainerID != "partial" || got.Status != storage.StatusError || got.LastStarted == nil || !got.LastStarted.Equal(started) {
					t.Fatalf("canonical recovery state lost: %+v, %v", got, err)
				}
			})
		}
	}
}
