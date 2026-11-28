// SPDX-License-Identifier: MPL-2.0

// Where a loader stores extension jars. The backend decides this through
// minecraft.GetModLoaderInfo (a loader is plugin-capable exactly when its
// content directory is "plugins", see internal/rpc/handlers/mod_search.go
// pluginLoader); the frontend copy lives here so dialog copy, download paths
// and filter vocabulary cannot drift from the install path.
import { ModLoader } from '$lib/proto/carbonpanel/v1/common_pb';

const PLUGIN_LOADERS = new Set<ModLoader>([
	ModLoader.BUKKIT,
	ModLoader.SPIGOT,
	ModLoader.PAPER,
	ModLoader.PURPUR,
	ModLoader.FOLIA
]);

/** `plugins` for the Bukkit/Paper family, `mods` for every other loader. */
export function contentDirectoryFor(loader: ModLoader): 'mods' | 'plugins' {
	return PLUGIN_LOADERS.has(loader) ? 'plugins' : 'mods';
}

/** True when the loader loads Bukkit/Paper plugins (Hangar territory). */
export function isPluginLoader(loader: ModLoader): boolean {
	return PLUGIN_LOADERS.has(loader);
}

/**
 * Catalogs the mods browser offers for a loader. CurseForge browsing targets
 * its Minecraft "Mods" class, so it is not offered for plugin-capable loaders:
 * there is no verified CurseForge plugin class/loader query to route to, and a
 * hardcoded mod-class search would return content that cannot be installed as a
 * plugin. Such targets browse Modrinth and Hangar's plugin indexes instead.
 */
export function catalogPlatformsFor(loader: ModLoader): Array<'modrinth' | 'curseforge'> {
	return isPluginLoader(loader) ? ['modrinth'] : ['modrinth', 'curseforge'];
}
