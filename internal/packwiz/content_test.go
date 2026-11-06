// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package packwiz

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContentRoundTripPreservesEveryOption(t *testing.T) {
	mgr := newDatapackTestManager(t)
	items := []ModItem{
		{
			Kind: ContentKindDatapack, Name: "Terralith", FileName: "terralith.zip",
			Side: "server", Platform: "modrinth", ProjectID: "proj-dp", VersionID: "ver-dp",
			DownloadURL: "https://cdn.example/dp.zip", Pinned: true, Optional: true,
			Default: false, OptionDescription: "Optional world generation",
			HashFormat: "sha512", Hash: "dp-hash", SHA512: "dp-hash",
		},
		{
			Kind: ContentKindResourcePack, Name: "Fresh Animations", FileName: "fresh.zip",
			Side: "client", Platform: "modrinth", ProjectID: "proj-rp", VersionID: "ver-rp",
			DownloadURL: "https://cdn.example/rp.zip", Optional: false,
			HashFormat: "sha512", Hash: "rp-hash", SHA512: "rp-hash",
		},
		{
			Kind: ContentKindShaderPack, Name: "Complementary", FileName: "comp.zip",
			Side: "client", Platform: "modrinth", ProjectID: "proj-sp", VersionID: "ver-sp",
			DownloadURL: "https://cdn.example/sp.zip", Optional: true, Default: true,
			OptionDescription: "High quality preset", HashFormat: "sha1", Hash: "sp-hash",
		},
	}
	for _, item := range items {
		require.NoError(t, mgr.AddContent("dp-pack", item))
	}

	pack, err := mgr.GetPack("dp-pack")
	require.NoError(t, err)
	require.Len(t, pack.Datapacks, 1)
	require.Len(t, pack.ResourcePacks, 1)
	require.Len(t, pack.ShaderPacks, 1)
	require.Equal(t, "datapacks", pack.DatapackFolder)

	dp := pack.Datapacks[0]
	require.Equal(t, ContentKindDatapack, dp.Kind)
	require.True(t, dp.Pinned)
	require.True(t, dp.Optional)
	require.False(t, dp.Default)
	require.Equal(t, "Optional world generation", dp.OptionDescription)
	require.Equal(t, "sha512", dp.HashFormat)
	require.Equal(t, "dp-hash", dp.Hash)

	sp := pack.ShaderPacks[0]
	require.True(t, sp.Optional)
	require.True(t, sp.Default)
	require.Equal(t, "High quality preset", sp.OptionDescription)

	// packwiz stores pinning at the top level and optional metadata in
	// [option]; datapack-folder lives in [options].
	meta, err := os.ReadFile(filepath.Join(mgr.packDir("dp-pack"), "datapacks", "terralith.pw.toml"))
	require.NoError(t, err)
	text := string(meta)
	require.Contains(t, text, "pin = true")
	require.Contains(t, text, "[option]")
	require.Contains(t, text, "optional = true")
	require.Contains(t, text, "Optional world generation")

	packTOML, err := os.ReadFile(filepath.Join(mgr.packDir("dp-pack"), "pack.toml"))
	require.NoError(t, err)
	require.Contains(t, string(packTOML), "[options]")
	require.Contains(t, string(packTOML), "datapack-folder")
	require.Contains(t, string(packTOML), "datapacks")
}

func TestUpdateAndDeleteContent(t *testing.T) {
	mgr := newDatapackTestManager(t)
	require.NoError(t, mgr.AddContent("dp-pack", ModItem{
		Kind: ContentKindResourcePack, Name: "Pack", FileName: "pack.zip",
		Side: "client", Platform: "url", DownloadURL: "https://example.invalid/pack.zip",
	}))

	name := "Renamed Pack"
	side := "both"
	pinned := true
	optional := true
	desc := "Enabled by default"
	require.NoError(t, mgr.UpdateContent("dp-pack", ContentKindResourcePack, "pack", ContentPatch{
		Name: &name, Side: &side, Pinned: &pinned, Optional: &optional, OptionDescription: &desc,
	}))

	pack, err := mgr.GetPack("dp-pack")
	require.NoError(t, err)
	require.Equal(t, "Renamed Pack", pack.ResourcePacks[0].Name)
	require.Equal(t, "both", pack.ResourcePacks[0].Side)
	require.True(t, pack.ResourcePacks[0].Pinned)
	require.True(t, pack.ResourcePacks[0].Optional)
	require.Equal(t, "Enabled by default", pack.ResourcePacks[0].OptionDescription)

	require.NoError(t, mgr.DeleteContent("dp-pack", ContentKindResourcePack, "pack"))
	pack, err = mgr.GetPack("dp-pack")
	require.NoError(t, err)
	require.Empty(t, pack.ResourcePacks)
}

