// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFabricJar drops a minimal fabric mod jar with the given minecraft
// constraint into dir.
func writeFabricJar(t *testing.T, dir, name, constraint string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	writeTestZip(t, filepath.Join(dir, name), map[string]string{
		"fabric.mod.json": `{"schemaVersion":1,"id":"test","depends":{"minecraft":"` + constraint + `"}}`,
	})
}

// writeTestZip builds a tiny zip archive for compatibility fixtures.
func writeTestZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
}

func newVersionCompatService(t *testing.T, store *storage.Store, dockerClient *docker.Client, log *logger.Logger) *ServerService {
	t.Helper()
	return &ServerService{store: store, docker: dockerClient, config: &config.Config{}, log: log}
}

// TestUpdateServerRefusesIncompatibleVersionChange guards the pre-flight
// gate: a Minecraft version change that installed mods declare incompatible
// is refused with failed_precondition before the row is persisted, so a
// failed recreation can never leave the new version behind in the database.
func TestUpdateServerRefusesIncompatibleVersionChange(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	log := logger.New()
	dockerClient, err := docker.NewClient("tcp://127.0.0.1:1", log)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dockerClient.Close() })

	dataPath := t.TempDir()
	writeFabricJar(t, filepath.Join(dataPath, "mods"), "sodium-fabric-0.6.0.jar", ">=1.21 <1.22")

	server := &storage.Server{
		ID: "srv-compat-refuse", Name: "Compat Refuse", NodeID: "local",
		Port: 25591, Memory: 1024, MaxPlayers: 20,
		ModLoader: storage.ModLoaderFabric, MCVersion: "1.21.1",
		ContainerID: "tracked", Status: storage.StatusRunning,
		DataPath: dataPath,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))

	svc := newVersionCompatService(t, store, dockerClient, log)
	_, err = svc.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{
		Id: server.ID, McVersion: "1.20.1",
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "error = %v", err)
	assert.Contains(t, err.Error(), "incompatible with Minecraft 1.20.1")
	assert.Contains(t, err.Error(), "sodium-fabric-0.6.0.jar")
	assert.Contains(t, err.Error(), "requires >=1.21 <1.22")

	// The refusal happens before any mutation: the row still carries the old
	// version.
	updated, err := store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	assert.Equal(t, "1.21.1", updated.MCVersion)
}

func TestUpdateServerRefusesIncompatibleVersionChangeUsingDestinationLoader(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	log := logger.New()
	dockerClient, err := docker.NewClient("tcp://127.0.0.1:1", log)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dockerClient.Close() })

	dataPath := t.TempDir()
	writeFabricJar(t, filepath.Join(dataPath, "mods"), "new-loader-mod.jar", ">=1.21 <1.22")

	server := &storage.Server{
		ID: "srv-compat-destination-loader", Name: "Compat Destination Loader", NodeID: "local",
		Port: 25595, Memory: 1024, MaxPlayers: 20,
		ModLoader: storage.ModLoaderVanilla, MCVersion: "1.21.1",
		ContainerID: "tracked", Status: storage.StatusStopped,
		DataPath: dataPath,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))

	svc := newVersionCompatService(t, store, dockerClient, log)
	_, err = svc.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{
		Id: server.ID, ModLoader: "fabric", McVersion: "1.20.1",
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "error = %v", err)
	assert.Contains(t, err.Error(), "new-loader-mod.jar")

	// The destination loader is used only for the pre-flight scan; a refused
	// request leaves the stored loader and version untouched.
	updated, err := store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	assert.Equal(t, storage.ModLoaderVanilla, updated.ModLoader)
	assert.Equal(t, "1.21.1", updated.MCVersion)
}

// TestUpdateServerVersionChangeProceedsWhenContentUnknown verifies the gate
// fails open: no installed content means no refusal, and the version change
// runs its normal (here Docker-failing) path.
func TestUpdateServerVersionChangeProceedsWhenContentUnknown(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	log := logger.New()
	dockerClient, err := docker.NewClient("tcp://127.0.0.1:1", log)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dockerClient.Close() })

	dataPath := t.TempDir()
	// An unreadable jar is unknown, never a blocker.
	require.NoError(t, os.MkdirAll(filepath.Join(dataPath, "mods"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataPath, "mods", "corrupt.jar"), []byte("garbage"), 0o644))

	server := &storage.Server{
		ID: "srv-compat-unknown", Name: "Compat Unknown", NodeID: "local",
		Port: 25592, Memory: 1024, MaxPlayers: 20,
		ModLoader: storage.ModLoaderFabric, MCVersion: "1.20.1",
		ContainerID: "tracked", Status: storage.StatusRunning,
		DataPath: dataPath,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))

	svc := newVersionCompatService(t, store, dockerClient, log)
	_, err = svc.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{
		Id: server.ID, McVersion: "1.21.1",
	}))
	require.Error(t, err, "recreation must still fail against an unreachable daemon")
	assert.NotEqual(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "gate must fail open: %v", err)

	updated, err := store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	assert.Equal(t, "1.21.1", updated.MCVersion)
}

