// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// Content tests for the dashboard tile retune and node inventory row.
// They assert on the shipped source (like CarbonTabs.test.ts does for layout)
// so a class regression fails without rendering the RPC-backed page.
const source = readFileSync(new URL('../+page.svelte', import.meta.url), 'utf8');

test('tiles use p-6 padding (retuned from p-5)', () => {
	assert.match(source, /bg-\[#262626\] p-6 transition-colors/);
	assert.doesNotMatch(source, /bg-\[#262626\] p-5 transition-colors/);
});

test('tile labels are sans semibold 12px uppercase #a8a8a8 tracking-wider', () => {
	assert.ok(
		source.includes('font-sans text-xs font-semibold tracking-wider text-[#a8a8a8] uppercase'),
		'Tile labels must use the Carbon retune classes'
	);
});

test('tile values are sans semibold 32px tabular-nums (mono numerals only)', () => {
	assert.match(source, /mt-3 font-sans text-\[32px\]/);
	assert.ok(source.includes('tabular-nums'), 'Values must use tabular-nums for numerals');
});

test('tile footers are 11px #8d8d8d with mt-3 pt-3 border-t', () => {
	assert.match(
		source,
		/mt-3 flex items-center justify-between[^"]*border-t border-\[#393939\] pt-3 font-sans text-\[11px\]/
	);
});

test('dashboard loads nodes in parallel and stays fail-open', () => {
	assert.ok(source.includes('listNodes'), 'Dashboard must call NodeService listNodes');
	assert.ok(source.includes('Promise.all'), 'Servers and nodes must load in parallel');
	assert.ok(source.includes('silentCallOptions'), 'Node fetch must stay silent so it never toasts');
	assert.ok(source.includes('fail-open'), 'A node failure must be documented as fail-open');
});

test('node inventory row shows per-node status, servers, and memory', () => {
	assert.ok(source.includes('dashboardNodes'), 'Dashboard must hold node inventory state');
	assert.ok(source.includes('data-testid="node-section"'), 'Node section must be labelled');
	assert.ok(source.includes('data-testid="node-row"'), 'Each node must render a row');
	assert.ok(source.includes('describeNodeStatusLabel'), 'Rows must label ONLINE/OFFLINE');
	assert.ok(source.includes('formatNodeMemory'), 'Rows must show allocated/total memory');
	assert.ok(source.includes('serverCount'), 'Rows must show per-node server counts');
});

test('system health strip extends to node counts', () => {
	assert.ok(source.includes('data-testid="health-nodes"'), 'Health strip needs a nodes cell');
	assert.ok(source.includes('nodeStats.online'), 'Health strip must count online nodes');
	assert.ok(source.includes('lg:grid-cols-4'), 'Health strip must fit the fourth cell');
});