func TestSetDatapackFolderMovesMetadataAndRewritesOwnedURLs(t *testing.T) {
	mgr := newDatapackTestManager(t)
	serveURL := "http://panel/api/v1/packwiz/dp-pack/datapacks/terralith.zip"
	archive := zipArchive(t, map[string]string{
		"pack.mcmeta": `{"pack":{"pack_format":34}}`,
	})
	_, err := mgr.AddContentFile("dp-pack", ContentKindDatapack, "terralith.zip",
		ContentUploadMetadata{Name: "Terralith", Side: "server"}, serveURL, archive)
	require.NoError(t, err)

	require.NoError(t, mgr.SetDatapackFolder("dp-pack", "config/openloader/data"))
	pack, err := mgr.GetPack("dp-pack")
	require.NoError(t, err)
	require.Equal(t, "config/openloader/data", pack.DatapackFolder)
	require.Contains(t, pack.Datapacks[0].DownloadURL, "/config/openloader/data/terralith.zip")

	require.FileExists(t, filepath.Join(mgr.packDir("dp-pack"), "config", "openloader", "data", "terralith.zip"))
	require.FileExists(t, filepath.Join(mgr.packDir("dp-pack"), "config", "openloader", "data", "terralith.pw.toml"))
}

func TestAddContentFileValidatesKindAndHashes(t *testing.T) {
	mgr := newDatapackTestManager(t)
	valid := zipArchive(t, map[string]string{
		"pack.mcmeta":             `{"pack":{"pack_format":34}}`,
		"assets/example/icon.png": "",
	})
	item, err := mgr.AddContentFile("dp-pack", ContentKindResourcePack, "textures.zip",
		ContentUploadMetadata{Name: "Textures", Side: "client", Pinned: true},
		"http://panel/api/v1/packwiz/dp-pack/resourcepacks/textures.zip", valid)
	require.NoError(t, err)
	require.Equal(t, ContentKindResourcePack, item.Kind)
	require.Len(t, item.SHA512, 128)
	require.Equal(t, "sha512", item.HashFormat)

	require.FileExists(t, filepath.Join(mgr.packDir("dp-pack"), "resourcepacks", "textures.zip"))
	require.FileExists(t, filepath.Join(mgr.packDir("dp-pack"), "resourcepacks", "textures.pw.toml"))

	invalid := zipArchive(t, map[string]string{"shaders/x.fsh": ""})
	_, err = mgr.AddContentFile("dp-pack", ContentKindResourcePack, "bad.zip",
		ContentUploadMetadata{Name: "Bad"}, "http://panel/x.zip", invalid)
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(mgr.packDir("dp-pack"), "resourcepacks", "bad.zip"))
}

func TestExportMrpackIncludesContentKindsAndSkipsDuplicates(t *testing.T) {
	mgr := newDatapackTestManager(t)
	uploads := []struct {
		kind  string
		file  string
		entry map[string]string
		url   string
	}{
		{ContentKindDatapack, "world.zip", map[string]string{"pack.mcmeta": `{"pack":{"pack_format":34}}`}, "http://panel/api/v1/packwiz/dp-pack/datapacks/world.zip"},
		{ContentKindResourcePack, "textures.zip", map[string]string{"pack.mcmeta": `{}`, "assets/x/y.png": ""}, "http://panel/api/v1/packwiz/dp-pack/resourcepacks/textures.zip"},
		{ContentKindShaderPack, "shiny.zip", map[string]string{"shaders/final.fsh": ""}, "http://panel/api/v1/packwiz/dp-pack/shaderpacks/shiny.zip"},
	}
	for _, upload := range uploads {
		_, err := mgr.AddContentFile("dp-pack", upload.kind, upload.file,
			ContentUploadMetadata{Name: upload.file}, upload.url, zipArchive(t, upload.entry))
		require.NoError(t, err)
	}

	var buf bytes.Buffer
	require.NoError(t, mgr.ExportMrpack("dp-pack", &buf))

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	require.True(t, names["modrinth.index.json"])
	// The archives install through the download entries, never as overrides.
	require.False(t, names["overrides/datapacks/world.zip"])
	require.False(t, names["overrides/resourcepacks/textures.zip"])
	require.False(t, names["overrides/shaderpacks/shiny.zip"])
	require.False(t, names["overrides/datapacks/world.pw.toml"])

	var index struct {
		Files []struct {
			Path   string            `json:"path"`
			Hashes map[string]string `json:"hashes"`
		} `json:"files"`
	}
	raw, err := zr.Open("modrinth.index.json")
	require.NoError(t, err)
	data, err := io.ReadAll(raw)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &index))

	paths := map[string]map[string]string{}
	for _, file := range index.Files {
		paths[file.Path] = file.Hashes
	}
	require.Contains(t, paths, "datapacks/world.zip")
	require.Contains(t, paths, "resourcepacks/textures.zip")
	require.Contains(t, paths, "shaderpacks/shiny.zip")
	require.NotEmpty(t, paths["datapacks/world.zip"]["sha512"])
}