// TestUpdateServerRefusesVersionChangeWhenNodeStorageUninspectable guards the
// transport-only residual: a remote node with no storage adapter returns the
// typed nodestorage refusal from dataRootFor. Skipping the gate there would
// silently let an uninspectable version change proceed, so the change must be
// refused with failed_precondition like any other unproven-incompatible case.
func TestUpdateServerRefusesVersionChangeWhenNodeStorageUninspectable(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	log := logger.New()
	dockerClient, err := docker.NewClient("tcp://127.0.0.1:1", log)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dockerClient.Close() })

	require.NoError(t, store.CreateNode(ctx, &storage.Node{
		ID: "remote-bare", Name: "remote-bare", Host: "tcp://10.0.0.8:2375", Enabled: true,
	}))

	server := &storage.Server{
		ID: "srv-compat-remote-bare", Name: "Compat Remote Bare", NodeID: "remote-bare",
		Port: 25596, Memory: 1024, MaxPlayers: 20,
		ModLoader: storage.ModLoaderFabric, MCVersion: "1.21.1",
		ContainerID: "tracked", Status: storage.StatusStopped,
		DataPath: filepath.Join("srv", "servers", "remote-bare"),
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))

	svc := newVersionCompatService(t, store, dockerClient, log)
	_, err = svc.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{
		Id: server.ID, McVersion: "1.20.1",
	}))
	require.Error(t, err, "an uninspectable storage location must not pass silently")
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "error = %v", err)
	assert.Contains(t, err.Error(), "content compatibility cannot be inspected on this server's storage")

	// The refusal is pre-flight: the stored version is untouched.
	updated, err := store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	assert.Equal(t, "1.21.1", updated.MCVersion)
}

// TestVersionCompatGateFailsClosedForTransportOnlyStorage covers the case the
// typed dataRootFor error misses: with a storage resolver configured, a
// transport-only node maps to a node-side path the controller cannot walk.
// Scanning that path returns nothing, so without this check the gate would
// fail open and let an unverified version change through.
func TestVersionCompatGateFailsClosedForTransportOnlyStorage(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	nodeRoot := filepath.Join(t.TempDir(), "only-on-node")
	require.NoError(t, store.CreateNode(ctx, &storage.Node{
		ID: "remote-transport", Name: "remote-transport", Host: "tcp://10.0.0.9:2375",
		Enabled: true, StorageRoot: nodeRoot,
	}))

	server := &storage.Server{
		ID: "srv-compat-transport", Name: "Compat Transport", NodeID: "remote-transport",
		Port: 25598, Memory: 1024, MaxPlayers: 20,
		ModLoader: storage.ModLoaderFabric, MCVersion: "1.21.1",
		ContainerID: "tracked", Status: storage.StatusStopped,
		DataPath: filepath.Join("srv", "servers", "srv-compat-transport"),
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))

	svc := newVersionCompatService(t, store, nil, logger.New())
	svc.storageResolver = nodestorage.NewResolver(&recordingTransport{workerRoot: t.TempDir()})

	err := svc.refuseVersionIncompatibleContent(ctx, server, "1.20.1")
	require.Error(t, err, "transport-only storage must not pass the gate silently")
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "error = %v", err)
	assert.Contains(t, err.Error(), "content compatibility cannot be inspected on this server's storage")
}

// TestVersionCompatGateFailsOpenOnUnparseableReadableContent pins the other
// half of the contract: readable content whose archives or metadata cannot be
// parsed is unknown, never a version-change blocker.
func TestVersionCompatGateFailsOpenOnUnparseableReadableContent(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	dataPath := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dataPath, "mods"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataPath, "mods", "corrupt.jar"), []byte("garbage"), 0o644))

	server := &storage.Server{
		ID: "srv-compat-unreadable", Name: "Compat Unreadable", NodeID: "local",
		Port: 25599, Memory: 1024, MaxPlayers: 20,
		ModLoader: storage.ModLoaderFabric, MCVersion: "1.21.1",
		DataPath: dataPath,
	}
	require.NoError(t, store.CreateServer(ctx, server))

	svc := newVersionCompatService(t, store, nil, logger.New())
	require.NoError(t, svc.refuseVersionIncompatibleContent(ctx, server, "1.20.1"),
		"an unreadable archive in a readable location must fail open")
}

// TestVersionCompatGateFailsOpenOnNodeResolutionError keeps the documented
// fail-open behavior for ordinary storage errors that are not the typed
// transport refusal (for example an unresolvable node reference).
func TestVersionCompatGateFailsOpenOnNodeResolutionError(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	server := &storage.Server{
		ID: "srv-compat-missing-node", Name: "Compat Missing Node", NodeID: "no-such-node",
		Port: 25597, Memory: 1024, MaxPlayers: 20,
		ModLoader: storage.ModLoaderFabric, MCVersion: "1.21.1",
		DataPath: filepath.Join("srv", "servers", "missing-node"),
	}
	require.NoError(t, store.CreateServer(ctx, server))

	svc := newVersionCompatService(t, store, nil, logger.New())
	require.NoError(t, svc.refuseVersionIncompatibleContent(ctx, server, "1.20.1"),
		"non-transport storage errors must keep failing open")
}

