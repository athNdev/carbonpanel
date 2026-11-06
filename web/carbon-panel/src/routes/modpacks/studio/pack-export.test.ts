// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';

import {
	DEFAULT_EXPORT_OPTIONS,
	EXPORT_FORMATS,
	buildExportQuery,
	findCurseForgeDroppedMods,
	formatExportLabel,
	getExportFilename
} from './pack-export';

test('every launcher format is offered with a helper hint', () => {
	const ids = EXPORT_FORMATS.map((f) => f.id);
	for (const expected of ['mrpack', 'curseforge', 'mmc', 'packwiz']) {
		assert.ok(ids.includes(expected as never), `Export menu must offer ${expected}`);
	}
});

test('default export keeps client mods and all overrides', () => {
	assert.equal(DEFAULT_EXPORT_OPTIONS.includeClient, true);
	assert.equal(DEFAULT_EXPORT_OPTIONS.optionalMode, 'keep');
	assert.equal(DEFAULT_EXPORT_OPTIONS.overrides, 'all');
});

test('export query encodes per-launcher options', () => {
	const q = buildExportQuery({
		includeClient: false,
		optionalMode: 'optional',
		overrides: 'configs-only',
		summary: 'Hello'
	});
	assert.match(q, /include_client=false/);
	assert.match(q, /optional_mode=optional/);
	assert.match(q, /overrides=configs-only/);
	assert.match(q, /summary=Hello/);
});

test('export filenames match launcher expectations', () => {
	assert.equal(getExportFilename('Odyssey', 'mrpack'), 'Odyssey.mrpack');
	assert.equal(getExportFilename('Odyssey', 'packwiz'), 'Odyssey.packwiz.zip');
	assert.equal(getExportFilename('Odyssey', 'mmc'), 'Odyssey-mmc.zip');
	assert.equal(getExportFilename('Odyssey', 'curseforge'), 'Odyssey.zip');
	assert.equal(formatExportLabel('mmc'), 'instance.zip');
});

test('CurseForge data-loss preview flags non-CF mods', () => {
	const dropped = findCurseForgeDroppedMods([
		{ name: 'Sodium', platform: 'modrinth', project_id: 'AANobbMI', version_id: 'v1' },
		{ name: 'JEI', platform: 'curseforge', project_id: '238222', version_id: '4635832' },
		{ name: 'Direct', platform: 'url', project_id: 'x', version_id: 'y' }
	]);
	assert.deepEqual(dropped, ['Sodium', 'Direct']);
});
