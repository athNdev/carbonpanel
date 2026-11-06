// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

const dialog = fs.readFileSync(
	path.resolve(import.meta.dirname, '../../lib/components/mod-browser-dialog.svelte'),
	'utf-8'
);
const list = fs.readFileSync(path.resolve(import.meta.dirname, './+page.svelte'), 'utf-8');
const detail = fs.readFileSync(path.resolve(import.meta.dirname, './[id]/+page.svelte'), 'utf-8');

test('mod browser filters and labels by the game loader, not the platform', () => {
	assert.ok(dialog.includes('gameLoader'), 'dialog must resolve an effective game loader');
	assert.ok(
		!dialog.includes('loaderSearchName(server.modLoader)') &&
			!dialog.includes('loaderDisplayName(server.modLoader)'),
		'dialog must not present the raw platform loader'
	);
});

test('engine specs and list show the game loader underneath a pack', () => {
	assert.ok(
		detail.includes('effectiveLoader(server.modLoader, server.modpackLoader)'),
		'engine specs must resolve the display loader'
	);
	assert.ok(
		list.includes('getModLoaderDisplay(server)'),
		'list must pass the server so the platform can resolve'
	);
});
