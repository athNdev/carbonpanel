// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package packwiz

import (
	"archive/zip"
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newExportTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "packwiz_export_*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	return NewManager(tempDir, logger.New()), tempDir
}

func seedHashPack(t *testing.T, mgr *Manager) {
	t.Helper()
	_, err := mgr.CreatePack(&Pack{
		ID: "hash-pack", Name: "Hash Pack", Author: "Test",
		Version: "1.0.0", MCVersion: "1.20.1",
		ModLoader: "fabric", LoaderVersion: "0.15.11",
	})
	require.NoError(t, err)
	require.NoError(t, mgr.AddMod("hash-pack", ModItem{
		Slug: "sodium", Name: "Sodium", FileName: "sodium.jar",
		Side: "both", Platform: "modrinth", ProjectID: "AANobbMI",
		VersionID:   "v1",
		DownloadURL: "https://cdn.modrinth.com/sodium.jar",
		FileSize:    42,
		Hash:        "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		HashFormat:  "sha256",
	}))
	require.NoError(t, mgr.AddMod("hash-pack", ModItem{
		Slug: "clientmod", Name: "ClientMod", FileName: "clientmod.jar",
		Side: "client", Platform: "modrinth", ProjectID: "xyz",
		VersionID:   "v2",
		DownloadURL: "https://cdn.modrinth.com/clientmod.jar",
	}))
	require.NoError(t, mgr.SavePackFile("hash-pack", "config/settings.json", strings.NewReader(`{"a":1}`)))
}

func readZipFile(t *testing.T, zr *zip.Reader, name string) []byte {
	t.Helper()
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			require.NoError(t, err)
			defer func() { _ = rc.Close() }()
			var buf bytes.Buffer
			_, err = buf.ReadFrom(rc)
			require.NoError(t, err)
			return buf.Bytes()
		}
	}
	t.Fatalf("file %q not found in zip", name)
	return nil
}

