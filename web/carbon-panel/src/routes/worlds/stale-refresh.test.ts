// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// The route source is the seam for this behavior: the store already retains
// last-good rows on a failed refresh, and the route must keep rendering them
// instead of replacing the table with the cold-load error state.
const source = readFileSync(new URL('./+page.svelte', import.meta.url), 'utf8');

test('a failed refresh keeps the last-good table visible', () => {
	assert.ok(
		source.includes('{:else if snapshot.error && snapshot.targets.length === 0}'),
		'the full error state must be reserved for a cold failure with no rows'
	);
	assert.ok(
		source.includes('showStaleBanner') && source.includes('snapshot.targets.length > 0'),
		'the stale banner must only appear when retained rows exist'
	);
});

test('the stale banner is dismissible per error and offers a retry', () => {
	assert.ok(source.includes('role="alert"'), 'the stale notice must be announced');
	assert.ok(
		source.includes('dismissedStaleError !== snapshot.error'),
		'a new error must show the banner again after a dismissal'
	);
	assert.ok(source.includes('onclick={() => (dismissedStaleError = snapshot.error)}'));
	assert.ok(source.includes('onclick={retryTargets}'), 'the banner and header must share retry');
});

test('the refresh retry path is single-sourced', () => {
	const handlers = source.match(/onclick=\{retryTargets\}/g) ?? [];
	assert.ok(handlers.length >= 2, 'header, cold error and stale banner must retry identically');
	assert.ok(
		source.includes('void store.refreshTargets().catch(handleListError)'),
		'retry must keep the permission-denied handling'
	);
});
