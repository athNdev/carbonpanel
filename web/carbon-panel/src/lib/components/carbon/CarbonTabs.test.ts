// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { compile } from 'svelte/compiler';
import { render } from 'svelte/server';

// Render the real component so the assertions describe the markup that ships.
const source = readFileSync(new URL('./CarbonTabs.svelte', import.meta.url), 'utf8');
const compiled = compile(source, { generate: 'server' }).js.code.replace(
	"'svelte/internal/server'",
	JSON.stringify(import.meta.resolve('svelte/internal/server'))
);
const { default: Tabs } = await import(
	`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`
);

const tabs = Array.from({ length: 12 }, (_, i) => ({ id: `tab-${i}`, label: `Section ${i}` }));

function classListOf(html: string, tag: string): string[][] {
	const match = html.match(new RegExp(`<${tag}\\b[^>]*class="([^"]*)"`, 'g')) ?? [];
	return match.map((element) => element.match(/class="([^"]*)"/)![1].split(/\s+/));
}

test('the tab strip never grows a scrollbar', () => {
	const html = render(Tabs, { props: { tabs, selectedTab: 'tab-0' } }).body;
	const strip = classListOf(html, 'div').find((classes: string[]) =>
		classes.includes('scroll-strip')
	);

	assert.ok(strip, 'The tab list must use the scroll-strip utility');
	// scroll-strip is the single place that defines how a strip scrolls, so the
	// bar cannot reappear because a caller passed a different overflow class.
	assert.ok(!strip.includes('overflow-x-auto'), 'The strip must not set overflow-x itself');
});

test('tabs do not shrink or wrap, so the bar height cannot change', () => {
	const html = render(Tabs, { props: { tabs, selectedTab: 'tab-0' } }).body;
	const buttons = classListOf(html, 'button');

	assert.equal(buttons.length, tabs.length);
	for (const classes of buttons) {
		assert.ok(classes.includes('shrink-0'), 'A tab must not shrink below its label');
		assert.ok(classes.includes('whitespace-nowrap'), 'A tab label must not wrap');
	}
});

test('the selection indicator stays inside the strip', () => {
	// The indicator only exists after layout, so it is asserted on the component
	// source. A negative offset would be clipped by the strip's hidden vertical
	// overflow, which is why it sits on the strip's inner bottom edge instead.
	assert.match(source, /bottom-0\b/);
	assert.doesNotMatch(source, /bottom-\[-1px\]/);
});
