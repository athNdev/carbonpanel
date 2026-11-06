// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { compile } from 'svelte/compiler';
import { render } from 'svelte/server';

// Render the real component, rather than duplicating its disabled-state logic.
const source = readFileSync(new URL('./CarbonButton.svelte', import.meta.url), 'utf8');
const compiled = compile(source, { generate: 'server' }).js.code.replace(
	"'svelte/internal/server'",
	JSON.stringify(import.meta.resolve('svelte/internal/server'))
);
const { default: Button } = await import(
	`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`
);

for (const state of ['disabled', 'loading'] as const) {
	test(`a ${state} link button cannot navigate or enter the tab order`, () => {
		const html = render(Button, {
			props: { href: '/destination', [state]: true, tabindex: 0, 'aria-disabled': 'false' }
		}).body;
		const anchor = html.match(/<a\b[^>]*>/)?.[0] ?? '';
		assert.ok(anchor, 'Expected a rendered link button');
		assert.doesNotMatch(anchor, /\shref=/);
		assert.match(anchor, /aria-disabled="true"/);
		assert.match(anchor, /role="link"/);
		assert.match(anchor, /tabindex="-1"/);
		assert.ok(
			anchor
				.match(/class="([^"]*)"/)?.[1]
				.split(/\s+/)
				.includes('opacity-50')
		);
	});
}

test('tertiary follows the Carbon spec: accent text, no border', () => {
	// Tertiary is the quiet action next to a primary button (Deploy/Export
	// beside Save). A full foreground border competes with the primary, so
	// per Carbon it renders accent text on transparent with a hover layer.
	const html = render(Button, { props: { kind: 'tertiary' } }).body;
	const button = html.match(/<button\b[^>]*>/)?.[0] ?? '';
	assert.ok(button.includes('text-accent-text'), 'tertiary text must be the accent');
	assert.ok(button.includes('border-transparent'), 'tertiary must not draw a border');
	assert.ok(!button.includes('border-foreground'), 'tertiary must not use a foreground border');
});

test('an enabled link button retains its destination', () => {
	const html = render(Button, { props: { href: '/destination' } }).body;
	assert.match(html, /\shref="\/destination"/);
});
