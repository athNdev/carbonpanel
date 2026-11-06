// SPDX-License-Identifier: MPL-2.0
// Contract tests for the file tree: column alignment, selection stability,
// and a Carbon-square context menu.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const tree = readFileSync(new URL('./file-tree.svelte', import.meta.url), 'utf8');
const row = readFileSync(new URL('./file-tree-row.svelte', import.meta.url), 'utf8');
const menu = readFileSync(new URL('./file-context-menu.svelte', import.meta.url), 'utf8');

test('header columns match the row columns exactly', () => {
	// The header comment claims an exact match; widths drifted before (w-16
	// vs w-20), which skews every column in the tree.
	for (const width of ['w-20', 'w-24']) {
		assert.ok(tree.includes(width), `tree header and rows must share ${width}`);
	}
	assert.match(tree, /Size<\/span>/);
	assert.ok(tree.includes('Modified'), 'header must label the modified column');
});

test('selection never shifts the row (border space is reserved)', () => {
	assert.ok(
		row.includes('border-l-transparent'),
		'rows must reserve the accent-border space when unselected'
	);
	assert.ok(row.includes('border-l-[#0f62fe]'), 'selection must use the accent border');
});

test('context menu is Carbon-square, not rounded shadcn', () => {
	assert.ok(menu.includes('rounded-none'), 'menu and items must be square');
	assert.doesNotMatch(menu, /rounded-md/, 'menu must not use the rounded popover shell');
	assert.doesNotMatch(menu, /rounded-sm/, 'menu items must not be rounded');
	assert.ok(menu.includes('bg-[#161616]'), 'menu must sit on the shell surface');
});
