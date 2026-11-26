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
	"path/filepath"
	"strings"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/packwiz"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/stretchr/testify/require"
)

// TestPackwizDeployRefusesRuntimeChangeWithReviewedCandidate proves the REST
// deploy entry point refuses to retarget the server runtime while a reviewed
// mod candidate is staged, before any server row or data-directory write.
func TestPackwizDeployRefusesRuntimeChangeWithReviewedCandidate(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	server := &storage.Server{
		ID: "srv-packwiz-gate", Name: "packwiz-gate", NodeID: "local",
		ModLoader: storage.ModLoaderFabric, MCVersion: "1.20.1", Status: storage.StatusStopped,
		DataPath: t.TempDir(),
	}
	require.NoError(t, store.CreateServer(ctx, server))
	cfg := store.CreateDefaultServerConfig(server.ID)
	serverType, gameVersion := "FABRIC", "1.20.1"
	cfg.Type, cfg.Version = &serverType, &gameVersion
	require.NoError(t, store.SaveServerConfig(ctx, cfg))

	_, err := store.AddStagedContent(ctx, &storage.StagedContentChange{
		ServerID:           server.ID,
		Kind:               storage.StagedKindMod,
		Action:             storage.StagedActionReplace,
		TargetPath:         "mods/mod.jar",
		StagePath:          ".carbon-panel/staged/reviewed/mod.jar",
		FileName:           "mod.jar",
		ExpectedLiveSha512: "deadbeef",
		ApprovedSha512:     strings.Repeat("ab", 64),
		ApprovedProvider:   "modrinth",
		ApprovedProjectID:  "project",
		ApprovedVersionID:  "release",
		ApprovedFilename:   "mod.jar",
	})
	require.NoError(t, err)

	mgr := packwiz.NewManager(t.TempDir(), logger.New())
	pack, err := mgr.CreatePack(&packwiz.Pack{
		Name: "Gate", MCVersion: "1.21.1", ModLoader: "fabric",
		Mods: []packwiz.ModItem{{Name: "Example", FileName: "example.jar", Side: "server", DownloadURL: "https://example.invalid/example.jar"}},
	})
	require.NoError(t, err)

	handler := NewPackwizHandler(mgr, store, nil, logger.New(), nil, nil, nil)

	for _, mode := range []string{"sync", "bake"} {
		t.Run(mode, func(t *testing.T) {
			body, err := json.Marshal(DeployPackRequest{ServerID: server.ID, Mode: mode})
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodPost, "/deploy", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			handler.handleDeploy(rec, req, pack.ID)
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), storage.ErrStagedModVersionChange.Error())

			got, err := store.GetServer(ctx, server.ID)
			require.NoError(t, err)
			require.Equal(t, "1.20.1", got.MCVersion)
			require.Equal(t, storage.ModLoaderFabric, got.ModLoader)
			require.NoDirExists(t, filepath.Join(server.DataPath, "mods"))
		})
	}
}
