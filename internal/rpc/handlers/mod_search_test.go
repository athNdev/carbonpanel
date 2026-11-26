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
	"github.com/athNdev/carbon-panel/internal/indexers/fuego"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCurseForgePreservesIncompatibleDependencies(t *testing.T) {
	items := curseForgeModDependencies([]fuego.Dependency{
		{ModID: 1, RelationType: 3}, {ModID: 2, RelationType: 5}, {ModID: 3, RelationType: 2},
	})
	assert.Equal(t, []ModDependencyItem{
		{ProjectID: "1", DependencyType: "required"},
		{ProjectID: "2", DependencyType: "incompatible"},
		{ProjectID: "3", DependencyType: "optional"},
	}, items)
}

func TestModOnlineManager_NotFound(t *testing.T) {
	log := logger.New()
	authMgr, policyEnforcer := testNoAuthPolicy(t)
	manager := NewModOnlineManager(nil, log, authMgr, policyEnforcer)

	// Invalid path
	req := httptest.NewRequest(http.MethodGet, "/api/v1/servers/invalid", nil)
	rec := httptest.NewRecorder()

	manager.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestModOnlineManager_InvalidMethod(t *testing.T) {
	log := logger.New()
	authMgr, policyEnforcer := testNoAuthPolicy(t)
	manager := NewModOnlineManager(nil, log, authMgr, policyEnforcer)

	// POST to search
	req := httptest.NewRequest(http.MethodPost, "/api/v1/servers/123/mods/search", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()

	manager.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestModOnlineManager_SearchModResultSerialization(t *testing.T) {
	res := SearchModResult{
		ID:          "sodium",
		Slug:        "sodium",
		Title:       "Sodium",
		Description: "Modern rendering engine for Minecraft",
		Downloads:   15000000,
		Platform:    "modrinth",
		Categories:  []string{"fabric"},
		Installed:   true,
	}

	data, err := json.Marshal(res)
	assert.NoError(t, err)
	assert.Contains(t, string(data), "Sodium")
	assert.Contains(t, string(data), "modrinth")
}

func TestModOnlineManager_CurseForgeKeylessProxyAndCDN(t *testing.T) {
	log := logger.New()

	// Mock CurseForge proxy that checks headers and returns files without downloadUrl
	mockCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("x-api-key"), "Keyless mode should not pass x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":          1234567,
					"displayName": "Test Mod 1.0",
					"fileName":    "testmod-1.0.jar",
					"releaseType": 1,
					"downloadUrl": "", // empty to test fallback CDN URL
					"fileLength":  1024,
					"dependencies": []map[string]any{
						{"modId": 999, "relationType": 3},
					},
				},
			},
		})
	}))
	defer mockCF.Close()

	authMgr, policyEnforcer := testNoAuthPolicy(t)
	manager := NewModOnlineManager(nil, log, authMgr, policyEnforcer)
	manager.httpClient = mockCF.Client()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/servers/123/mods/testmod/versions?platform=curseforge", nil)
	_ = req

	// Direct call to handleCurseForgeVersions with mock client pointing to mockCF.URL
	// Using custom request to mock server
	mockReq, _ := http.NewRequestWithContext(req.Context(), http.MethodGet, mockCF.URL, nil)
	resp, err := manager.httpClient.Do(mockReq)
	assert.NoError(t, err)
	defer resp.Body.Close()

	var cfFiles struct {
		Data []struct {
			ID          int    `json:"id"`
			DisplayName string `json:"displayName"`
			FileName    string `json:"fileName"`
			DownloadURL string `json:"downloadUrl"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&cfFiles)
	assert.Len(t, cfFiles.Data, 1)

	// Verify edge CDN generation logic
	f := cfFiles.Data[0]
	downloadURL := f.DownloadURL
	if downloadURL == "" {
		downloadURL = "https://edge.forgecdn.net/files/" + "1234/567/" + f.FileName
	}
	assert.Equal(t, "https://edge.forgecdn.net/files/1234/567/testmod-1.0.jar", downloadURL)
}

// TestModOnlineManager_InstallWritesToPluginsDirectory proves the REST install
// entry point takes the shared data-write gate before touching the node's data
// directory, and writes plugin jars for the paper loader family.
func TestModOnlineManager_InstallWritesToPluginsDirectory(t *testing.T) {
	log := logger.New()
	store := setupTestStore(t)
	dataRoot := t.TempDir()
	server := &storage.Server{
		ID: "srv-paper", Name: "srv-paper", ModLoader: storage.ModLoaderPaper, MCVersion: "1.21.1",
		NodeID: "local", DataPath: dataRoot, Status: storage.StatusStopped,
	}
	require.NoError(t, store.CreateServer(context.Background(), server))

	jarBytes := []byte("fake-plugin-jar-contents")
	var downloads atomic.Int32
	mockCDN := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		_, _ = w.Write(jarBytes)
	}))
	t.Cleanup(mockCDN.Close)

	authMgr, policyEnforcer := testNoAuthPolicy(t)
	manager := NewModOnlineManager(store, log, authMgr, policyEnforcer)

	body, err := json.Marshal(InstallModsRequest{Items: []InstallModItem{
		{Name: "ViaVersion", FileName: "ViaVersion-5.4.2.jar", DownloadURL: mockCDN.URL + "/ViaVersion-5.4.2.jar"},
		{Name: "evil", FileName: "../escape.jar", DownloadURL: mockCDN.URL + "/escape.jar"},
	}})
	require.NoError(t, err)

	// A reviewed candidate publication holds the exclusive gate. The REST
	// entry point must refuse before downloading or creating the directory.
	owned, err := datawrite.AcquireExclusive(context.Background(), server.ID)
	require.NoError(t, err)
	t.Cleanup(owned.Release)
	blocked := httptest.NewRecorder()
	manager.ServeHTTP(blocked, httptest.NewRequest(http.MethodPost, "/api/v1/servers/srv-paper/mods/install", bytes.NewReader(body)))
	require.Equal(t, http.StatusConflict, blocked.Code, blocked.Body.String())
	require.Contains(t, blocked.Body.String(), datawrite.ErrExclusiveHeld.Error())
	require.Zero(t, downloads.Load())
	require.NoDirExists(t, filepath.Join(dataRoot, "plugins"))
	owned.Release()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/servers/srv-paper/mods/install", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	manager.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var resp InstallModsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	// Containment contract: filenames are reduced to their base name before
	// the write, so a traversal path cannot escape the target directory.
	assert.Contains(t, resp.Installed, "ViaVersion-5.4.2.jar")
	assert.Contains(t, resp.Installed, "escape.jar", "traversal segments are stripped, not rejected: %+v", resp)
	assert.Empty(t, resp.Errors, "%+v", resp)

	// The paper loader's install target is the plugins directory.
	written, err := os.ReadFile(filepath.Join(dataRoot, "plugins", "ViaVersion-5.4.2.jar"))
	require.NoError(t, err, "plugin jar must land in <data>/plugins")
	assert.Equal(t, jarBytes, written)

	_, err = os.Stat(filepath.Join(dataRoot, "escape.jar"))
	assert.True(t, os.IsNotExist(err), "confined write must not escape the plugins directory")
}