func TestExportMrpack_RealHashes(t *testing.T) {
	mgr, _ := newExportTestManager(t)
	seedHashPack(t, mgr)

	var buf bytes.Buffer
	require.NoError(t, mgr.ExportMrpack("hash-pack", &buf, ExportOptions{IncludeClient: true}))

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	raw := readZipFile(t, zr, "modrinth.index.json")

	var index struct {
		Files []struct {
			Path   string            `json:"path"`
			Hashes map[string]string `json:"hashes"`
			Env    map[string]string `json:"env"`
		} `json:"files"`
	}
	require.NoError(t, json.Unmarshal(raw, &index))
	require.Len(t, index.Files, 2)

	var sodium map[string]string
	for _, f := range index.Files {
		if strings.Contains(f.Path, "sodium.jar") {
			sodium = f.Hashes
		}
	}
	require.NotNil(t, sodium)
	assert.Equal(t,
		"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		sodium["sha256"])

	emptySHA512 := hex.EncodeToString(sha512.New().Sum(nil))
	for _, f := range index.Files {
		if v, ok := f.Hashes["sha512"]; ok {
			assert.NotEqual(t, emptySHA512, v, "placeholder sha512(empty) must never ship")
		}
	}
}

func TestExportMrpack_NoPlaceholderWhenHashMissing(t *testing.T) {
	mgr, _ := newExportTestManager(t)
	seedHashPack(t, mgr)

	var buf bytes.Buffer
	require.NoError(t, mgr.ExportMrpack("hash-pack", &buf, ExportOptions{IncludeClient: true}))

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	raw := readZipFile(t, zr, "modrinth.index.json")

	emptySHA512 := hex.EncodeToString(sha512.New().Sum(nil))
	assert.NotContains(t, string(raw), emptySHA512)
}

func TestExportMrpack_IncludeClientFilter(t *testing.T) {
	mgr, _ := newExportTestManager(t)
	seedHashPack(t, mgr)

	var full bytes.Buffer
	require.NoError(t, mgr.ExportMrpack("hash-pack", &full, ExportOptions{IncludeClient: true}))
	zr, err := zip.NewReader(bytes.NewReader(full.Bytes()), int64(full.Len()))
	require.NoError(t, err)
	assert.Contains(t, string(readZipFile(t, zr, "modrinth.index.json")), "clientmod.jar")

	var serverOnly bytes.Buffer
	require.NoError(t, mgr.ExportMrpack("hash-pack", &serverOnly, ExportOptions{IncludeClient: false}))
	zr2, err := zip.NewReader(bytes.NewReader(serverOnly.Bytes()), int64(serverOnly.Len()))
	require.NoError(t, err)
	body := string(readZipFile(t, zr2, "modrinth.index.json"))
	assert.NotContains(t, body, "clientmod.jar")
	assert.Contains(t, body, "sodium.jar")
}

func TestExportMrpack_OptionalAndSummary(t *testing.T) {
	mgr, _ := newExportTestManager(t)
	seedHashPack(t, mgr)

	var buf bytes.Buffer
	require.NoError(t, mgr.ExportMrpack("hash-pack", &buf, ExportOptions{
		IncludeClient: true, OptionalMode: "optional", Summary: "Custom summary",
	}))
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	raw := readZipFile(t, zr, "modrinth.index.json")
	var index struct {
		Summary string `json:"summary"`
		Files   []struct {
			Env map[string]string `json:"env"`
		} `json:"files"`
	}
	require.NoError(t, json.Unmarshal(raw, &index))
	assert.Equal(t, "Custom summary", index.Summary)
	for _, f := range index.Files {
		for _, v := range f.Env {
			if v != "unsupported" {
				assert.Equal(t, "optional", v)
			}
		}
	}
}

func TestExportMrpack_OverridesPolicy(t *testing.T) {
	mgr, _ := newExportTestManager(t)
	seedHashPack(t, mgr)

	var noneBuf bytes.Buffer
	require.NoError(t, mgr.ExportMrpack("hash-pack", &noneBuf, ExportOptions{IncludeClient: true, Overrides: "none"}))
	zr, err := zip.NewReader(bytes.NewReader(noneBuf.Bytes()), int64(noneBuf.Len()))
	require.NoError(t, err)
	for _, f := range zr.File {
		assert.NotContains(t, f.Name, "overrides/config/settings.json")
	}

	var allBuf bytes.Buffer
	require.NoError(t, mgr.ExportMrpack("hash-pack", &allBuf, ExportOptions{IncludeClient: true, Overrides: "all"}))
	zr2, err := zip.NewReader(bytes.NewReader(allBuf.Bytes()), int64(allBuf.Len()))
	require.NoError(t, err)
	found := false
	for _, f := range zr2.File {
		if f.Name == "overrides/config/settings.json" {
			found = true
		}
	}
	assert.True(t, found, "overrides must ship with policy all")
}

func TestExportCurseForge_BlocksDroppedMods(t *testing.T) {
	mgr, _ := newExportTestManager(t)
	seedHashPack(t, mgr)

	var buf bytes.Buffer
	err := mgr.ExportCurseForge("hash-pack", &buf, ExportOptions{IncludeClient: true})
	require.Error(t, err)
	var dropped *DroppedModsError
	require.ErrorAs(t, err, &dropped)
	assert.Len(t, dropped.Dropped, 2)
	assert.Contains(t, err.Error(), "Sodium")
}

func TestExportCurseForge_AllCFShipsOverrides(t *testing.T) {
	mgr, _ := newExportTestManager(t)
	_, err := mgr.CreatePack(&Pack{
		ID: "cf-pack", Name: "CF Pack", Version: "1.0.0",
		MCVersion: "1.20.1", ModLoader: "forge", LoaderVersion: "47.2.0",
	})
	require.NoError(t, err)
	require.NoError(t, mgr.AddMod("cf-pack", ModItem{
		Slug: "jei", Name: "JEI", FileName: "jei.jar", Side: "both",
		Platform: "curseforge", ProjectID: "238222", VersionID: "4635832",
		DownloadURL: "https://edge.forgecdn.net/jei.jar",
	}))
	require.NoError(t, mgr.SavePackFile("cf-pack", "config/jei.json", strings.NewReader(`{}`)))

	var buf bytes.Buffer
	require.NoError(t, mgr.ExportCurseForge("cf-pack", &buf, ExportOptions{IncludeClient: true}))
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)

	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	assert.True(t, names["manifest.json"])
	assert.True(t, names["modlist.html"])
	assert.True(t, names["overrides/config/jei.json"], "CF export must bundle overrides, not ship them empty")

	raw := readZipFile(t, zr, "manifest.json")
	var manifest struct {
		Files []struct {
			ProjectID int  `json:"projectID"`
			FileID    int  `json:"fileID"`
			Required  bool `json:"required"`
		} `json:"files"`
	}
	require.NoError(t, json.Unmarshal(raw, &manifest))
	require.Len(t, manifest.Files, 1)
	assert.Equal(t, 238222, manifest.Files[0].ProjectID)
	assert.True(t, manifest.Files[0].Required)
}

