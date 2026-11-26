// SPDX-License-Identifier: MPL-2.0

package services

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/serverruntime"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/google/uuid"
)

type worldSwapObserver struct {
	nodestorage.Accessor
	rename func(string, string) error
}

func (a *worldSwapObserver) Rename(from, to string) error {
	if err := a.rename(from, to); err != nil {
		return err
	}
	return a.Accessor.Rename(from, to)
}

func TestBindWorldRunningContainerOwnership(t *testing.T) {
	exerciseWorldBindLifecycle(t, fixtureWorldDocker)
}

func TestBindWorldRunningContainerLiveDocker(t *testing.T) {
	if os.Getenv("CARBONPANEL_TEST_WORLD_DOCKER") != "1" {
		t.Skip("set CARBONPANEL_TEST_WORLD_DOCKER=1 to use a disposable local Docker container")
	}
	exerciseWorldBindLifecycle(t, liveWorldDocker)
}

func exerciseWorldBindLifecycle(t *testing.T, daemon func(*testing.T, string) (*docker.Client, string)) {
	t.Helper()
	for _, failPromotion := range []bool{false, true} {
		name := "publish"
		if failPromotion {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			ctx := ctxWithRole("admin")
			store := newWorldRPCStore(t)
			root := t.TempDir()
			cfg := &config.Config{Storage: config.StorageConfig{DataDir: filepath.Join(root, "controller")}}
			libraryRoot := WorldLibraryRoot(cfg)
			server, revision := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
			seedLiveWorldFamilies(t, server.DataPath, revision.StoragePath)
			client, containerID := daemon(t, server.DataPath)
			// Leave the database status stale to prove that the binder uses Docker.
			server.ContainerID = containerID
			server.Memory = 512
			if err := store.UpdateServer(ctx, server); err != nil {
				t.Fatal(err)
			}

			log := logger.New()
			pool := docker.NewClientPool(store, client, log)
			pool.RegisterClient(server.NodeID, client)
			t.Cleanup(func() { _ = pool.Close() })
			serverService := &ServerService{
				store: store, docker: client, pool: pool, log: log,
				admission:    serverruntime.NewAdmissionCoordinator(),
				hostMemoryMB: func(context.Context, string) (int64, error) { return 4096, nil },
			}
			enforcer, err := rbac.NewEnforcer(store.DB())
			if err != nil {
				t.Fatal(err)
			}
			if err := enforcer.SeedDefaultPolicies(false); err != nil {
				t.Fatal(err)
			}
			resolver := nodestorage.NewResolver(nil)
			world := NewWorldService(store, cfg, log, resolver, nil, libraryRoot)
			adapter := newWorldLifecycleAdapter(serverService, store, enforcer)
			world.binder = NewWorldBinder(store, cfg, log, resolver, nil, serverService, enforcer)

			// Neither grants nor an existing journal operation lets a caller
			// invoke the internal power path without the mutation capability.
			if _, err := adapter.IsRunning(ctxWithRole("viewer"), server.ID); err == nil {
				t.Fatal("viewer was allowed to prepare a running-world transition")
			}

			checked := false
			world.binder.SetAccessorDecorator(func(acc nodestorage.Accessor) nodestorage.Accessor {
				return &worldSwapObserver{Accessor: acc, rename: func(from, to string) error {
					if to == "world" && strings.HasPrefix(from, ".world-library/stage-") {
						checked = true
						status, err := client.GetContainerStatus(ctx, containerID)
						if err != nil || status != storage.StatusStopped {
							t.Fatalf("promotion ran before Docker stopped: %s, %v", status, err)
						}
						if _, err := serverService.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID})); connect.CodeOf(err) != connect.CodeAborted {
							t.Fatalf("public start during promotion = %v, want Aborted", err)
						}
						if _, err := serverService.StopServer(ctx, connect.NewRequest(&v1.StopServerRequest{Id: server.ID})); connect.CodeOf(err) != connect.CodeAborted {
							t.Fatalf("public stop during promotion = %v, want Aborted", err)
						}
						if err := adapter.StartServer(ctx, server.ID); err == nil {
							t.Fatal("internal start without the bind capability succeeded")
						}
					}
					if failPromotion && to == "world_nether" && strings.HasPrefix(from, ".world-library/stage-") {
						return errors.New("injected nether promotion failure")
					}
					return nil
				}}
			})

			out, err := world.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
				ServerId: server.ID, LevelName: "world", RevisionId: revision.ID, RequestId: "world-lifecycle-" + name,
			}))
			if !checked {
				t.Fatalf("bind never reached promotion: %+v, %v", out, err)
			}
			expectPrefix, expectPhase := "new-", storage.ServerOperationComplete
			if failPromotion {
				if err == nil || !strings.Contains(err.Error(), "injected nether promotion failure") {
					t.Fatalf("rollback error = %v, want promotion fault", err)
				}
				expectPrefix, expectPhase = "old-", storage.ServerOperationFailed
			} else if err != nil || out.Msg.Outcome != "success" {
				t.Fatalf("bind = %+v, %v, want success", out, err)
			}
			for _, family := range []string{"world", "world_nether"} {
				bytes, err := os.ReadFile(filepath.Join(server.DataPath, family, "level.dat"))
				if err != nil || string(bytes) != expectPrefix+family {
					t.Fatalf("%s after %s = %q, %v", family, name, bytes, err)
				}
			}
			state, err := client.ObserveContainer(ctx, containerID)
			if err != nil || !state.Running {
				t.Fatalf("original container was not restarted: %+v, %v", state, err)
			}
			ops, err := store.ListRecentServerOperations(ctx, server.ID, 4)
			if err != nil || len(ops) != 1 || ops[0].Kind != "world-bind" ||
				ops[0].Phase != expectPhase || ops[0].ActiveServer != nil {
				t.Fatalf("lifecycle used a different lock or left it active: %+v, %v", ops, err)
			}
		})
	}
}

