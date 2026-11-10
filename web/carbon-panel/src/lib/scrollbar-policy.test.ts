// SPDX-License-Identifier: MPL-2.0
//
// Contract tests for the scrollbar policy in app.css. The rules have to agree
// across the whole app, and a single removed line is invisible in review but
// brings back the overflow flicker, so they are asserted here.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const css = readFileSync(new URL('../app.css', import.meta.url), 'utf8').replace(/\r\n/g, '\n');

/** Every declaration block for a selector, whitespace-normalised. */
function rules(selector: string): string[] {
	const pattern = new RegExp(`(^|\\n)[ \\t]*${selector}[ \\t]*\\{([^}]*)\\}`, 'gm');
	return [...css.matchAll(pattern)].map((match) => match[2].replace(/\s+/g, ' ').trim());
}

function requireRule(selector: string): string {
	const found = rules(selector);
	assert.ok(found.length > 0, `Missing rule for ${selector}`);
	return found.join(' ');
}

test('scrollbars are thin and theme-coloured everywhere', () => {
	// A classic 17px scrollbar is what makes an overflow change reflow the page.
	const universal = requireRule('\\*');
	assert.match(universal, /scrollbar-width: thin/);
	assert.match(universal, /scrollbar-color:/);
});

test('a vertical scroll container reserves its gutter', () => {
	assert.match(requireRule('html'), /scrollbar-gutter: stable/);

	// Utility classes cannot carry per-instance state, so the reservation is
	// applied wherever Tailwind's overflow utilities are used.
	assert.match(
		css,
		/:where\(\.overflow-y-auto,\s*\.overflow-y-scroll,\s*\.overflow-auto\)\s*\{[^}]*scrollbar-gutter: stable/
	);
});

test('a strip can scroll without ever showing a scrollbar', () => {
	const strip = requireRule('\\.scroll-strip');
	assert.match(strip, /overflow-x: auto/);
	assert.match(strip, /overflow-y: hidden/);
	assert.match(strip, /scrollbar-width: none/);
	assert.match(css, /\.scroll-strip::-webkit-scrollbar\s*\{[^}]*display: none/);
});

test('nested horizontal panes hide the bar so sidebar collapse cannot change their height', () => {
	assert.match(
		css,
		/:where\(\.overflow-x-auto,\s*\.overflow-x-scroll\):not\(\.overflow-y-auto\):not\(\.overflow-y-scroll\):not\(\s*\.overflow-auto\s*\)\s*\{[^}]*scrollbar-width: none/
	);
	assert.doesNotMatch(
		css,
		/:where\(\.overflow-x-auto,\s*\.overflow-x-scroll\)\s*\{[^}]*scrollbar-width: none/,
		'Dual-axis panes must keep a visible vertical scrollbar'
	);
});

test('an opt-out utility hides the bar but keeps the surface scrollable', () => {
	const none = requireRule('\\.scrollbar-none');
	assert.match(none, /scrollbar-width: none/);
	assert.match(css, /\.scrollbar-none::-webkit-scrollbar\s*\{[^}]*display: none/);
});

test('the WebKit fallback matches the standard scrollbar look', () => {
	// Without this, browsers that ignore scrollbar-width/scrollbar-color fall
	// back to a platform scrollbar and the flicker returns there.
	assert.match(css, /::-webkit-scrollbar\s*\{[^}]*width:\s*10px/);
	assert.match(css, /::-webkit-scrollbar-thumb\s*\{[^}]*background-color/);
});
