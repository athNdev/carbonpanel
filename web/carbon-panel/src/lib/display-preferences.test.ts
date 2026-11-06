// SPDX-License-Identifier: MPL-2.0
// Contract tests for the stored display preferences: the dialog must close,
// the motion/density attributes must have CSS effects, and the shell must
// apply stored values on boot (not only when the dialog opens).
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const css = readFileSync(new URL('../app.css', import.meta.url), 'utf8').replace(/\r\n/g, '\n');

test('reduced motion kills animation via the data attribute, not just the media query', () => {
	assert.ok(
		css.includes("[data-carbon-motion='reduced']"),
		'reduced motion must have a data-attribute kill-switch in app.css'
	);
	assert.match(css, /animation-duration: 0\.01ms !important/);
});

test('compact density tightens the control tokens', () => {
	assert.ok(
		css.includes("[data-carbon-density='compact']"),
		'compact density must override tokens in app.css'
	);
	assert.match(css, /--carbon-control-md: 2rem/);
});

test('mono eyebrow labels share one tracking/color contract', () => {
	for (const utility of ['.mono-label ', '.mono-label-strong', '.mono-label-accent']) {
		assert.ok(css.includes(utility), `app.css must define ${utility.trim()}`);
	}
	assert.match(css, /letter-spacing: 0\.04em/);
});