// TestUpdateServerRefusesIncompatibleDatapackDowngrade covers the datapack
// half of the gate: a pack built for a newer format than the target release
// supports is refused.
func TestUpdateServerRefusesIncompatibleDatapackDowngrade(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	log := logger.New()
	dockerClient, err := docker.NewClient("tcp://127.0.0.1:1", log)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dockerClient.Close() })

	dataPath := t.TempDir()
	datapacks := filepath.Join(dataPath, "world", "datapacks")
	require.NoError(t, os.MkdirAll(datapacks, 0o755))
	writeTestZip(t, filepath.Join(datapacks, "fancy-pack.zip"), map[string]string{
		"pack.mcmeta": `{"pack":{"pack_format":48}}`,
	})

	server := &storage.Server{
		ID: "srv-compat-datapack", Name: "Compat Datapack", NodeID: "local",
		Port: 25593, Memory: 1024, MaxPlayers: 20,
		ModLoader: storage.ModLoaderVanilla, MCVersion: "1.21.1",
		ContainerID: "tracked", Status: storage.StatusStopped,
		DataPath: dataPath,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))

	svc := newVersionCompatService(t, store, dockerClient, log)
	_, err = svc.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{
		Id: server.ID, McVersion: "1.20.1",
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "error = %v", err)
	assert.Contains(t, err.Error(), "fancy-pack.zip")
	assert.True(t, strings.Contains(err.Error(), "1.21"), "message should name the pack's target: %s", err.Error())
}

// TestCommandBlockedReason pins the status-aware console refusals so the RPC
// and websocket paths can stay worded identically.
func TestCommandBlockedReason(t *testing.T) {
	running := []struct {
		db       storage.ServerStatus
		observed storage.ServerStatus
	}{
		{storage.StatusRunning, storage.StatusRunning},
		{storage.StatusStarting, storage.StatusRunning},
	}
	for _, tc := range running {
		if got := commandBlockedReason(tc.db, tc.observed, nil); got != "" {
			t.Fatalf("running command must proceed, got %q", got)
		}
	}

	cases := []struct {
		name     string
		db       storage.ServerStatus
		observed storage.ServerStatus
		want     string
	}{
		{"db paused", storage.StatusPaused, storage.StatusStopped, "server is frozen (paused); resume the server before sending commands"},
		{"observed paused", storage.StatusRunning, storage.StatusPaused, "server is frozen (paused); resume the server before sending commands"},
		{"deep sleep", storage.StatusDeepSleep, storage.StatusStopped, "server is in deep sleep; it wakes when a player connects — wake it first to run commands"},
		{"db stopping, container gone", storage.StatusStopping, storage.StatusStopped, "server is stopping; try again shortly"},
		{"observed stopping", storage.StatusStopped, storage.StatusStopping, "server is stopping; try again shortly"},
		{"plain stopped", storage.StatusStopped, storage.StatusStopped, "server is not running"},
	}
	for _, tc := range cases {
		if got := commandBlockedReason(tc.db, tc.observed, nil); got != tc.want {
			t.Fatalf("%s: reason = %q, want %q", tc.name, got, tc.want)
		}
	}

	// A failed observation keeps the honest generic refusal.
	if got := commandBlockedReason(storage.StatusRunning, "", assert.AnError); got != "server is not running" {
		t.Fatalf("observe failure reason = %q", got)
	}
}

// TestUpdateServerRefusesIncompatibleDatapackDowngradeInCustomLevel guards the
// A20 regression: datapacks live under the active level-name save folder, not
// always "world", so the version gate must scan the configured level.
func TestUpdateServerRefusesIncompatibleDatapackDowngradeInCustomLevel(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	log := logger.New()
	dockerClient, err := docker.NewClient("tcp://127.0.0.1:1", log)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dockerClient.Close() })

	dataPath := t.TempDir()
	datapacks := filepath.Join(dataPath, "customworld", "datapacks")
	require.NoError(t, os.MkdirAll(datapacks, 0o755))
	writeTestZip(t, filepath.Join(datapacks, "fancy-pack.zip"), map[string]string{
		"pack.mcmeta": `{"pack":{"pack_format":48}}`,
	})

	server := &storage.Server{
		ID: "srv-compat-datapack-custom", Name: "Compat Datapack Custom", NodeID: "local",
		Port: 25594, Memory: 1024, MaxPlayers: 20,
		ModLoader: storage.ModLoaderVanilla, MCVersion: "1.21.1",
		ContainerID: "tracked", Status: storage.StatusStopped,
		DataPath: dataPath,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	cfg := store.CreateDefaultServerConfig(server.ID)
	level := "customworld"
	cfg.Level = &level
	require.NoError(t, store.SaveServerConfig(ctx, cfg))

	svc := newVersionCompatService(t, store, dockerClient, log)
	_, err = svc.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{
		Id: server.ID, McVersion: "1.20.1",
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "error = %v", err)
	assert.Contains(t, err.Error(), "fancy-pack.zip")
	assert.True(t, strings.Contains(err.Error(), "1.21"), "message should name the pack's target: %s", err.Error())
}
