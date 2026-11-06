// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/athNdev/carbon-panel/internal/packwiz"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/stretchr/testify/require"
)

func TestPackwizHandler_ContentRoutes(t *testing.T) {
	dir, err := os.MkdirTemp("", "packwiz_content_handler_*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(dir) }()

	log := logger.New()
	mgr := packwiz.NewManager(dir, log)
	handler := NewPackwizHandler(mgr, nil, nil, log, nil, nil, nil)

	pack := packwiz.Pack{ID: "p", Name: "Content Pack", MCVersion: "1.21.1", ModLoader: "fabric"}
	body, _ := json.Marshal(pack)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/packwiz/packs", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code)

	item := packwiz.ModItem{
		Kind: packwiz.ContentKindDatapack, Name: "Terralith", FileName: "terralith.zip",
		Side: "server", Platform: "modrinth", ProjectID: "proj", VersionID: "ver",
		DownloadURL: "https://cdn.example/terralith.zip", SHA512: "abc",
		Optional: true, OptionDescription: "Optional worldgen",
	}
	body, _ = json.Marshal(item)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/packwiz/packs/p/content", bytes.NewReader(body))
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	loaded, err := mgr.GetPack("p")
	require.NoError(t, err)
	require.Len(t, loaded.Datapacks, 1)
	require.Equal(t, "Terralith", loaded.Datapacks[0].Name)
	require.True(t, loaded.Datapacks[0].Optional)

	pinned := true
	side := "both"
	patchBody, _ := json.Marshal(packwiz.ContentPatch{Pinned: &pinned, Side: &side})
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/packwiz/packs/p/content/datapack/terralith", bytes.NewReader(patchBody))
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	folderBody := []byte(`{"folder":"config/openloader/data"}`)
	req = httptest.NewRequest(http.MethodPut, "/api/v1/packwiz/packs/p/datapack-folder", bytes.NewReader(folderBody))
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	loaded, err = mgr.GetPack("p")
	require.NoError(t, err)
	require.Equal(t, "config/openloader/data", loaded.DatapackFolder)
	require.True(t, loaded.Datapacks[0].Pinned)
	require.Equal(t, "both", loaded.Datapacks[0].Side)

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/packwiz/packs/p/content/datapack/terralith", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	loaded, err = mgr.GetPack("p")
	require.NoError(t, err)
	require.Empty(t, loaded.Datapacks)
}
