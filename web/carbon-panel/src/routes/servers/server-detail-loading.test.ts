// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

const detail = fs.readFileSync(path.resolve(import.meta.dirname, './[id]/+page.svelte'), 'utf-8');

test('first server load shows the Carbon spinner, not a CSS border spinner', () => {
	assert.match(
		detail,
		/<CarbonSpinner\s+size="lg"\s+label="Loading server"\s+description="Connecting to server daemon\.\.\."/,
		'the first-load state must use the shared CarbonSpinner component'
	);
	assert.ok(
		!detail.includes('border-t-transparent'),
		'the ad-hoc three-sided border spinner must not return'
	);
	assert.match(
		detail,
		/motion-skeleton/,
		'first load should skeleton the upcoming runtime tile grid'
	);
});

test('engine specs render the Java row even when the daemon reports no Java version', () => {
	assert.match(
		detail,
		/<dt class="font-mono text-xs text-muted-foreground uppercase">Java<\/dt>/,
		'the engine specs list must always contain a Java row'
	);
	assert.doesNotMatch(
		detail,
		/\{#if server\.javaVersion\}\s*<dt/,
		'the Java row itself must not be conditional; only its value may branch'
	);
	assert.match(detail, /JDK \{server\.javaVersion\}/);
	assert.match(
		detail,
		/<span class="text-muted-foreground">—<\/span>/,
		'an unset Java version must render an em-dash placeholder, not shrink the card'
	);
});
