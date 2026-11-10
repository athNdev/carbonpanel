// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package snapshot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/google/uuid"
)

type recordingTransport struct {
	opened     int
	workerRoot string
}

func (r *recordingTransport) Open(_ context.Context, _ nodestorage.NodeInfo, _ string) (nodestorage.Accessor, error) {
	r.opened++
	return nodestorage.NewLocal(r.workerRoot)
}

func TestSnapshotTransportOnlyUsesForNodeNotControllerLocal(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	workerRoot := filepath.Join(t.TempDir(), "worker-data")
	if err := os.MkdirAll(workerRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerRoot, "server.properties"), []byte("from-worker"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := &recordingTransport{workerRoot: workerRoot}
	nodestorage.SetTransport(rt)
	t.Cleanup(func() { nodestorage.SetTransport(nil) })

	phantomRoot := filepath.Join(t.TempDir(), "phantom-node-root")
	if err := store.CreateNode(ctx, &storage.Node{
		ID: "transport-snap", Name: "transport-snap", Host: "tcp://10.4.0.8:2375", Enabled: true,
		StorageRoot: phantomRoot,
	}); err != nil {
		t.Fatal(err)
	}
	server := &storage.Server{
		ID: uuid.New().String(), Name: "snap-transport", NodeID: "transport-snap",
		DataPath: filepath.Join("var", "data", "servers", "snap-transport"), Status: storage.StatusStopped,
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	dockerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(dockerAPI.Close)
	cli, err := docker.NewClient(strings.Replace(dockerAPI.URL, "http://", "tcp://", 1), logger.New())
	if err != nil {
		t.Fatal(err)
	}
	pool := docker.NewClientPool(store, cli, logger.New())
	pool.RegisterClient("transport-snap", cli)
	t.Cleanup(func() { _ = pool.Close() })

	engine := NewEngineWithPool(store, cli, pool, nil, logger.New(), Config{BackupDir: filepath.Join(t.TempDir(), "backups")})
	snap, err := engine.CreatePreUpdateSnapshot(ctx, server, "mapped")
	if err != nil {
		t.Fatal(err)
	}
	if rt.opened == 0 {
		t.Fatal("expected snapshot create to open remote transport")
	}
	if _, err := os.Stat(phantomRoot); !os.IsNotExist(err) {
		t.Fatalf("controller created storage_root: %v", err)
	}

	if err := os.WriteFile(filepath.Join(workerRoot, "server.properties"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerRoot, "stale.txt"), []byte("extra"), 0o644); err != nil {
		t.Fatal(err)
	}
	openedBefore := rt.opened
	if err := engine.Rollback(ctx, server, snap.ID); err != nil {
		t.Fatal(err)
	}
	if rt.opened <= openedBefore {
		t.Fatal("expected rollback to open remote transport")
	}
	got, err := os.ReadFile(filepath.Join(workerRoot, "server.properties"))
	if err != nil || string(got) != "from-worker" {
		t.Fatalf("worker world = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(workerRoot, "stale.txt")); !os.IsNotExist(err) {
		t.Fatal("stale worker file survived rollback")
	}
	if _, err := os.Stat(phantomRoot); !os.IsNotExist(err) {
		t.Fatalf("controller-local tree after rollback: %v", err)
	}
}

func TestSnapshotTransportOnlyWithoutProviderCreatesNothing(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	nodestorage.SetTransport(nil)

	phantomRoot := filepath.Join(t.TempDir(), "phantom-node-root")
	if err := store.CreateNode(ctx, &storage.Node{
		ID: "transport-bare", Name: "transport-bare", Host: "tcp://10.4.0.9:2375", Enabled: true,
		StorageRoot: phantomRoot,
	}); err != nil {
		t.Fatal(err)
	}
	server := &storage.Server{
		ID: uuid.New().String(), Name: "snap-bare", NodeID: "transport-bare",
		DataPath: filepath.Join("var", "data", "servers", "snap-bare"), Status: storage.StatusStopped,
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(store, nil, nil, logger.New(), Config{BackupDir: filepath.Join(t.TempDir(), "backups")})
	if _, err := engine.CreatePreUpdateSnapshot(ctx, server, "bare"); !nodestorage.IsUnavailable(err) {
		t.Fatalf("create error = %v, want unavailable", err)
	}
	if _, err := os.Stat(phantomRoot); !os.IsNotExist(err) {
		t.Fatalf("controller created storage_root: %v", err)
	}
}
