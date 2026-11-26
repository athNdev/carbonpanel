// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL-2.0 License was distributed with this
// file, You can obtain one at https://mozilla.org/MPL-2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { allowlistDialogShouldClose } from './allowlist-dialog-state';

test('allowlist dialog survives repeated telemetry polls of the same server', () => {
	// The server detail inventory republishes a new Server object every 5s.
	// Only the identity is stable; a fresh object must not close the dialog.
	let previous: string | null = null;
	for (let poll = 0; poll < 40; poll += 1) {
		const close = allowlistDialogShouldClose(previous, 'srv-review-vanilla', true);
		assert.equal(close, false, `poll ${poll} must not close the dialog`);
		previous = 'srv-review-vanilla';
	}
});

test('allowlist dialog closes on a real server switch or inactive tab', () => {
	assert.equal(allowlistDialogShouldClose('srv-a', 'srv-b', true), true);
	assert.equal(allowlistDialogShouldClose('srv-a', 'srv-a', false), true);
	assert.equal(allowlistDialogShouldClose('srv-a', 'srv-b', false), true);
});

test('initial observation never closes an already open dialog', () => {
	assert.equal(allowlistDialogShouldClose(null, 'srv-a', true), false);
	assert.equal(allowlistDialogShouldClose(null, 'srv-a', false), false);
	assert.equal(allowlistDialogShouldClose(null, '', true), false);
});

test('ServerPlayers uses the identity-aware lifetime helper', () => {
	const componentPath = path.resolve(import.meta.dirname, '../../components/server-players.svelte');
	const content = fs.readFileSync(componentPath, 'utf-8');
	assert.ok(
		content.includes(
			"import { allowlistDialogShouldClose } from '$lib/features/players/allowlist-dialog-state';"
		),
		'ServerPlayers must use the shared dialog lifetime helper'
	);
	assert.ok(
		content.includes('allowlistDialogShouldClose(previousAllowlistServerId, server.id, active)'),
		'the dialog must close only on identity/tab changes, not on every poll'
	);
	assert.ok(
		!content.includes('void server.id;'),
		'the old effect that closed the dialog on every server republish must be gone'
	);
});
