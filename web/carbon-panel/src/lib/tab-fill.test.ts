// SPDX-License-Identifier: MPL-2.0
//
// Contract tests for the tab-fill policy in app.css. Tab panes with their own
// scroll regions (console log, mods list) must grow with the viewport instead
// of stopping at a fixed pixel cap, which left a dead void below them on tall
// screens. A reintroduced cap is invisible in review, so it is asserted here.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(here, '../app.css'), 'utf8').replace(/\r\n/g, '\n');
const consoleSource = readFileSync(join(here, 'components/server-console.svelte'), 'utf8');
const modsSource = readFileSync(join(here, 'components/server-mods.svelte'), 'utf8');

test('tab-fill tracks the viewport with a vh fallback', () => {
	const pattern = /(^|\n)[ \t]*\.tab-fill[ \t]*\{([^}]*)\}/gm;
	const found = [...css.matchAll(pattern)].map((match) => match[2].replace(/\s+/g, ' ').trim());
	assert.ok(found.length > 0, 'Missing rule for .tab-fill');
	const rule = found.join(' ');
	// vh first so browsers without dvh keep the fallback; dvh wins where supported.
	assert.match(rule, /height: calc\(100vh - /);
	assert.match(rule, /height: calc\(100dvh - /);
});

test('the console and mods panes fill instead of capping at a fixed height', () => {
	for (const [name, source] of [
		['server-console', consoleSource],
		['server-mods', modsSource]
	] as const) {
		assert.ok(source.includes('tab-fill'), `${name} no longer fills the viewport`);
		assert.ok(!source.includes('max-h-[800px]'), `${name} reintroduced the 800px cap`);
	}
});
