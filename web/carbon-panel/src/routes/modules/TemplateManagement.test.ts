// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const template = readFileSync(new URL('./TemplateManagement.svelte', import.meta.url), 'utf8');
const active = readFileSync(new URL('./ActiveModules.svelte', import.meta.url), 'utf8');
const table = readFileSync(
	new URL('../../lib/components/carbon/CarbonDataTable.svelte', import.meta.url),
	'utf8'
);

test('tables scroll horizontally instead of overflowing the page', () => {
	assert.ok(
		table.includes('overflow-x-auto'),
		'CarbonDataTable must wrap the table in overflow-x-auto so wide module tables scroll'
	);
});

test('TemplateManagement truncates the template name with a title', () => {
	assert.match(template, /title=\{template\.name\}/);
	assert.ok(template.includes('truncate'), 'template name must truncate');
});

test('TemplateManagement keeps Type/Category/Actions on one line', () => {
	const nowrapCells = template.match(/<td[^>]*whitespace-nowrap[^>]*>/g) ?? [];
	assert.ok(
		nowrapCells.length >= 3,
		`expected Type/Category/Actions cells to be nowrap, found ${nowrapCells.length}`
	);
});

test('TemplateManagement keeps Docker image and description truncated', () => {
	assert.match(template, /max-w-\[180px\] truncate/);
	assert.match(template, /max-w-\[240px\] truncate/);
	assert.ok(template.includes('title={template.dockerImage}'));
	assert.ok(template.includes('title={template.description}'));
});

test('ActiveModules truncates module and template names with titles', () => {
	assert.match(active, /title=\{module\.name\}/);
	assert.match(active, /title=\{module\.serverName \|\| module\.serverId\}/);
	assert.match(active, /title=\{module\.templateName\}/);
	assert.ok(active.includes('max-w-[180px]'), 'templateName column must cap its width');
	assert.ok(active.includes('max-w-[240px]'), 'module/server column must cap its width');
});

test('ActiveModules keeps status, metrics and actions on one line', () => {
	const nowrapCells = active.match(/<td[^>]*whitespace-nowrap[^>]*>/g) ?? [];
	assert.ok(
		nowrapCells.length >= 5,
		`expected status/memory/cpu/auto-start/actions cells to be nowrap, found ${nowrapCells.length}`
	);
	assert.ok(
		active.includes('text-right whitespace-nowrap') ||
			active.includes('whitespace-nowrap text-right')
	);
});
