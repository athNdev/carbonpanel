// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const studioList = readFileSync(new URL('./+page.svelte', import.meta.url), 'utf8');
const detail = readFileSync(new URL('./[id]/+page.svelte', import.meta.url), 'utf8');
const exportDialog = readFileSync(new URL('./export-dialog.svelte', import.meta.url), 'utf8');
const inspector = readFileSync(
	new URL('../../../lib/components/manifest-inspector-dialog.svelte', import.meta.url),
	'utf8'
);

test('both studio pages share one export helper', () => {
	assert.match(studioList, /from '.\/pack-export'/);
	assert.match(exportDialog, /from '.\/pack-export'/);
	assert.match(detail, /from '..\/export-dialog.svelte'/);
});

test('studio list exposes the missing CurseForge and MMC exports', () => {
	assert.match(studioList, /exportPack\(pack\.id, 'curseforge'/);
	assert.match(studioList, /exportPack\(pack\.id, 'mmc'/);
});

test('detail header collapses exports into a single Export entry', () => {
	assert.match(detail, /exportDialogOpen = true/);
	assert.doesNotMatch(detail, /exportPack\('packwiz'\)/);
	assert.doesNotMatch(detail, /exportPack\('mrpack'\)/);
	assert.doesNotMatch(detail, /exportPack\('curseforge'\)/);
});

test('detail has a sticky Export and Manifest tab', () => {
	assert.match(detail, /id: 'export', label: 'Export & Manifest'/);
	assert.match(detail, /sticky top-0/);
	assert.match(detail, /Raw pack\.toml Preview/);
	assert.match(detail, /inspectorOpen = true/);
});

test('export dialog warns about CurseForge data loss', () => {
	assert.match(exportDialog, /CurseForge can drop mods/);
	assert.match(exportDialog, /radiogroup/);
	assert.match(exportDialog, /includeClient/);
	assert.match(exportDialog, /optionalMode/);
	assert.match(exportDialog, /overrides/);
});

test('inspector dialog is Carbon-only and keeps every feature', () => {
	for (const component of [
		'CarbonModal',
		'CarbonButton',
		'CarbonTag',
		'CarbonSearch',
		'CarbonStructuredList'
	]) {
		assert.ok(inspector.includes(component), `Inspector must use ${component}`);
	}
	assert.doesNotMatch(inspector, /from '\$lib\/components\/ui\//);
	for (const feature of [
		'handleFileUpload',
		'inspectManifest',
		'exportServerManifest',
		'copyServerManifest',
		'downloadServerManifest',
		'Inspect Another'
	]) {
		assert.ok(inspector.includes(feature), `Inspector must keep ${feature}`);
	}
});

test('new export UI uses Carbon tokens and no emojis', () => {
	for (const [name, source] of [
		['export-dialog', exportDialog],
		['inspector', inspector]
	] as const) {
		assert.doesNotMatch(source, /#[0-9a-fA-F]{6}\b/, `${name} must not use literal hex colors`);
		assert.doesNotMatch(
			source,
			/[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u,
			`${name} must not use emojis`
		);
	}
});
