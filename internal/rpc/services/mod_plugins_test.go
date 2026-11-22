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
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/minecraft"
	"github.com/stretchr/testify/require"
)

// writePluginJar archives the given entries into a jar-shaped zip file.
func writePluginJar(t *testing.T, path string, entries map[string]string) {
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

func TestValidatePluginJar(t *testing.T) {
	dir := t.TempDir()

	plugin := filepath.Join(dir, "essentialsx.jar")
	writePluginJar(t, plugin, map[string]string{"plugin.yml": "name: EssentialsX\nversion: 2.20.1\n"})

	paperPlugin := filepath.Join(dir, "paperplugin.jar")
	writePluginJar(t, paperPlugin, map[string]string{"paper-plugin.yml": "name: PaperPlugin\n"})

	mod := filepath.Join(dir, "lithium.jar")
	writePluginJar(t, mod, map[string]string{"fabric.mod.json": `{"environment": "*"}`})

	plain := filepath.Join(dir, "notajar.jar")
	require.NoError(t, os.WriteFile(plain, []byte("plain text"), 0o644))

	require.NoError(t, ValidatePluginJar(plugin, storage.ModLoaderPaper))
	require.NoError(t, ValidatePluginJar(paperPlugin, storage.ModLoaderPaper))

	// Mod loaders are not plugin loaders, and vanilla has no content dir.
	require.Error(t, ValidatePluginJar(plugin, storage.ModLoaderFabric))
	require.Error(t, ValidatePluginJar(plugin, storage.ModLoaderVanilla))

	// Wrong extension, unparseable archive, and a mod jar without a plugin
	// descriptor are all refused.
	require.Error(t, ValidatePluginJar(filepath.Join(dir, "notajar.zip"), storage.ModLoaderPaper), "extension check")
	require.Error(t, ValidatePluginJar(plain, storage.ModLoaderPaper), "zip check")
	require.Error(t, ValidatePluginJar(mod, storage.ModLoaderPaper), "mod jar is not a plugin")
}

func TestPluginDirectoryFollowsLoaderTable(t *testing.T) {
	store, err := storage.NewSQLiteStore(&config.Config{
		Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "plugins.db")},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.Migrate())

	ctx := context.Background()
	dataRoot := t.TempDir()
	server := &storage.Server{
		ID: "srv-plugins", Name: "plugins", ModLoader: storage.ModLoaderPaper,
		MCVersion: "1.21.1", NodeID: "local", DataPath: dataRoot, Status: storage.StatusStopped,
	}
	require.NoError(t, store.CreateServer(ctx, server))

	pluginsDir, err := PluginDirectory(ctx, store, server, true)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dataRoot, "plugins"), pluginsDir)

	// The read variant maps without creating anything.
	mapped, err := PluginDirectory(ctx, store, server, false)
	require.NoError(t, err)
	require.Equal(t, pluginsDir, mapped)

	// Vanilla declares no content directory at all.
	server.ModLoader = storage.ModLoaderVanilla
	none, err := PluginDirectory(ctx, store, server, false)
	require.NoError(t, err)
	require.Empty(t, none)

	// Fabric keeps writing to mods/, not plugins/.
	server.ModLoader = storage.ModLoaderFabric
	modsDir, err := PluginDirectory(ctx, store, server, false)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dataRoot, "mods"), modsDir)

	// A nil server is refused, not guessed.
	_, err = PluginDirectory(ctx, store, nil, false)
	require.Error(t, err)
}

func TestPluginDirectoryMatchesGetModsPath(t *testing.T) {
	dataRoot := t.TempDir()
	for _, loader := range []storage.ModLoader{
		storage.ModLoaderPaper, storage.ModLoaderPurpur, storage.ModLoaderPufferfish,
		storage.ModLoaderFolia, storage.ModLoaderBukkit, storage.ModLoaderSpigot,
	} {
		dir, err := PluginDirectory(context.Background(), nil, &storage.Server{
			ModLoader: loader, NodeID: "local", DataPath: dataRoot,
		}, false)
		require.NoError(t, err)
		require.Equal(t, minecraft.GetModsPath(dataRoot, loader), dir, "loader %q", loader)
		require.NotEmpty(t, dir, "plugin-capable loader %q must declare a content directory", loader)
	}
}
