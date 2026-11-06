// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/athNdev/carbon-panel/internal/packwiz"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newExportHandler(t *testing.T) (*PackwizHandler, string) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "packwiz_handler_export_*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	log := logger.New()
	mgr := packwiz.NewManager(tempDir, log)
	return NewPackwizHandler(mgr, nil, nil, log, nil, nil, nil), tempDir
}

func seedHandlerPack(t *testing.T, h *PackwizHandler) {
	t.Helper()
	_, err := h.manager.CreatePack(&packwiz.Pack{
		ID: "export-pack", Name: "Export Pack", Version: "1.0.0",
		MCVersion: "1.20.1", ModLoader: "fabric", LoaderVersion: "0.15.11",
	})
	require.NoError(t, err)
	require.NoError(t, h.manager.AddMod("export-pack", packwiz.ModItem{
		Slug: "cfmod", Name: "CF Mod", FileName: "cfmod.jar", Side: "both",
		Platform: "curseforge", ProjectID: "238222", VersionID: "4635832",
		DownloadURL: "https://edge.forgecdn.net/cfmod.jar",
		Hash:        "abc123", HashFormat: "sha256",
	}))
	require.NoError(t, h.manager.AddMod("export-pack", packwiz.ModItem{
		Slug: "mrmod", Name: "MR Mod", FileName: "mrmod.jar", Side: "client",
		Platform: "modrinth", ProjectID: "AANobbMI", VersionID: "v1",
		DownloadURL: "https://cdn.modrinth.com/mrmod.jar",
		Hash:        "def456", HashFormat: "sha256",
	}))
}

func TestPackwizHandler_ExportMrpackWithOptions(t *testing.T) {
	h, _ := newExportHandler(t)
	seedHandlerPack(t, h)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/packwiz/packs/export-pack/export/mrpack?include_client=false&optional_mode=optional&summary=Hello", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Disposition"), ".mrpack")

	zr, err := zip.NewReader(bytes.NewReader(rr.Body.Bytes()), int64(rr.Body.Len()))
	require.NoError(t, err)
	var indexRaw []byte
	for _, f := range zr.File {
		if f.Name == "modrinth.index.json" {
			rc, err := f.Open()
			require.NoError(t, err)
			indexRaw, err = io.ReadAll(rc)
			rc.Close()
			require.NoError(t, err)
		}
	}
	require.NotEmpty(t, indexRaw)
	var index struct {
		Summary string `json:"summary"`
		Files   []struct {
			Path   string            `json:"path"`
			Hashes map[string]string `json:"hashes"`
			Env    map[string]string `json:"env"`
		} `json:"files"`
	}
	require.NoError(t, json.Unmarshal(indexRaw, &index))
	assert.Equal(t, "Hello", index.Summary)
	// Client-only MR mod excluded via include_client=false; CF mod remains.
	require.Len(t, index.Files, 1)
	assert.Contains(t, index.Files[0].Path, "cfmod.jar")
	assert.Equal(t, "optional", index.Files[0].Env["client"])
	assert.Equal(t, "abc123", index.Files[0].Hashes["sha256"])
	assert.NotEmpty(t, rr.Header().Get("X-Checksum-SHA256"))
}

func TestPackwizHandler_ExportCurseForgeBlocked(t *testing.T) {
	h, _ := newExportHandler(t)
	seedHandlerPack(t, h)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/packwiz/packs/export-pack/export/curseforge", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnprocessableEntity, rr.Code)

	var body struct {
		Error   string   `json:"error"`
		Dropped []string `json:"dropped"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Contains(t, body.Error, "drop")
	require.Len(t, body.Dropped, 1)
	assert.Contains(t, body.Dropped[0], "MR Mod")
}

func TestPackwizHandler_ExportMMC(t *testing.T) {
	h, _ := newExportHandler(t)
	seedHandlerPack(t, h)

	for _, format := range []string{"mmc", "prism"} {
		req := httptest.NewRequest(http.MethodGet,
			"/api/v1/packwiz/packs/export-pack/export/"+format+"?include_client=true", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, "format %s", format)
		assert.Equal(t, "application/zip", rr.Header().Get("Content-Type"))

		zr, err := zip.NewReader(bytes.NewReader(rr.Body.Bytes()), int64(rr.Body.Len()))
		require.NoError(t, err)
		names := map[string]bool{}
		for _, f := range zr.File {
			names[f.Name] = true
		}
		assert.True(t, names["mmc-pack.json"], "format %s", format)
		assert.True(t, names["instance.cfg"], "format %s", format)
	}
}

func TestParseExportOptions_Defaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/packwiz/packs/x/export/mrpack", nil)
	opts := parseExportOptions(req)
	assert.True(t, opts.IncludeClient)
	assert.Equal(t, "keep", opts.OptionalMode)
	assert.Equal(t, "all", opts.Overrides)

	req2 := httptest.NewRequest(http.MethodGet,
		"/api/v1/packwiz/packs/x/export/mrpack?include_client=false&optional_mode=optional&overrides=none&summary=S", nil)
	opts2 := parseExportOptions(req2)
	assert.False(t, opts2.IncludeClient)
	assert.Equal(t, "optional", opts2.OptionalMode)
	assert.Equal(t, "none", opts2.Overrides)
	assert.Equal(t, "S", opts2.Summary)
}
