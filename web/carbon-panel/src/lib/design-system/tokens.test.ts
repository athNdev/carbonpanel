// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const css = readFileSync(new URL('./tokens.css', import.meta.url), 'utf8');

function theme(selector: string): Record<string, string> {
	const start = css.indexOf(selector);
	const end = css.indexOf('}', start);
	assert.ok(start >= 0 && end >= 0, `Missing theme ${selector}`);
	return Object.fromEntries(
		Array.from(css.slice(start, end).matchAll(/(--[\w-]+):\s*(#[\da-f]{6})\s*;/gi), (m) => [
			m[1],
			m[2]
		])
	);
}

function contrast(a: string, b: string): number {
	const luminance = (hex: string) => {
		assert.match(hex ?? '', /^#[\da-f]{6}$/i, 'Expected a hex color');
		const channels = [1, 3, 5].map((i) => {
			const c = parseInt(hex.slice(i, i + 2), 16) / 255;
			return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
		});
		return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
	};
	const values = [luminance(a), luminance(b)].sort((x, y) => y - x);
	return (values[0] + 0.05) / (values[1] + 0.05);
}

for (const selector of ["[data-carbon-theme='light']", "[data-carbon-theme='dark']"]) {
	test(`${selector} keeps key text, controls and status tags legible`, () => {
		const values = theme(selector);
		for (const surface of ['--background', '--card']) {
			for (const text of [
				'--foreground',
				'--muted-foreground',
				'--primary-text',
				'--destructive-text',
				'--success-text',
				'--warning-text'
			]) {
				assert.ok(contrast(values[text], values[surface]) >= 4.5, `${text} on ${surface}`);
			}
			for (const boundary of ['--input', '--ring']) {
				assert.ok(contrast(values[boundary], values[surface]) >= 3, `${boundary} on ${surface}`);
			}
		}
		for (const tag of [
			'blue',
			'green',
			'red',
			'yellow',
			'purple',
			'cyan',
			'teal',
			'magenta',
			'gray',
			'warm-gray',
			'cool-gray'
		]) {
			assert.ok(contrast(values[`--tag-${tag}-text`], values[`--tag-${tag}-bg`]) >= 4.5, tag);
		}
		for (const [foreground, background] of [
			['--primary-foreground', '--button-primary-hover'],
			['--primary-foreground', '--button-primary-active'],
			['--secondary-foreground', '--button-secondary-hover'],
			['--secondary-foreground', '--button-secondary-active'],
			['--foreground', '--accent'],
			['--foreground', '--button-secondary-active']
		]) {
			assert.ok(
				contrast(values[foreground], values[background]) >= 4.5,
				`${foreground} on ${background}`
			);
		}
		assert.ok(contrast(values['--primary-foreground'], values['--primary']) >= 4.5);
		assert.ok(contrast('#ffffff', values['--destructive']) >= 4.5);
	});
}

test('the dark default yields to the explicit light theme', () => {
	// The default must not outrank [data-carbon-theme='light']: equal
	// specificity would let source order decide, and the dark block comes
	// last, which once made light unreachable. `:where(:root)` carries zero
	// specificity so any explicit theme wins.
	assert.ok(
		css.includes(':where(:root)'),
		'dark default must use :where(:root) so light overrides it'
	);
	const light = theme("[data-carbon-theme='light']");
	const root = theme(':root');
	assert.notEqual(light['--card'], root['--card'], 'light and dark palettes must actually differ');
});

test(':root defaults to the dark palette so fresh browsers render dark controls', () => {
	const root = theme(':root');
	const dark = theme("[data-carbon-theme='dark']");
	for (const token of ['--background', '--card', '--foreground', '--input', '--border']) {
		assert.equal(
			root[token],
			dark[token],
			`:root ${token} must match the dark theme (unstyled selects/cards flash white otherwise)`
		);
	}
});
