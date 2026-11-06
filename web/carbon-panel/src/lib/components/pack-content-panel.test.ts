// SPDX-License-Identifier: MPL-2.0
// Contract tests for the Studio content manager: every packwiz option must be
// reachable for datapacks, resource packs and shader packs.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';

const panel = readFileSync(new URL('./pack-content-panel.svelte', import.meta.url), 'utf8');
const studio = readFileSync(
	path.resolve(import.meta.dirname, '../../routes/modpacks/studio/[id]/+page.svelte'),
	'utf8'
);

test('content panel covers datapacks, resource packs and shader packs', () => {
	assert.ok(panel.includes("type Kind = 'datapack' | 'resourcepack' | 'shaderpack'"));
	assert.ok(studio.includes('kind="datapack"'));
	assert.ok(studio.includes('kind="resourcepack"'));
	assert.ok(studio.includes('kind="shaderpack"'));
	assert.ok(studio.includes("label: 'Datapacks'"));
	assert.ok(studio.includes("label: 'Resource Packs'"));
	assert.ok(studio.includes("label: 'Shaders'"));
});

test('every packwiz metafile option is editable', () => {
	for (const option of [
		'name',
		'file_name',
		'side',
		'pinned',
		'optional',
		'default',
		'option_description'
	]) {
		assert.ok(panel.includes(`editForm.${option}`), `edit dialog must expose ${option}`);
	}
	assert.match(panel, /Pin version/);
	assert.match(panel, /Enabled by default/);
	assert.match(panel, /Option description/);
});

test('all three add sources are present', () => {
	assert.match(panel, /addTab === 'modrinth'/);
	assert.match(panel, /addTab === 'url'/);
	assert.match(panel, /addTab === 'upload'/);
	assert.ok(panel.includes('/content/search?'));
	assert.ok(panel.includes('/content/versions?'));
	assert.ok(panel.includes('/content/url'));
	assert.ok(panel.includes('/content/upload'));
});

test('datapack folder is written through packwiz options', () => {
	assert.ok(panel.includes('/datapack-folder'));
	assert.ok(panel.includes('datapack-folder'));
	assert.ok(panel.includes('moves existing datapack metadata'));
});

test('updates, edits and removal use generic content routes', () => {
	assert.ok(panel.includes('/updates?kind='));
	assert.match(panel, /target_version_id: update\.latest_version_id/);
	assert.match(panel, /method: 'PATCH'/);
	assert.match(panel, /method: 'DELETE'/);
});
