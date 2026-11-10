// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	appconfig "github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/worldstore"
	"github.com/athNdev/carbon-panel/pkg/files"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func TestRestoreBackupTransportOnlyUsesForNode(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "u-admin", Username: "admin", Roles: []string{"admin"}})

	workerRoot := filepath.Join(t.TempDir(), "worker-data")
	if err := os.MkdirAll(workerRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerRoot, "stale.txt"), []byte("left-over"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := &recordingTransport{workerRoot: workerRoot}
	nodestorage.SetTransport(rt)
	t.Cleanup(func() { nodestorage.SetTransport(nil) })

	phantomRoot := filepath.Join(t.TempDir(), "phantom-node-root")
	if err := store.CreateNode(ctx, &storage.Node{
		ID: "transport-restore", Name: "transport-restore", Host: "tcp://10.0.0.60:2375", Enabled: true,
		StorageRoot: phantomRoot,
	}); err != nil {
		t.Fatal(err)
	}
	server := &storage.Server{
		ID: "srv-restore-t", Name: "srv-restore-t", NodeID: "transport-restore",
		DataPath: filepath.Join("var", "data", "servers", "srv-restore-t"),
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "server.properties"), []byte("motd=restored"), 0o644); err != nil {
		t.Fatal(err)
	}
	acc, err := nodestorage.NewLocal(src)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "world.zip")
	if _, err := worldstore.BackupToAccessor(acc, archive, nil); err != nil {
		t.Fatal(err)
	}
	_ = acc.Close()
	sum, err := files.SHA256File(archive)
	if err != nil {
		t.Fatal(err)
	}
	rec := newBackupRecord("b-transport", server.ID, archive)
	rec.SHA256 = sum
	if err := store.CreateBackupRecord(ctx, rec); err != nil {
		t.Fatal(err)
	}

	svc := NewBackupService(store, nil, nil, appconfig.S3Config{}, logger.New())
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	_ = enforcer.SeedDefaultPolicies(false)
	svc.SetEnforcer(enforcer)

	openedBefore := rt.opened
	resp, err := svc.RestoreBackup(ctx, connect.NewRequest(&v1.RestoreBackupRequest{Id: rec.ID}))
	if err != nil || !resp.Msg.Success {
		t.Fatalf("restore: %v %+v", err, resp)
	}
	if rt.opened <= openedBefore {
		t.Fatal("expected restore to open remote transport")
	}
	got, err := os.ReadFile(filepath.Join(workerRoot, "server.properties"))
	if err != nil || string(got) != "motd=restored" {
		t.Fatalf("worker restore = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(workerRoot, "stale.txt")); !os.IsNotExist(err) {
		t.Fatal("stale worker file survived restore")
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
}

func TestRestoreBackupTransportOnlyWithoutProviderCreatesNothing(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "u-admin", Username: "admin", Roles: []string{"admin"}})
	nodestorage.SetTransport(nil)

	phantomRoot := filepath.Join(t.TempDir(), "phantom-node-root")
	if err := store.CreateNode(ctx, &storage.Node{
		ID: "transport-bare", Name: "transport-bare", Host: "tcp://10.0.0.61:2375", Enabled: true,
		StorageRoot: phantomRoot,
	}); err != nil {
		t.Fatal(err)
	}
	server := &storage.Server{
		ID: "srv-restore-bare", Name: "srv-restore-bare", NodeID: "transport-bare",
		DataPath: filepath.Join("var", "data", "servers", "srv-restore-bare"),
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "empty.zip")
	if err := os.WriteFile(archive, []byte("PK\x03\x04"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := newBackupRecord("b-bare", server.ID, archive)
	if err := store.CreateBackupRecord(ctx, rec); err != nil {
		t.Fatal(err)
	}

	svc := NewBackupService(store, nil, nil, appconfig.S3Config{}, logger.New())
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	_ = enforcer.SeedDefaultPolicies(false)
	svc.SetEnforcer(enforcer)

	_, err = svc.RestoreBackup(ctx, connect.NewRequest(&v1.RestoreBackupRequest{Id: rec.ID}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("restore error = %v, want unavailable", err)
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
}