func seedLiveWorldFamilies(t *testing.T, dataRoot, archivePath string) {
	t.Helper()
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archive.Close() }()
	zw := zip.NewWriter(archive)
	for _, family := range []string{"world", "world_nether"} {
		if err := os.MkdirAll(filepath.Join(dataRoot, family), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataRoot, family, "level.dat"), []byte("old-"+family), 0o644); err != nil {
			t.Fatal(err)
		}
		entry, err := zw.Create(family + "/level.dat")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("new-" + family)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func fixtureWorldDocker(t *testing.T, _ string) (*docker.Client, string) {
	t.Helper()
	const id = "world-container"
	var running atomic.Bool
	running.Store(true)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.URL.Path == "/_ping":
			w.Header().Set("Api-Version", "1.45")
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/containers/"+id+"/json"):
			status := "exited"
			if running.Load() {
				status = "running"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": id, "State": map[string]any{"Status": status, "Running": status == "running"},
			})
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/containers/"+id+"/stop"):
			running.Store(false)
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/containers/"+id+"/start"):
			running.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(api.Close)
	client, err := docker.NewClient("tcp://"+strings.TrimPrefix(api.URL, "http://"), logger.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, id
}

func liveWorldDocker(t *testing.T, dataRoot string) (*docker.Client, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const socket = "unix:///var/run/docker.sock"
	const image = "eclipse-temurin:21-jre-jammy"
	name := "carbonpanel-world-bind-fixture-" + uuid.NewString()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cleanupCtx, "docker", "--host", socket, "rm", "-f", name)
		if result, err := cmd.CombinedOutput(); err != nil && !strings.Contains(string(result), "No such container") {
			t.Errorf("remove disposable Docker fixture: %v\n%s", err, result)
		}
	})
	output, err := exec.CommandContext(ctx, "docker", "--host", socket, "create", "--pull=never",
		"--name", name, "--network", "none", "--read-only", "--cap-drop", "ALL",
		"--label", "carbonpanel.test=world-bind-lifecycle",
		"--mount", "type=bind,src="+dataRoot+",dst=/worlds,readonly", "--entrypoint", "/bin/sh", image,
		"-c", `test -r /worlds/world/level.dat && test -r /worlds/world_nether/level.dat || exit 2
		trap 'exit 0' TERM
		while :; do sleep 1; done`).CombinedOutput()
	if err != nil {
		t.Fatalf("create disposable Docker fixture: %v\n%s", err, output)
	}
	id := strings.TrimSpace(string(output))
	if result, err := exec.CommandContext(ctx, "docker", "--host", socket, "start", id).CombinedOutput(); err != nil {
		t.Fatalf("start disposable Docker fixture: %v\n%s", err, result)
	}
	client, err := docker.NewClient(socket, logger.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, id
}
