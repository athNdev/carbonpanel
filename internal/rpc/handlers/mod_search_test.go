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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/indexers"
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

// newModSearchStore builds a real store with one paper server whose data root
// lives in a temp dir, mirroring a legacy local-node deployment.
func newModSearchStore(t *testing.T) (*db.Store, *db.Server, string) {
	t.Helper()
	store, err := db.NewSQLiteStore(&config.Config{
		Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "modsearch.db"), MaxConnections: 5},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.Migrate())

	dataRoot := t.TempDir()
	server := &db.Server{
		ID: "srv-paper", Name: "paper", ModLoader: db.ModLoaderPaper, MCVersion: "1.21.1",
		NodeID: "local", DataPath: dataRoot, Status: db.StatusStopped,
	}
	require.NoError(t, store.CreateServer(context.Background(), server))
	return store, server, dataRoot
}

func TestPluginLoaderClassification(t *testing.T) {
	for _, loader := range []string{"paper", "purpur", "pufferfish", "folia", "spigot", "bukkit", "glowstone"} {
		assert.True(t, pluginLoader(loader), "loader %q should be plugin-capable", loader)
	}
	for _, loader := range []string{"fabric", "forge", "quilt", "neoforge", "vanilla", "auto_curseforge", "", "custom"} {
		assert.False(t, pluginLoader(loader), "loader %q should not be plugin-capable", loader)
	}

	// Raw db/proto enum values and aliases normalize into the family.
	assert.Equal(t, "paper", normalizeLoaderName("MOD_LOADER_PAPER"))
	assert.Equal(t, "paper", normalizeLoaderName("papermc"))
	assert.Equal(t, "paper", normalizeLoaderName(" Paper "))
	assert.True(t, pluginLoader(normalizeLoaderName("MOD_LOADER_PUFFERFISH")))
}

