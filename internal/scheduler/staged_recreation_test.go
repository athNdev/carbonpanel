// SPDX-License-Identifier: MPL-2.0
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/staging"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"gorm.io/gorm"
)

func TestPendingStagedRecreationKeepsAppliedConfigWhenIdentitySaveFails(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &db.Server{ID: "server", Name: "Server", NodeID: "node", ContainerID: "original", Status: db.StatusStopped}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StageConfigChange(ctx, server.ID, map[string]any{"motd": "applied spec"}, db.StagedApplyOnRestart, ""); err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("identity save unavailable")
	if err := store.DB().Callback().Update().Before("gorm:update").Register("test:fail_identity_save", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "servers" {
			_ = tx.AddError(persistErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	client := &fakeLifecycleDocker{result: &docker.RecreateContainerResult{NewContainerID: "replacement"}}
	handled, err := recreateForPendingStagedConfig(ctx, store, client, nil, server, true, true, nil)
	if !handled || !errors.Is(err, persistErr) || len(client.starts) != 0 {
		t.Fatalf("handled=%v error=%v starts=%v", handled, err, client.starts)
	}
	pending, err := HasPendingRestartStagedConfigRecreate(ctx, store, server.ID, server.LastStarted)
	if err != nil || !pending {
		t.Fatalf("pending recovery marker lost: %v, %v", pending, err)
	}
	cfg, err := store.GetServerConfig(ctx, server.ID)
	if err != nil || cfg.MOTD == nil || *cfg.MOTD != "applied spec" {
		t.Fatalf("applied config was undone after successful Docker recreation: %+v, %v", cfg, err)
	}
}

func TestStagedPublishPersistsRecreationRecoveryIdentity(t *testing.T) {
	for _, removeFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "rolled back original", true: "partial replacement"}[removeFails], func(t *testing.T) {
			ctx := context.Background()
			store := setupTestStore(t)
			t.Cleanup(func() { _ = store.Close() })
			if err := store.CreateNode(ctx, &db.Node{ID: "node", Name: "Node", IsLocal: true, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			lastStarted := time.Now().Add(-time.Hour).Truncate(time.Second)
			server := &db.Server{ID: "server", Name: "Server", NodeID: "node", Port: 25565, Memory: 1024, ModLoader: db.ModLoaderAutoCurseForge, DataPath: t.TempDir(), ContainerID: "original", Status: db.StatusRunning, LastStarted: &lastStarted}
			if err := store.CreateServer(ctx, server); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)); err != nil {
				t.Fatal(err)
			}
			cf, token := "fixture-global-cf", "fixture-global-modrinth"
			if err := store.UpdateGlobalSettings(ctx, &db.ServerConfig{CFAPIKey: &cf, ModrinthToken: &token}); err != nil {
				t.Fatal(err)
			}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/_ping":
					w.Header().Set("Api-Version", "1.45")
					w.Header().Set("Ostype", "linux")
				case strings.HasSuffix(r.URL.Path, "/info"):
					_, _ = w.Write([]byte(`{"MemTotal":4294967296}`))
				case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
					_, _ = w.Write([]byte(`{"Id":"sha256:fixture"}`))
				case strings.HasSuffix(r.URL.Path, "/containers/original/json"):
					_, _ = w.Write([]byte(`{"Id":"original","Name":"/original","State":{"Running":true,"Status":"running"}}`))
				case strings.HasSuffix(r.URL.Path, "/containers/create"):
					var request struct{ Env []string }
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("decode container config: %v", err)
					}
					env := "\n" + strings.Join(request.Env, "\n") + "\n"
					for _, want := range []string{"CF_API_KEY=" + cf, "MODRINTH_TOKEN=" + token, "CF_SLUG=server"} {
						if !strings.Contains(env, "\n"+want+"\n") {
							t.Error("staged container omitted inherited runtime provider configuration")
						}
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"Id":"replacement"}`))
				case strings.HasSuffix(r.URL.Path, "/containers/replacement/start"):
					http.Error(w, `{"message":"replacement start failed"}`, http.StatusInternalServerError)
				case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/replacement") && removeFails:
					http.Error(w, `{"message":"replacement removal failed"}`, http.StatusInternalServerError)
				case strings.HasSuffix(r.URL.Path, "/wait"):
					_, _ = w.Write([]byte(`{"StatusCode":0}`))
				case r.Method == http.MethodPost || r.Method == http.MethodDelete:
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
			err = scheduler.publishStagedBatch(ctx, server, staging.Plan{Recreate: true, StartAfter: true})
			if err == nil || !strings.Contains(err.Error(), "replacement start failed") {
				t.Fatalf("expected recreation error, got %v", err)
			}
			got, err := store.GetServer(ctx, server.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantID := "original"
			if removeFails {
				wantID = "replacement"
			}
			if got.ContainerID != wantID || got.Status != db.StatusError || got.LastStarted == nil || !got.LastStarted.Equal(lastStarted) {
				t.Fatalf("recovery state = %+v, want ID %s, error status and original LastStarted", got, wantID)
			}
		})
	}
}
