// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/athNdev/carbon-panel/internal/datawrite"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/packwiz"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/stretchr/testify/require"
)

// TestPackwizSameRuntimeBakeSharesDataWriteGate proves the REST deploy entry
// point takes the shared data-write gate before baking into the server's data
// directory, while a same-runtime sync does not need the exclusive path.
func TestPackwizSameRuntimeBakeSharesDataWriteGate(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	server := &storage.Server{
		ID: "srv-packwiz-bake", Name: "srv-packwiz-bake", NodeID: "local",
		ModLoader: storage.ModLoaderFabric, MCVersion: "1.20.1", Status: storage.StatusStopped,
		DataPath: t.TempDir(),
	}
	require.NoError(t, store.CreateServer(ctx, server))
	cfg := store.CreateDefaultServerConfig(server.ID)
	serverType, gameVersion := "FABRIC", "1.20.1"
	cfg.Type, cfg.Version = &serverType, &gameVersion
	require.NoError(t, store.SaveServerConfig(ctx, cfg))

	var downloads atomic.Int32
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		_, _ = w.Write([]byte("mod contents"))
	}))
	t.Cleanup(cdn.Close)
	mgr := packwiz.NewManager(t.TempDir(), logger.New())
	pack, err := mgr.CreatePack(&packwiz.Pack{
		Name: "Same runtime", MCVersion: server.MCVersion, ModLoader: string(server.ModLoader),
		Mods: []packwiz.ModItem{{Name: "Example", FileName: "example.jar", Side: "server", DownloadURL: cdn.URL}},
	})
	require.NoError(t, err)
	handler := NewPackwizHandler(mgr, store, nil, logger.New(), nil, nil, nil)
	deploy := func(mode string) *httptest.ResponseRecorder {
		body, err := json.Marshal(DeployPackRequest{ServerID: server.ID, Mode: mode})
		require.NoError(t, err)
		rec := httptest.NewRecorder()
		handler.handleDeploy(rec, httptest.NewRequest(http.MethodPost, "/deploy", bytes.NewReader(body)), pack.ID)
		return rec
	}

	// The reviewed candidate publication holds the shared gate exclusively.
	owned, err := datawrite.AcquireExclusive(ctx, server.ID)
	require.NoError(t, err)
	t.Cleanup(owned.Release)
	blocked := deploy("bake")
	require.Equal(t, http.StatusConflict, blocked.Code, blocked.Body.String())
	require.Contains(t, blocked.Body.String(), datawrite.ErrExclusiveHeld.Error())
	require.Zero(t, downloads.Load())
	require.NoDirExists(t, filepath.Join(server.DataPath, "mods"))
	synced := deploy("sync")
	require.Equal(t, http.StatusOK, synced.Code, synced.Body.String())
	owned.Release()

	baked := deploy("bake")
	require.Equal(t, http.StatusOK, baked.Code, baked.Body.String())
	require.EqualValues(t, 1, downloads.Load())
	jar, err := os.ReadFile(filepath.Join(server.DataPath, "mods", "example.jar"))
	require.NoError(t, err)
	require.Equal(t, "mod contents", string(jar))
}