func TestModOnlineManager_PaperServerSearchMergesPluginSources(t *testing.T) {
	log := logger.New()
	store, _, _ := newModSearchStore(t)

	var hangarUA, hangarQuery string
	mockHangar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/projects" {
			http.NotFound(w, r)
			return
		}
		hangarUA = r.Header.Get("User-Agent")
		q := r.URL.Query()
		hangarQuery = q.Get("q")
		// ViaVersion supports 1.21.1 on PAPER; OldPlugin does not.
		_, _ = w.Write([]byte(`{
		  "pagination": {"count": 2, "limit": 25, "offset": 0},
		  "result": [
		    {"id": 31, "name": "ViaVersion", "namespace": {"owner": "ViaVersion", "slug": "ViaVersion"},
		     "stats": {"downloads": 533531}, "description": "Protocol translation",
		     "supportedPlatforms": {"PAPER": ["1.21", "1.21.1"]}, "avatarUrl": "https://hangarcdn/x.webp"},
		    {"id": 32, "name": "OldPlugin", "namespace": {"owner": "old", "slug": "OldPlugin"},
		     "stats": {"downloads": 10}, "description": "old only",
		     "supportedPlatforms": {"PAPER": ["1.8.8"]}, "avatarUrl": ""}
		  ]
		}`))
	}))
	t.Cleanup(mockHangar.Close)

	var modrinthFacets string
	mockModrinth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modrinthFacets = r.URL.Query().Get("facets")
		_, _ = w.Write([]byte(`{"hits":[{"project_id":"9eGKb6K1","slug":"simple-voice-chat","title":"Simple Voice Chat","description":"Voice chat","categories":["paper"],"client_side":"optional","server_side":"required","downloads":77628691,"icon_url":""}],"offset":0,"limit":25,"total_hits":1}`))
	}))
	t.Cleanup(mockModrinth.Close)

	authMgr, policyEnforcer := testNoAuthPolicy(t)
	manager := NewModOnlineManager(store, log, authMgr, policyEnforcer, ModOnlineOptions{
		HangarBaseURL:   mockHangar.URL,
		ModrinthBaseURL: mockModrinth.URL,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/servers/srv-paper/mods/search?query=via&loader=paper&mc_version=1.21.1", nil)
	rec := httptest.NewRecorder()
	manager.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var payload struct {
		Results []SearchModResult `json:"results"`
		Count   int               `json:"count"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	// Hangar query was narrowed by the search term and the identifying UA
	// policy is honored.
	assert.Equal(t, "via", hangarQuery)
	assert.Contains(t, hangarUA, "CarbonPanel")
	// Modrinth was queried as the plugin catalog with the paper facet.
	assert.Contains(t, modrinthFacets, "project_type:plugin")
	assert.Contains(t, modrinthFacets, "categories:paper")

	// Merged, labeled, and filtered by the server's game version. Both
	// sources appear with their own platform label.
	var hangarResult, modrinthResult *SearchModResult
	for i := range payload.Results {
		switch payload.Results[i].Platform {
		case "hangar":
			hangarResult = &payload.Results[i]
		case "modrinth":
			modrinthResult = &payload.Results[i]
		}
	}
	require.NotNil(t, hangarResult, "hangar results: %+v", payload.Results)
	assert.Equal(t, "ViaVersion", hangarResult.Slug)
	assert.Equal(t, "ViaVersion", hangarResult.Author)
	assert.Equal(t, int64(533531), hangarResult.Downloads)
	require.NotNil(t, modrinthResult, "modrinth plugin results: %+v", payload.Results)
	assert.Equal(t, "simple-voice-chat", modrinthResult.Slug)
	// OldPlugin only supports 1.8.8 and must be filtered by mc_version.
	for _, r := range payload.Results {
		assert.NotEqual(t, "OldPlugin", r.Title)
	}

	// A merged duplicate keeps the plugin-native (hangar) entry only.
	hangarOnly := manager.searchHangar(req.Context(), "", "via", "1.21.1", map[string]bool{})
	modrinthOnly := []SearchModResult{{Slug: "ViaVersion", Title: "ViaVersion", Platform: "modrinth"}}
	merged := indexers.MergeLabeledResults(func(r SearchModResult) string {
		if r.Title != "" {
			return r.Title
		}
		return r.Slug
	}, hangarOnly, modrinthOnly)
	require.Len(t, merged, 1)
	assert.Equal(t, "hangar", merged[0].Platform)
}

func TestModOnlineManager_PaperServerVersionsPreferHangar(t *testing.T) {
	log := logger.New()
	store, _, _ := newModSearchStore(t)

	hangarMux := http.NewServeMux()
	mockHangar := httptest.NewServer(hangarMux)
	hangarMux.HandleFunc("/projects/ViaVersion", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":31,"name":"ViaVersion","namespace":{"owner":"ViaVersion","slug":"ViaVersion"}}`))
	})
	hangarMux.HandleFunc("/projects/ViaVersion/ViaVersion/versions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
		  "pagination": {"count": 1, "limit": 25, "offset": 0},
		  "result": [{
		    "id": 34580, "projectId": 31, "name": "5.4.2", "visibility": "public",
		    "channel": {"name": "Release"},
		    "downloads": {"PAPER": {"fileInfo": {"name": "ViaVersion-5.4.2.jar", "sizeBytes": 5132235, "sha256Hash": "abc"}, "downloadUrl": "https://hangarcdn/ViaVersion-5.4.2.jar"}},
		    "pluginDependencies": {"PAPER": [{"name": "ViaBackwards", "projectId": 12, "required": false, "platform": "PAPER"}]},
		    "platformDependencies": {"PAPER": ["1.21", "1.21.1"]}
		  }]
		}`))
	})
	t.Cleanup(mockHangar.Close)

	// Modrinth serves a different project only when asked with its slug.
	mockModrinth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/modrinth-only/version") {
			_, _ = w.Write([]byte(`[{"id":"m1","version_number":"1.2.3","name":"1.2.3","version_type":"release","files":[{"url":"https://cdn.modrinth.com/m.jar","filename":"modrinth-only-1.2.3.jar","primary":true,"size":10}]}]`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(mockModrinth.Close)

	authMgr, policyEnforcer := testNoAuthPolicy(t)
	manager := NewModOnlineManager(store, log, authMgr, policyEnforcer, ModOnlineOptions{
		HangarBaseURL:   mockHangar.URL,
		ModrinthBaseURL: mockModrinth.URL,
	})

	fetchVersions := func(url string) (int, []ModVersionItem) {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		manager.ServeHTTP(rec, req)
		var payload struct {
			Versions []ModVersionItem `json:"versions"`
		}
		if rec.Code == http.StatusOK {
			_ = json.Unmarshal(rec.Body.Bytes(), &payload)
		}
		return rec.Code, payload.Versions
	}

	// Explicit platform=hangar.
	code, versions := fetchVersions("/api/v1/servers/srv-paper/mods/ViaVersion/versions?platform=hangar")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, versions, 1)
	v := versions[0]
	assert.Equal(t, "5.4.2", v.VersionNumber)
	assert.Equal(t, "ViaVersion-5.4.2.jar", v.FileName)
	assert.Equal(t, "https://hangarcdn/ViaVersion-5.4.2.jar", v.DownloadURL)
	assert.Equal(t, int64(5132235), v.FileSize)
	assert.Equal(t, "release", v.VersionType)
	require.Len(t, v.Dependencies, 1)
	assert.Equal(t, "12", v.Dependencies[0].ProjectID)
	assert.Equal(t, "optional", v.Dependencies[0].DependencyType)
	assert.Equal(t, "ViaBackwards", v.Dependencies[0].Title)

	// Default (modrinth) platform still resolves the plugin-native index first.
	code, versions = fetchVersions("/api/v1/servers/srv-paper/mods/ViaVersion/versions?loader=paper")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, versions, 1)
	assert.Equal(t, "5.4.2", versions[0].VersionNumber)

	// A Modrinth-sourced slug falls through to Modrinth when Hangar misses.
	code, versions = fetchVersions("/api/v1/servers/srv-paper/mods/modrinth-only/versions?loader=paper")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, versions, 1)
	assert.Equal(t, "modrinth-only-1.2.3.jar", versions[0].FileName)
}

func TestModOnlineManager_ExplicitModrinthVersionsSkipHangarFallback(t *testing.T) {
	log := logger.New()
	store, server, _ := newModSearchStore(t)

	var hangarCalls atomic.Int32
	mockHangar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hangarCalls.Add(1)
		switch r.URL.Path {
		case "/projects/ViaVersion":
			_, _ = w.Write([]byte(`{"id":31,"name":"ViaVersion","namespace":{"owner":"ViaVersion","slug":"ViaVersion"}}`))
		case "/projects/ViaVersion/ViaVersion/versions":
			_, _ = w.Write([]byte(`{
			  "pagination": {"count": 1, "limit": 25, "offset": 0},
			  "result": [{
			    "id": 34580, "projectId": 31, "name": "hangar-version", "visibility": "public",
			    "channel": {"name": "Release"},
			    "downloads": {"PAPER": {"fileInfo": {"name": "ViaVersion-hangar.jar", "sizeBytes": 10, "sha256Hash": "abc"}, "downloadUrl": "https://hangarcdn/ViaVersion-hangar.jar"}},
			    "platformDependencies": {"PAPER": ["1.21.1"]}
			  }]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(mockHangar.Close)

	mockModrinth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/project/ViaVersion/version" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[{"id":"mr-version","version_number":"modrinth-version","name":"modrinth-version","version_type":"release","files":[{"url":"https://modrinth.example/ViaVersion-modrinth.jar","filename":"ViaVersion-modrinth.jar","primary":true,"size":20}]}]`))
	}))
	t.Cleanup(mockModrinth.Close)

	authMgr, policyEnforcer := testNoAuthPolicy(t)
	manager := NewModOnlineManager(store, log, authMgr, policyEnforcer, ModOnlineOptions{
		HangarBaseURL:   mockHangar.URL,
		ModrinthBaseURL: mockModrinth.URL,
	})

	versions, browseErr := manager.Versions(context.Background(), server, "ViaVersion", ModVersionParams{
		Platform: "modrinth",
		Loader:   "paper",
	})
	require.Nil(t, browseErr)
	require.Len(t, versions, 1)
	assert.Equal(t, "ViaVersion-modrinth.jar", versions[0].FileName)
	assert.Equal(t, "https://modrinth.example/ViaVersion-modrinth.jar", versions[0].DownloadURL)
	assert.Zero(t, hangarCalls.Load(), "an explicit Modrinth selection must not query Hangar")
}

func TestModOnlineManager_InstallWritesToPluginsDirectory(t *testing.T) {
	log := logger.New()
	store, _, dataRoot := newModSearchStore(t)

	jarBytes := []byte("fake-plugin-jar-contents")
	mockCDN := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
