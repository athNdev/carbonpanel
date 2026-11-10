// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

const SHADCN_FORM_IMPORTS = [
	'$lib/components/ui/dialog',
	'$lib/components/ui/input',
	'$lib/components/ui/button',
	'$lib/components/ui/card',
	'$lib/components/ui/badge',
	'$lib/components/ui/switch',
	'$lib/components/ui/accordion',
	'$lib/components/ui/alert',
	'$lib/components/ui/label',
	'$lib/components/ui/textarea'
];

const MIGRATED_FILES = [
	'node-settings.svelte',
	'auth-settings.svelte',
	'logs-settings.svelte',
	'api-keys-settings.svelte',
	'routing-settings.svelte'
];

const componentsDir = path.resolve(import.meta.dirname);

for (const file of MIGRATED_FILES) {
	test(`${file} does not import shadcn dialog/form primitives`, () => {
		const content = fs.readFileSync(path.join(componentsDir, file), 'utf-8');
		for (const imp of SHADCN_FORM_IMPORTS) {
			assert.ok(
				!content.includes(imp),
				`${file} must not import ${imp} after Carbon migration (OSS-314)`
			);
		}
	});
}

test('servers/new modpack dialog does not import shadcn Dialog', () => {
	const pagePath = path.resolve(import.meta.dirname, '../../routes/servers/new/+page.svelte');
	const content = fs.readFileSync(pagePath, 'utf-8');
	assert.ok(
		!content.includes('$lib/components/ui/dialog'),
		'servers/new +page must not import shadcn dialog for modpack picker'
	);
	assert.ok(
		content.includes('CarbonModal'),
		'servers/new +page modpack picker should use CarbonModal'
	);
});

test('numeric settings fields bind text so partial input is preserved', () => {
	const auth = fs.readFileSync(path.join(componentsDir, 'auth-settings.svelte'), 'utf-8');
	const nodes = fs.readFileSync(path.join(componentsDir, 'node-settings.svelte'), 'utf-8');
	const routing = fs.readFileSync(path.join(componentsDir, 'routing-settings.svelte'), 'utf-8');
	assert.match(auth, /bind:value=\{sessionTimeoutText\}/);
	assert.match(nodes, /bind:value=\{newNodeMemoryText\}/);
	assert.match(nodes, /bind:value=\{editMemoryText\}/);
	assert.match(routing, /bind:value=\{newListenerPort\}/);
	assert.doesNotMatch(auth, /value=\{String\(sessionTimeoutHours\)\}/);
});
