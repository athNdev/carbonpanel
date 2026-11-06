// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// Content tests for the changelog page: version headers, grouped lists,
// sticky TOC, and the resynced lib copy.
const source = readFileSync(new URL('./+page.svelte', import.meta.url), 'utf8');
const libCopy = readFileSync(new URL('../../lib/changelog.md', import.meta.url), 'utf8');
const root = readFileSync(new URL('../../../../../CHANGELOG.md', import.meta.url), 'utf8');

test('lib changelog copy is resynced from the root CHANGELOG', () => {
	assert.equal(libCopy, root);
});

test('version headers render as CarbonTag plus h2', () => {
	assert.ok(source.includes('CarbonTag'), 'Versions must use the CarbonTag component');
	assert.ok(
		source.includes('changelog-version-title'),
		'Version titles must use the version heading style'
	);
	assert.match(source, /<h2 id=\{version\.id\}/);
});

test('added and fixed groups render with icons', () => {
	assert.ok(source.includes('Plus'), 'Added groups need an icon');
	assert.ok(source.includes('Check'), 'Fixed groups need an icon');
	assert.match(source, /group\.kind\.toLowerCase\(\) === 'added'/);
	assert.match(source, /group\.kind\.toLowerCase\(\) === 'fixed'/);
});

test('version TOC is sticky with a 220px rail on large screens', () => {
	assert.ok(source.includes('lg:grid-cols-[220px_1fr]'), 'Layout must reserve a 220px TOC rail');
	assert.ok(source.includes('lg:sticky'), 'TOC must stick on large screens');
	assert.ok(source.includes('aria-label="Versions"'), 'TOC nav must be labelled');
});

test('changelog type scale uses 1.5rem h2 and 0.5rem list margins', () => {
	assert.match(source, /font-size: 1\.5rem/);
	assert.doesNotMatch(source, /font-size: 1\.25rem/);
	assert.match(source, /margin: 0\.5rem 0/);
});

test('version headings carry scroll margin for anchor links', () => {
	assert.ok(source.includes('scroll-margin-top'), 'Anchored h2s must not hide under headers');
});
