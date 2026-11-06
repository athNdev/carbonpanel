// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import { ModLoader } from '$lib/proto/carbonpanel/v1/common_pb';

/**
 * The name a mod index uses for this loader.
 *
 * Switch on the generated enum, never on numeric literals: the previous helper
 * hard-coded `1 = forge, 2 = fabric, 3 = quilt, 4 = neoforge`, which stopped
 * matching the proto long ago. A NeoForge server therefore browsed Fabric mods,
 * and Forge silently browsed Fabric too. A test walks every ModLoader value so a
 * future renumbering fails at build time instead of in front of a user.
 *
 * An empty string means "no loader filter": vanilla has no mod index categories,
 * and a modpack server's loader is whatever the pack pins.
 */
export function loaderSearchName(loader: ModLoader): string {
	switch (loader) {
		case ModLoader.FORGE:
			return 'forge';
		case ModLoader.FABRIC:
			return 'fabric';
		case ModLoader.NEOFORGE:
			return 'neoforge';
		case ModLoader.QUILT:
			return 'quilt';
		case ModLoader.PAPER:
			return 'paper';
		case ModLoader.SPIGOT:
			return 'spigot';
		case ModLoader.BUKKIT:
			return 'bukkit';
		case ModLoader.PURPUR:
			return 'purpur';
		case ModLoader.FOLIA:
			return 'folia';
		case ModLoader.SPONGE_VANILLA:
		case ModLoader.SPONGE_FORGE:
			return 'sponge';
		case ModLoader.MOHIST:
			return 'mohist';
		case ModLoader.CATSERVER:
			return 'catserver';
		case ModLoader.ARCLIGHT:
			return 'arclight';
		case ModLoader.VANILLA:
			return 'vanilla';
		case ModLoader.AUTO_CURSEFORGE:
		case ModLoader.MODRINTH:
			// The loader comes from the pack, not from the server's own setting.
			return '';
		default:
			return '';
	}
}

/** The short badge label shown next to the server name in the browser. */
export function loaderDisplayName(loader: ModLoader): string {
	const name = loaderSearchName(loader);
	return name === '' ? 'ANY' : name.toUpperCase();
}

/**
 * Whether the loader is a modpack provisioning platform rather than a game
 * loader. The container must keep booting from the platform; displays must
 * not present it as the mod loader.
 */
export function isModpackPlatform(loader: ModLoader): boolean {
	return loader === ModLoader.AUTO_CURSEFORGE || loader === ModLoader.MODRINTH;
}

/**
 * The loader to show and to filter mod indexes by. Modpack servers carry the
 * platform in modLoader and the real game loader in modpackLoader; every
 * other server shows modLoader unchanged.
 */
export function effectiveLoader(modLoader: ModLoader, modpackLoader?: ModLoader): ModLoader {
	if (
		modpackLoader !== undefined &&
		modpackLoader !== ModLoader.UNSPECIFIED &&
		isModpackPlatform(modLoader)
	) {
		return modpackLoader;
	}
	return modLoader;
}
