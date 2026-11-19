// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// The world detail route separates read access (world_view/read) from action
// scopes (update, observe_players). Action failures must stay local so a user
// who can still read the map is not sent to the page-level access-denied state.
const source = readFileSync(new URL('./+page.svelte', import.meta.url), 'utf8');

test('player visibility changes do not revoke the read view on denial', () => {
	const start = source.indexOf('async function togglePlayers');
	assert.ok(start >= 0, 'togglePlayers handler missing');
	const block = source.slice(start, source.indexOf('\n\t}\n', start) + 3);
	assert.ok(
		block.includes("toast.error(formatRpcError(error, 'Could not change player visibility.'))"),
		'visibility failures must surface as an action error'
	);
	assert.ok(
		!block.includes('handleError(error)'),
		'observe_players denial must not trigger page-level denied state'
	);
});

test('overlay edits do not revoke the read view on denial', () => {
	const start = source.indexOf('async function saveOverlay');
	assert.ok(start >= 0, 'saveOverlay handler missing');
	const block = source.slice(start, source.indexOf('\n\t}\n', start) + 3);
	assert.ok(
		block.includes("toast.error(formatRpcError(error, 'Could not save the overlay.'))"),
		'overlay save failures must surface as an action error'
	);
	assert.ok(
		!block.includes('handleError(error)'),
		'world_view/update denial must not trigger page-level denied state'
	);
});

test('read-scope loaders still escalate permission failures to view access handling', () => {
	assert.match(
		source,
		/async function loadSession[\s\S]*?handleError\(error\)/,
		'session/read failures must be handled as view access failures'
	);
	assert.match(
		source,
		/async function loadCapabilities[\s\S]*?handleError\(error\)/,
		'capability/read failures must be handled as view access failures'
	);
	assert.match(
		source,
		/async function loadOverlays[\s\S]*?handleError\(error\)/,
		'overlay list/read failures must be handled as view access failures'
	);
});
