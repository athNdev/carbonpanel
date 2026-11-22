// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"fmt"
	"path/filepath"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/minecraft"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
)

// PluginDirectory resolves the directory where the server's loader stores
// plugins. Paper-family loaders declare ModsDirectory "plugins" in the loader
// table, so minecraft.GetModsPath — the same resolution ListMods and
// ImportUploadedMod use — already lands on <dataRoot>/plugins for them. An
// empty string means this loader has no plugin directory.
func PluginDirectory(ctx context.Context, store *storage.Store, server *storage.Server, write bool, resolvers ...*nodestorage.Resolver) (string, error) {
	if server == nil {
		return "", fmt.Errorf("server is required")
	}
	root, err := dataRootFor(ctx, store, server, write, resolvers...)
	if err != nil {
		return "", err
	}
	return minecraft.GetModsPath(root, server.ModLoader), nil
}

// ValidatePluginJar checks that an uploaded jar is installable as a plugin on
// the server's loader, mirroring the ImportUploadedMod guards for mods: the
// loader must declare "plugins" as its content directory (the same rule the
// proto supports_plugins flag uses), the filename must carry the loader's
// extension, and InspectModSide must classify the jar as a Bukkit/Paper
// plugin (plugin.yml or paper-plugin.yml at the jar root). A plugin jar on a
// mod loader is refused as well: a fabric/forge server will silently never
// load a file that only ships plugin metadata.
func ValidatePluginJar(jarPath string, loader storage.ModLoader) error {
	info := minecraft.GetModLoaderInfo(loader)
	if info.ModsDirectory != "plugins" {
		return fmt.Errorf("this server type does not support plugins")
	}
	if !minecraft.IsValidModFile(filepath.Base(jarPath), loader) {
		return fmt.Errorf("invalid file type for this loader: expected %v", info.FileExtensions)
	}
	side, err := minecraft.InspectModSide(jarPath)
	if err != nil {
		return fmt.Errorf("inspect plugin jar: %w", err)
	}
	if side != minecraft.SidePlugin {
		return fmt.Errorf("not a Bukkit/Paper plugin: no plugin.yml or paper-plugin.yml at the jar root")
	}
	return nil
}
