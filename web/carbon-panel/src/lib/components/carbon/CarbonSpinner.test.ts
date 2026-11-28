// SPDX-License-Identifier: MPL-2.0
//
// Contract tests for CarbonSpinner and its adoption at the former
// "three-sided square" border-spinner call sites. The arc must draw with the
// inherited text color (a hardcoded Carbon blue vanishes on primary buttons),
// and the border-spinner markup must stay gone from the shared primitives.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { compile } from 'svelte/compiler';
import { render } from 'svelte/server';

const source = readFileSync(new URL('./CarbonSpinner.svelte', import.meta.url), 'utf8');

function normalize(text: string): string {
	return text.replace(/\s+/g, ' ');
}

function styleBlock(text: string): string {
	const match = text.match(/<style>([\s\S]*)<\/style>/);
	assert.ok(match, 'CarbonSpinner carries a scoped style block');
	return match[1];
}

/** Render the real component through Svelte's server build. */
async function renderSpinner(props: Record<string, unknown> = {}): Promise<string> {
	const compiled = compile(source, { generate: 'server' }).js.code.replace(
		"'svelte/internal/server'",
		JSON.stringify(import.meta.resolve('svelte/internal/server'))
	);
	const { default: Spinner } = await import(
		`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`
	);
	return render(Spinner, { props }).body;
}

test('the arc draws with the inherited text color, never a hardcoded blue', async () => {
	const style = styleBlock(source);
	assert.match(style, /\.carbon-spinner\s*\{[^}]*color:\s*inherit/);
	assert.doesNotMatch(style, /var\(--primary/);
	assert.doesNotMatch(style, /#0f62fe/i);
	const html = await renderSpinner({});
	assert.match(
		html,
		/stroke="currentColor"/,
		'strokes follow currentColor so call sites choose the tone'
	);
});

test('sizes map to the 16/24/40 Carbon scale and the root is a status region', async () => {
	const sm = await renderSpinner({ size: 'sm' });
	assert.match(sm, /role="status"/);
	assert.match(sm, /<svg[^>]*width="16" height="16"/);
	const md = await renderSpinner({});
	assert.match(md, /<svg[^>]*width="24" height="24"/);
	const lg = await renderSpinner({ size: 'lg' });
	assert.match(lg, /<svg[^>]*width="40" height="40"/);
});

test('label and description flow into the accessible and visible text', async () => {
	const withDescription = await renderSpinner({
		label: 'Saving changes',
		description: 'Connecting to server daemon...'
	});
	// Server render stamps the scoped class onto every element, title included.
	assert.match(withDescription, /<title[^>]*>Saving changes<\/title>/);
	assert.match(withDescription, /Connecting to server daemon\.\.\./);

	const bare = await renderSpinner({});
	assert.match(bare, /<title[^>]*>Loading<\/title>/, 'label defaults to a generic Loading');
	assert.doesNotMatch(bare, /font-mono text-xs/, 'no description text without the prop');
});

test('reduced motion collapses the sweep to a static pulsing glyph', () => {
	const style = styleBlock(source);
	assert.match(style, /@media \(prefers-reduced-motion: reduce\)/);
	assert.match(style, /carbon-spinner-pulse/);
});

test('CarbonButton loading states render the arc, not a border spinner', () => {
	const button = normalize(readFileSync(new URL('./CarbonButton.svelte', import.meta.url), 'utf8'));
	const spinnerUses = button.match(/<CarbonSpinner size="sm" \/>/g) ?? [];
	assert.equal(spinnerUses.length, 2, 'both kind branches use the shared spinner');
	assert.ok(
		button.includes('aria-hidden="true"'),
		'spinner stays presentational: aria-busy speaks'
	);
	assert.ok(button.includes('shrink-0'), 'spinner keeps the shrink-0 layout contract');
	assert.doesNotMatch(button, /border-t-transparent/, 'the three-sided square is gone');
	assert.doesNotMatch(button, /animate-spin/, 'rotation is owned by CarbonSpinner');
});

test('CarbonInlineLoading active state keeps the Carbon blue via text-primary', () => {
	const inline = normalize(
		readFileSync(new URL('./CarbonInlineLoading.svelte', import.meta.url), 'utf8')
	);
	assert.match(inline, /<CarbonSpinner size="sm" label="Loading" \/>/);
	assert.match(inline, /<span class="text-primary">/);
	assert.doesNotMatch(inline, /border-t-transparent/, 'the three-sided square is gone');
	assert.match(inline, /carbon-check-draw/, 'finished state keeps its draw animation');
	assert.match(inline, /motion-fade-in/, 'error state keeps its entrance');
});

test('the boot screen mirrors the shell chrome and keeps status semantics', () => {
	const layout = normalize(
		readFileSync(new URL('../../../routes/+layout.svelte', import.meta.url), 'utf8')
	);
	assert.match(layout, /<CarbonSpinner\s+size="lg"/);
	assert.match(layout, /label="Loading Carbon Panel"/);
	assert.match(layout, /role="status"/);
	assert.match(layout, /sr-only[^>]*> ?Loading Carbon Panel…/);
	assert.match(
		layout,
		/h-12 shrink-0 border-b border-border/,
		'placeholder bar mirrors the 48px shell header'
	);
	assert.match(layout, /min-h-dvh/, 'boot screen uses the same dynamic viewport as CarbonShell');
	assert.doesNotMatch(layout, /border-t-transparent/, 'the three-sided square is gone');
	assert.doesNotMatch(layout, /animate-spin border/, 'no border spinner remains at boot');
});

test('page-level fade-only motion variants exist for route roots and grids', () => {
	const css = normalize(
		readFileSync(new URL('../../../app.css', import.meta.url), 'utf8').replace(/\r\n/g, '\n')
	);
	assert.match(css, /\.motion-fade-in-only\s*\{[^}]*--carbon-animation-fade-in/);
	assert.match(css, /\.motion-stagger-fade > \* \{ animation: var\(--carbon-animation-fade-in\)/);
	// The stagger cap must exist for both variants so late children never lag.
	assert.match(css, /\.motion-stagger-fade > \*:nth-child\(n \+ 8\)/);
	assert.match(css, /\.motion-stagger > \*:nth-child\(n \+ 8\)/);
	// Inner surfaces keep the rise; only the fade-only variants dropped it.
	assert.match(css, /\.motion-stagger > \* \{ animation: var\(--carbon-animation-rise-in\)/);
	assert.match(css, /\.motion-rise-in \{ animation: var\(--carbon-animation-rise-in\)/);
});

test('integration: a loading CarbonButton mounts the arc spinner in a real DOM', async () => {
	// Registers happy-dom globals and the .svelte loader plugin; module caching
	// makes a second import (a11y-dom.test.ts also uses it) a no-op.
	await import('../../../../test/dom-setup.mjs');
	const { mount, unmount } = await import('svelte');
	const { default: CarbonButton } = await import('./CarbonButton.svelte');

	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(CarbonButton, { target, props: { loading: true } });
	try {
		await new Promise((resolve) => setTimeout(resolve, 40));
		assert.ok(target.querySelector('svg.carbon-spinner-arc'), 'the arc spinner is mounted');
		assert.equal(target.querySelector('.animate-spin'), null, 'no CSS border spinner remains');
		const button = target.querySelector('button');
		assert.equal(button?.getAttribute('aria-busy'), 'true');
	} finally {
		unmount(instance);
		target.remove();
	}
});