func TestExportMMC_InstanceZip(t *testing.T) {
	mgr, tmp := newExportTestManager(t)
	seedHashPack(t, mgr)

	var buf bytes.Buffer
	require.NoError(t, mgr.ExportMMC("hash-pack", &buf, ExportOptions{IncludeClient: true}))
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)

	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	assert.True(t, names["mmc-pack.json"])
	assert.True(t, names["instance.cfg"])
	assert.True(t, names["modlist.txt"])

	raw := readZipFile(t, zr, "mmc-pack.json")
	var mmc struct {
		Components []struct {
			UID     string `json:"uid"`
			Version string `json:"version"`
		} `json:"components"`
		FormatVersion int `json:"formatVersion"`
	}
	require.NoError(t, json.Unmarshal(raw, &mmc))
	assert.Equal(t, 1, mmc.FormatVersion)
	uids := []string{}
	for _, c := range mmc.Components {
		uids = append(uids, c.UID)
	}
	assert.Contains(t, uids, "net.minecraft")
	assert.Contains(t, uids, "net.fabricmc.fabric-loader")

	cfg := string(readZipFile(t, zr, "instance.cfg"))
	assert.Contains(t, cfg, "Hash Pack")
	modlist := string(readZipFile(t, zr, "modlist.txt"))
	assert.Contains(t, modlist, "sodium.jar")

	// Server-only variant drops the client mod from the reused mod list.
	var serverBuf bytes.Buffer
	require.NoError(t, mgr.ExportMMC("hash-pack", &serverBuf, ExportOptions{IncludeClient: false}))
	zr2, err := zip.NewReader(bytes.NewReader(serverBuf.Bytes()), int64(serverBuf.Len()))
	require.NoError(t, err)
	assert.NotContains(t, string(readZipFile(t, zr2, "modlist.txt")), "clientmod.jar")

	_ = filepath.Join(tmp, "keep")
}

func TestExportMrpack_RoundTripPreservesHash(t *testing.T) {
	mgr, _ := newExportTestManager(t)

	var mrpackBuf bytes.Buffer
	zw := zip.NewWriter(&mrpackBuf)
	idx := map[string]any{
		"formatVersion": 1, "game": "minecraft", "versionId": "1.0.0",
		"name":    "RoundTrip",
		"summary": "rt",
		"dependencies": map[string]string{
			"minecraft": "1.20.1", "fabric-loader": "0.15.11",
		},
		"files": []map[string]any{{
			"path":      "mods/rt.jar",
			"hashes":    map[string]string{"sha256": "deadbeef"},
			"env":       map[string]string{"client": "required", "server": "required"},
			"downloads": []string{"https://example.com/rt.jar"},
			"fileSize":  10,
		}},
	}
	idxBytes, err := json.Marshal(idx)
	require.NoError(t, err)
	f, err := zw.Create("modrinth.index.json")
	require.NoError(t, err)
	_, err = f.Write(idxBytes)
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	pack, err := mgr.ImportMrpack(bytes.NewReader(mrpackBuf.Bytes()), int64(mrpackBuf.Len()), ImportOptions{})
	require.NoError(t, err)
	require.Len(t, pack.Mods, 1)
	assert.Equal(t, "deadbeef", pack.Mods[0].Hash)

	var out bytes.Buffer
	require.NoError(t, mgr.ExportMrpack(pack.ID, &out, ExportOptions{IncludeClient: true}))
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	require.NoError(t, err)
	assert.Contains(t, string(readZipFile(t, zr, "modrinth.index.json")), "deadbeef")
}
