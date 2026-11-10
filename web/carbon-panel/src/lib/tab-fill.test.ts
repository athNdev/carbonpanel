// SPDX-License-Identifier: MPL-2.0
//
// Contract tests for the tab-fill policy. Server-detail working panes must
// grow with the remaining flex column instead of a viewport-minus-chrome
// calc, which left a dead void below them whenever the chrome was shorter
// than the constant. A reintroduced cap is invisible in review, so it is
// asserted here. Every tab also uses one ServerTabPanel header/container.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(here, '../app.css'), 'utf8').replace(/\r\n/g, '\n');
const shell = readFileSync(join(here, 'components/carbon/CarbonShell.svelte'), 'utf8');
const serverPage = readFileSync(join(here, '../routes/servers/[id]/+page.svelte'), 'utf8');
const panel = readFileSync(join(here, 'components/server/ServerTabPanel.svelte'), 'utf8');

function source(rel: string): string {
	return readFileSync(join(here, rel), 'utf8');
}

function cssRule(selector: string): string {
	const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
	const pattern = new RegExp(`(^|\\n)[ \\t]*${escaped}[ \\t]*\\{([^}]*)\\}`, 'gm');
	const found = [...css.matchAll(pattern)].map((match) => match[2].replace(/\s+/g, ' ').trim());
	assert.ok(found.length > 0, `Missing rule for ${selector}`);
	return found.join(' ');
}

test('tab-fill is a flex fill, not a viewport-minus-chrome height', () => {
	const rule = cssRule('.tab-fill');
	assert.match(rule, /flex:\s*1 1 0%/);
	assert.match(rule, /min-height:\s*0/);
	assert.match(rule, /height:\s*100%/);
	assert.doesNotMatch(css, /calc\(100vh\s*-/);
	assert.doesNotMatch(css, /calc\(100dvh\s*-/);
});

test('server-tab-header is one shared height, type, padding and divider', () => {
	const header = cssRule('.server-tab-header');
	assert.match(header, /min-height:\s*4\.5rem/);
	assert.match(header, /padding:\s*0\.75rem 1\.25rem/);
	assert.match(header, /border-bottom:\s*1px solid #393939/);
	const title = cssRule('.server-tab-title');
	assert.match(title, /font-size:\s*1\.125rem/);
	assert.match(title, /font-weight:\s*300/);
	assert.match(panel, /class="server-tab-header"/);
	assert.match(panel, /class="server-tab-body server-tab-body--\{mode\}"/);
});

test('the shell bounds height so tab panes can fill the remaining column', () => {
	assert.match(shell, /h-dvh/);
	assert.match(shell, /flex min-h-0 flex-1 flex-col overflow-y-auto p-4/);
	assert.match(shell, /min-h-full w-full max-w-7xl flex-1 flex-col/);
});

test('the server detail page passes min-h-0 through the tab panel', () => {
	assert.match(serverPage, /flex min-h-0 flex-1 flex-col gap-6/);
	assert.match(serverPage, /flex min-h-0 flex-1 flex-col overflow-hidden/);
});

test('every server-detail tab uses ServerTabPanel', () => {
	const panes = [
		['overview', serverPage],
		['activity', serverPage],
		['server-console', source('components/server-console.svelte')],
		['server-configuration', source('components/server-configuration.svelte')],
		['server-mods', source('components/server-mods.svelte')],
		['server-datapacks', source('components/server-datapacks.svelte')],
		['server-files', source('components/files/server-files.svelte')],
		['server-modules', source('components/server/ServerModules.svelte')],
		['server-tasks', source('components/server-tasks.svelte')],
		['server-backups', source('components/server-backups.svelte')],
		['server-players', source('components/server-players.svelte')],
		['server-routing', source('components/server-routing.svelte')]
	] as const;
	for (const [name, src] of panes) {
		assert.ok(src.includes('ServerTabPanel'), `${name} is not using ServerTabPanel`);
	}
});

test('working panes fill instead of capping at a fixed height', () => {
	const panes = [
		['server-console', source('components/server-console.svelte')],
		['server-mods', source('components/server-mods.svelte')],
		['server-datapacks', source('components/server-datapacks.svelte')],
		['server-files', source('components/files/server-files.svelte')],
		['server-modules', source('components/server/ServerModules.svelte')],
		['server-tasks', source('components/server-tasks.svelte')],
		['server-backups', source('components/server-backups.svelte')],
		['server-players', source('components/server-players.svelte')]
	] as const;
	for (const [name, src] of panes) {
		assert.ok(
			src.includes('tab-fill') || src.includes('ServerTabPanel'),
			`${name} no longer fills`
		);
		assert.ok(!src.includes('max-h-[800px]'), `${name} reintroduced the 800px cap`);
	}
	assert.ok(
		!source('components/files/server-files.svelte').includes('innerHeight'),
		'server-files reintroduced a viewport-minus-offset height'
	);
});
