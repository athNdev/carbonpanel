// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';

import { ModLoader } from '$lib/proto/carbonpanel/v1/common_pb';
import {
	effectiveLoader,
	isModpackPlatform,
	loaderDisplayName,
	loaderSearchName
} from './mod-loader';

test('every loader maps to a name the mod indexes understand', () => {
	// A wrong loader does not fail loudly: it silently installs mods the server
	// cannot load. NeoForge used to browse Fabric for exactly this reason.
	const expected: Array<[ModLoader, string]> = [
		[ModLoader.VANILLA, 'vanilla'],
		[ModLoader.FORGE, 'forge'],
		[ModLoader.FABRIC, 'fabric'],
		[ModLoader.QUILT, 'quilt'],
		[ModLoader.NEOFORGE, 'neoforge'],
		[ModLoader.PAPER, 'paper'],
		[ModLoader.SPIGOT, 'spigot'],
		[ModLoader.BUKKIT, 'bukkit'],
		[ModLoader.PURPUR, 'purpur'],
		[ModLoader.FOLIA, 'folia'],
		[ModLoader.SPONGE_VANILLA, 'sponge'],
		[ModLoader.SPONGE_FORGE, 'sponge'],
		[ModLoader.MOHIST, 'mohist'],
		[ModLoader.CATSERVER, 'catserver'],
		[ModLoader.ARCLIGHT, 'arclight'],
		[ModLoader.AUTO_CURSEFORGE, ''],
		[ModLoader.MODRINTH, ''],
		[ModLoader.UNSPECIFIED, '']
	];

	for (const [loader, want] of expected) {
		assert.equal(loaderSearchName(loader), want, `ModLoader ${loader}`);
	}
});

test('NeoForge is never reported as a Fabric server', () => {
	// The regression in one line.
	assert.notEqual(loaderSearchName(ModLoader.NEOFORGE), 'fabric');
	assert.notEqual(loaderSearchName(ModLoader.FORGE), 'fabric');
	assert.equal(loaderDisplayName(ModLoader.NEOFORGE), 'NEOFORGE');
	assert.equal(loaderDisplayName(ModLoader.FORGE), 'FORGE');
});

test('a pack-driven or vanilla server shows ANY rather than a wrong loader', () => {
	assert.equal(loaderDisplayName(ModLoader.UNSPECIFIED), 'ANY');
	assert.equal(loaderDisplayName(ModLoader.AUTO_CURSEFORGE), 'ANY');
	assert.equal(loaderDisplayName(ModLoader.MODRINTH), 'ANY');
});

test('platform loaders are recognised as provisioning, not game loaders', () => {
	assert.equal(isModpackPlatform(ModLoader.MODRINTH), true);
	assert.equal(isModpackPlatform(ModLoader.AUTO_CURSEFORGE), true);
	assert.equal(isModpackPlatform(ModLoader.NEOFORGE), false);
	assert.equal(isModpackPlatform(ModLoader.FABRIC), false);
});

test('modpack servers display the game loader, plain servers are unchanged', () => {
	assert.equal(effectiveLoader(ModLoader.MODRINTH, ModLoader.NEOFORGE), ModLoader.NEOFORGE);
	assert.equal(effectiveLoader(ModLoader.AUTO_CURSEFORGE, ModLoader.FABRIC), ModLoader.FABRIC);
	assert.equal(effectiveLoader(ModLoader.MODRINTH, ModLoader.UNSPECIFIED), ModLoader.MODRINTH);
	assert.equal(effectiveLoader(ModLoader.MODRINTH), ModLoader.MODRINTH);
	assert.equal(effectiveLoader(ModLoader.NEOFORGE, ModLoader.FABRIC), ModLoader.NEOFORGE);
});