func TestWritePackFilesFallsBackForLegacyEmptyFilename(t *testing.T) {
	mgr := newDatapackTestManager(t)
	require.NoError(t, mgr.AddContent("dp-pack", ModItem{
		Kind: ContentKindShaderPack, Name: "Legacy", Side: "client",
		Platform: "url", DownloadURL: "https://example.invalid/legacy.zip",
		HashFormat: "sha256", Hash: "abc",
	}))
	metaPath := filepath.Join(mgr.packDir("dp-pack"), "shaderpacks", "legacy.pw.toml")
	require.FileExists(t, metaPath)
	meta, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	require.Contains(t, string(meta), "legacy.zip")
	require.NotContains(t, meta, "shaderpack.pw.toml")
}

func TestBakeToServerContentInstallsServerContentOnly(t *testing.T) {
	mgr := newDatapackTestManager(t)

	files := map[string][]byte{
		"/mod-server.jar":    []byte("server mod"),
		"/mod-client.jar":    []byte("client mod"),
		"/terralith.zip":     []byte("datapack"),
		"/clientdp.zip":      []byte("client datapack"),
		"/fresh.zip":         []byte("resource pack"),
		"/complementary.zip": []byte("shader pack"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	items := []ModItem{
		{Kind: ContentKindMod, Slug: "server-mod", Name: "Server Mod", FileName: "server-mod.jar",
			Side: "server", DownloadURL: srv.URL + "/mod-server.jar"},
		{Kind: ContentKindMod, Slug: "client-mod", Name: "Client Mod", FileName: "mod-client.jar",
			Side: "client", DownloadURL: srv.URL + "/mod-client.jar"},
		{Kind: ContentKindDatapack, Slug: "terralith", Name: "Terralith", FileName: "terralith.zip",
			Side: "server", DownloadURL: srv.URL + "/terralith.zip"},
		{Kind: ContentKindDatapack, Slug: "clientdp", Name: "Client Datapack", FileName: "clientdp.zip",
			Side: "client", DownloadURL: srv.URL + "/clientdp.zip"},
		{Kind: ContentKindResourcePack, Slug: "fresh", Name: "Fresh Animations", FileName: "fresh.zip",
			Side: "client", DownloadURL: srv.URL + "/fresh.zip"},
		{Kind: ContentKindShaderPack, Slug: "complementary", Name: "Complementary", FileName: "complementary.zip",
			Side: "client", DownloadURL: srv.URL + "/complementary.zip"},
	}
	for _, item := range items {
		require.NoError(t, mgr.AddContent("dp-pack", item))
	}

	modsDir := filepath.Join(t.TempDir(), "mods")
	datapacksDir := filepath.Join(t.TempDir(), "world", "datapacks")
	res, err := mgr.BakeToServerContent("dp-pack", modsDir, datapacksDir)
	require.NoError(t, err)
	require.Equal(t, 1, res.Mods)
	require.Equal(t, 1, res.Datapacks)
	require.Equal(t, 4, res.ClientOnly)

	require.FileExists(t, filepath.Join(modsDir, "server-mod.jar"))
	require.NoFileExists(t, filepath.Join(modsDir, "mod-client.jar"))
	require.FileExists(t, filepath.Join(datapacksDir, "terralith.zip"))
	require.NoFileExists(t, filepath.Join(datapacksDir, "clientdp.zip"))
	require.NoFileExists(t, filepath.Join(datapacksDir, "fresh.zip"))
	require.NoFileExists(t, filepath.Join(datapacksDir, "complementary.zip"))

	// The legacy entry point still reports the mod count and installs nothing
	// outside the mods directory.
	legacyDir := filepath.Join(t.TempDir(), "mods")
	installed, err := mgr.BakeToServer("dp-pack", legacyDir)
	require.NoError(t, err)
	require.Equal(t, 1, installed)
	require.FileExists(t, filepath.Join(legacyDir, "server-mod.jar"))
}
