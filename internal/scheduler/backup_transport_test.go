// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/worldstore"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

type recordingTransport struct {
	opened     int
	workerRoot string
}

func (r *recordingTransport) Open(_ context.Context, _ nodestorage.NodeInfo, _ string) (nodestorage.Accessor, error) {
	r.opened++
	return nodestorage.NewLocal(r.workerRoot)
}

func assertNoControllerLocalTree(t *testing.T, phantomRoot, dataPath string) {
	t.Helper()
	if _, err := os.Stat(phantomRoot); !os.IsNotExist(err) {
		t.Fatalf("controller created node storage_root %s: %v", phantomRoot, err)
	}
	mapped := filepath.Join(phantomRoot, filepath.Base(dataPath))
	if _, err := os.Stat(mapped); !os.IsNotExist(err) {
		t.Fatalf("controller-local mapped root %s exists: %v", mapped, err)
	}
}

func TestBackupTransportOnlyUsesForNodeNotControllerLocal(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.NewSQLiteStore(&appconfig.Config{
		Database: appconfig.DatabaseConfig{Path: filepath.Join(dir, "panel.sqlite")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	workerRoot := filepath.Join(t.TempDir(), "worker-data")
	if err := os.MkdirAll(filepath.Join(workerRoot, "world"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerRoot, "world", "level.dat"), []byte("from-worker"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerRoot, "server.properties"), []byte("motd=remote"), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := &recordingTransport{workerRoot: workerRoot}
	nodestorage.SetTransport(rt)
	t.Cleanup(func() { nodestorage.SetTransport(nil) })

	phantomRoot := filepath.Join(t.TempDir(), "phantom-node-root")
	ctx := context.Background()
	if err := store.CreateNode(ctx, &storage.Node{
		ID: "transport-node", Name: "transport-node", Host: "tcp://10.0.0.50:2375", Enabled: true,
		StorageRoot: phantomRoot,
	}); err != nil {
		t.Fatal(err)
	}
	server := &storage.Server{
		ID: "srv-transport", Name: "srv-transport", NodeID: "transport-node",
		DataPath: filepath.Join("var", "data", "servers", "srv-transport"),
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	s := &Scheduler{
		store: store,
		appConfig: &appconfig.Config{
			Storage: appconfig.StorageConfig{
				DataDir:   dir,
				BackupDir: filepath.Join(dir, "backups"),
			},
		},
		log: logger.New(),
	}
	out, err := s.executeBackupTask(ctx, server, &storage.ScheduledTask{Name: "nightly"})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if rt.opened == 0 {
		t.Fatal("expected backup to open remote transport")
	}
	if out == "" {
		t.Fatal("expected backup output")
	}
	destDir := filepath.Join(s.appConfig.Storage.BackupDir, filepath.Base(server.DataPath))
	matches, err := filepath.Glob(filepath.Join(destDir, "*.zip"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("backup zips = %v, %v", matches, err)
	}
	if !worldstore.HasWorldstoreManifest(matches[0]) {
		t.Fatal("default backup must be a worldstore manifest archive")
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)

	restored := filepath.Join(t.TempDir(), "restored")
	if err := os.MkdirAll(restored, 0o700); err != nil {
		t.Fatal(err)
	}
	acc, err := nodestorage.NewLocal(restored)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = acc.Close() })
	if err := worldstore.RestoreFromAccessor(acc, matches[0]); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(restored, "world", "level.dat"))
	if err != nil || string(got) != "from-worker" {
		t.Fatalf("restored world = %q, %v", got, err)
	}
}

func TestBackupTransportOnlyWithoutProviderCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.NewSQLiteStore(&appconfig.Config{
		Database: appconfig.DatabaseConfig{Path: filepath.Join(dir, "panel.sqlite")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	nodestorage.SetTransport(nil)

	phantomRoot := filepath.Join(t.TempDir(), "phantom-node-root")
	ctx := context.Background()
	if err := store.CreateNode(ctx, &storage.Node{
		ID: "transport-bare", Name: "transport-bare", Host: "tcp://10.0.0.51:2375", Enabled: true,
		StorageRoot: phantomRoot,
	}); err != nil {
		t.Fatal(err)
	}
	server := &storage.Server{
		ID: "srv-tbare", Name: "srv-tbare", NodeID: "transport-bare",
		DataPath: filepath.Join("var", "data", "servers", "srv-tbare"),
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{
		store: store,
		appConfig: &appconfig.Config{
			Storage: appconfig.StorageConfig{
				DataDir:   dir,
				BackupDir: filepath.Join(dir, "backups"),
			},
		},
		log: logger.New(),
	}
	_, err = s.executeBackupTask(ctx, server, &storage.ScheduledTask{Name: "nightly"})
	if !nodestorage.IsUnavailable(err) {
		t.Fatalf("backup error = %v, want unavailable", err)
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
}
