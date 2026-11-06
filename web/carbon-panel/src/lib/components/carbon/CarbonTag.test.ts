// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { compile } from 'svelte/compiler';
import { render } from 'svelte/server';

const source = readFileSync(new URL('./CarbonTag.svelte', import.meta.url), 'utf8');

test('CarbonTag never wraps its label', () => {
	assert.ok(
		source.includes('whitespace-nowrap'),
		'CarbonTag must include whitespace-nowrap so table tags cannot wrap'
	);
});

test('CarbonTag does not shrink inside flex rows', () => {
	assert.ok(
		source.includes('shrink-0'),
		'CarbonTag must include shrink-0 so flex layouts do not squeeze the tag'
	);
});

test('rendered tag carries the nowrap contract', async () => {
	const compiled = compile(source, { generate: 'server' }).js.code.replace(
		"'svelte/internal/server'",
		JSON.stringify(import.meta.resolve('svelte/internal/server'))
	);
	const { default: Tag } = await import(
		`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`
	);
	const html = render(Tag, { props: { type: 'gray', size: 'sm' } }).body;
	assert.match(html, /whitespace-nowrap/);
	assert.match(html, /shrink-0/);
});
