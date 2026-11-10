// SPDX-License-Identifier: MPL-2.0
//
// OSS-226: login, signup, route-change layout, and sidebar collapse must not
// cause a horizontal content jump. Route motion stays short and is disabled
// under reduced-motion (prefers-reduced-motion and the stored data attribute).
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const css = readFileSync(new URL('../app.css', import.meta.url), 'utf8').replace(/\r\n/g, '\n');
const tokens = readFileSync(new URL('./design-system/tokens.css', import.meta.url), 'utf8');
const layout = readFileSync(new URL('../routes/+layout.svelte', import.meta.url), 'utf8');
const login = readFileSync(new URL('../routes/login/+page.svelte', import.meta.url), 'utf8');
const motion = readFileSync(new URL('./motion.ts', import.meta.url), 'utf8');
const modal = readFileSync(
	new URL('./components/carbon/CarbonModal.svelte', import.meta.url),
	'utf8'
);
const shell = readFileSync(
	new URL('./components/carbon/CarbonShell.svelte', import.meta.url),
	'utf8'
);

function ruleBody(source: string, selector: string): string {
	const pattern = new RegExp(`(^|\\n)[ \\t]*${selector}[ \\t]*\\{([^}]*)\\}`, 'gm');
	const found = [...source.matchAll(pattern)].map((match) => match[2].replace(/\s+/g, ' ').trim());
	assert.ok(found.length > 0, `Missing rule for ${selector}`);
	return found.join(' ');
}

test('login and signup share one page that cannot opt out of the document gutter', () => {
	assert.match(login, /mode = \$state<'login' \| 'register'>/);
	assert.match(login, /id: 'register'/);
	assert.match(login, /min-h-screen/);
	assert.doesNotMatch(
		login,
		/overflow-x-(auto|scroll)/,
		'Login/signup must not grow a page-level horizontal scroller'
	);
	assert.doesNotMatch(
		login,
		/scrollbar-gutter:\s*(auto|unset|initial)/,
		'Login/signup must inherit html { scrollbar-gutter: stable }'
	);
});

test('route-change layout still loads app.css and keeps login off the shell', () => {
	assert.match(layout, /import '\.\.\/app\.css'/);
	assert.match(layout, /pathname === '\/login'/);
	assert.match(layout, /<CarbonShell>/);
	assert.match(
		css,
		/(^|\n)[ \t]*html[ \t]*\{[^}]*scrollbar-gutter: stable/,
		'Document gutter must stay reserved across login and in-app routes'
	);
});

test('sidebar collapse reserves its gutter and only moves the main margin', () => {
	assert.match(shell, /overflow-x-hidden overflow-y-auto/);
	assert.match(shell, /scrollbar-stable/);
	assert.match(shell, /overflow-y-auto p-4/);
	assert.match(shell, /md:ml-64/);
	assert.match(shell, /md:ml-12/);
	assert.match(shell, /transition-\[width,transform\]/);
	assert.match(shell, /transition-\[margin\]/);
	assert.match(shell, /duration-\(--motion-fast\)/);
	assert.doesNotMatch(
		shell,
		/<main[\s\S]*overflow-x-(auto|scroll)/,
		'Main content must not introduce a horizontal scrollbar on collapse'
	);
});

test('route view-transitions are the short page-motion duration', () => {
	assert.match(tokens, /--motion-page:\s*140ms/);
	const oldRule = ruleBody(css, '::view-transition-old\\(root\\)');
	const newRule = ruleBody(css, '::view-transition-new\\(root\\)');
	assert.match(oldRule, /var\(--motion-page\)/);
	assert.match(newRule, /var\(--motion-page\)/);
	assert.doesNotMatch(oldRule, /\d{3,}ms/, 'Route exit must not use a long hardcoded duration');
	assert.doesNotMatch(newRule, /\d{3,}ms/, 'Route enter must not use a long hardcoded duration');
});

test('prefers-reduced-motion disables view-transitions and other motion', () => {
	assert.match(
		css,
		/@media \(prefers-reduced-motion: reduce\) \{[\s\S]*::view-transition-old\(root\),\s*::view-transition-new\(root\) \{\s*animation: none;/
	);
	assert.match(
		css,
		/@media \(prefers-reduced-motion: reduce\) \{[\s\S]*animation-duration: 0\.01ms !important/
	);
	assert.match(motion, /matchMedia\('\(prefers-reduced-motion: reduce\)'\)\.matches/);
	assert.match(
		layout,
		/prefersReducedMotion\(\)/,
		'Layout must skip startViewTransition when the OS asks for reduced motion'
	);
});

test('stored reduced motion also disables view-transitions', () => {
	assert.match(
		css,
		/\[data-carbon-motion='reduced'\]::view-transition-old\(root\),\s*\[data-carbon-motion='reduced'\]::view-transition-new\(root\) \{\s*animation: none;/
	);
	assert.match(motion, /getAttribute\('data-carbon-motion'\) === 'reduced'/);
	assert.match(
		layout,
		/prefersReducedMotion\(\)/,
		'Layout must skip startViewTransition when the stored preference is reduced'
	);
});

test('route view-transitions are interruptible', () => {
	assert.match(layout, /skipTransition/);
	assert.match(layout, /activeViewTransition/);
});

test('Svelte JS transitions honor reduced motion instead of a fixed duration', () => {
	assert.match(shell, /motionDuration\(150\)/);
	assert.match(modal, /motionDuration\(150\)/);
	assert.match(modal, /motionDuration\(200\)/);
});

test('tab hover motion only animates color, not layout', () => {
	const tab = ruleBody(css, '\\.cds-tab-item');
	assert.match(tab, /var\(--carbon-transition-color\)/);
	assert.doesNotMatch(tab, /transition:\s*all/);
});

// Cloud visual verification of the same login, signup, route-change, and
// sidebar interaction states lives in the Cloud console snapshot, which is
// out of this repository. Do not treat that gap as missing OSS work.
// test.todo('Cloud: login, signup, route change, and sidebar collapse match OSS gutters');
