// SPDX-License-Identifier: MPL-2.0

import { describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { ModLoader } from '$lib/proto/carbonpanel/v1/common_pb';
import { catalogPlatformsFor, contentDirectoryFor, isPluginLoader } from './loader-directories';

describe('contentDirectoryFor', () => {
	test('the Bukkit/Paper family stores content in plugins/', () => {
		for (const loader of [
			ModLoader.BUKKIT,
			ModLoader.SPIGOT,
			ModLoader.PAPER,
			ModLoader.PURPUR,
			ModLoader.FOLIA
		]) {
			assert.equal(contentDirectoryFor(loader), 'plugins', `${loader} is plugin-capable`);
			assert.equal(isPluginLoader(loader), true);
		}
	});

	test('mod loaders store content in mods/', () => {
		for (const loader of [
			ModLoader.VANILLA,
			ModLoader.FORGE,
			ModLoader.NEOFORGE,
			ModLoader.FABRIC,
			ModLoader.QUILT,
			ModLoader.MOHIST,
			ModLoader.CATSERVER,
			ModLoader.ARCLIGHT,
			ModLoader.UNSPECIFIED
		]) {
			assert.equal(contentDirectoryFor(loader), 'mods', `${loader} is a mod loader`);
			assert.equal(isPluginLoader(loader), false);
		}
	});
});

describe('catalogPlatformsFor', () => {
	test('plugin-capable loaders do not offer the CurseForge mod catalog', () => {
		// CurseForge browsing is pinned to its Minecraft mod class and no
		// verified plugin class/loader query exists, so a plugin target would
		// get uninstallable mod results. Modrinth still contributes plugins.
		for (const loader of [
			ModLoader.BUKKIT,
			ModLoader.SPIGOT,
			ModLoader.PAPER,
			ModLoader.PURPUR,
			ModLoader.FOLIA
		]) {
			assert.deepEqual(catalogPlatformsFor(loader), ['modrinth'], `${loader} is plugin-capable`);
		}
	});

	test('mod loaders keep both catalogs', () => {
		for (const loader of [ModLoader.VANILLA, ModLoader.FORGE, ModLoader.FABRIC, ModLoader.QUILT]) {
			assert.deepEqual(catalogPlatformsFor(loader), ['modrinth', 'curseforge']);
		}
	});
});
