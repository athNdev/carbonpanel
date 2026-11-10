// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

test('ServerConsole does not depend on Tooltip context and has accessible tooltips', () => {
	const consolePath = path.resolve(import.meta.dirname, 'server-console.svelte');
	const content = fs.readFileSync(consolePath, 'utf-8');

	// Must never import or use shadcn/bits-ui Tooltip inside ServerConsole.
	// Tooltip.Root throws Context "Tooltip.Provider" not found if mounted outside a provider,
	// which previously broke the Console tab and wedged Svelte 5's reactive effect scheduler.
	assert.ok(
		!content.includes('Tooltip.Root'),
		'ServerConsole must not use Tooltip.Root (causes Context "Tooltip.Provider" not found)'
	);
	assert.ok(!content.includes('Tooltip.Trigger'), 'ServerConsole must not use Tooltip.Trigger');
	assert.ok(!content.includes('Tooltip.Content'), 'ServerConsole must not use Tooltip.Content');
	assert.ok(
		!content.includes('$lib/components/ui/tooltip'),
		'ServerConsole must not import tooltip UI primitives'
	);

	// Ensure action buttons have both aria-label and title for accessibility and native tooltips
	assert.ok(
		content.includes('aria-label="Refresh logs"') && content.includes('title="Refresh logs"'),
		'Refresh logs button must declare aria-label and title'
	);
	assert.ok(
		content.includes('aria-label="Upload logs to mclo.gs"') &&
			content.includes('title="Upload logs to mclo.gs"'),
		'Upload logs button must declare aria-label and title'
	);
	assert.ok(
		content.includes('aria-label="Download raw log"') &&
			content.includes('title="Download raw log"'),
		'Download logs button must declare aria-label and title'
	);
	assert.ok(
		content.includes('aria-label="Clear log buffer"'),
		'Clear logs button must declare aria-label'
	);
	assert.ok(
		content.includes('viewed buffer'),
		'Clear affordance must state it clears the viewed buffer'
	);
});

test('Root layout provides global Tooltip.Provider safety net', () => {
	const layoutPath = path.resolve(import.meta.dirname, '../../routes/+layout.svelte');
	const content = fs.readFileSync(layoutPath, 'utf-8');

	assert.ok(
		content.includes("import * as Tooltip from '$lib/components/ui/tooltip/index.js';") ||
			content.includes("from '$lib/components/ui/tooltip"),
		'Root layout must import Tooltip provider'
	);
	assert.ok(
		content.includes('<Tooltip.Provider>'),
		'Root layout must wrap application with <Tooltip.Provider>'
	);
});

test('Server detail page wraps tab panels with svelte:boundary to prevent tab wedging', () => {
	const serverPagePath = path.resolve(
		import.meta.dirname,
		'../../routes/servers/[id]/+page.svelte'
	);
	const content = fs.readFileSync(serverPagePath, 'utf-8');

	assert.ok(
		content.includes('<svelte:boundary>'),
		'Server detail page must wrap tab switching in <svelte:boundary>'
	);
	assert.ok(
		content.includes('{#snippet failed(error, reset)}'),
		'Server detail page must declare failed fallback snippet for svelte:boundary'
	);
	assert.ok(
		content.includes("<ServerConsole {server} active={activeTab === 'console'} />"),
		'Server detail page must render ServerConsole'
	);
});

test('ServerConsole gates clear behind a danger confirm and states viewed-buffer semantics', () => {
	const consolePath = path.resolve(import.meta.dirname, 'server-console.svelte');
	const content = fs.readFileSync(consolePath, 'utf-8');
	assert.ok(content.includes('CarbonConfirm'), 'Console must use CarbonConfirm for clear');
	assert.ok(content.includes('clearConfirmOpen'), 'Clear must open a confirm dialog');
	assert.ok(content.includes('danger={true}'), 'Clear confirm must use the danger pattern');
	assert.ok(
		content.includes('viewed buffer only') || content.includes('viewed buffer'),
		'Clear confirm must state only the viewed buffer is cleared'
	);
	assert.ok(
		content.includes('Daemon logs are kept') || content.includes('daemon logs are kept'),
		'Clear confirm must state daemon logs are kept'
	);
	assert.ok(
		!content.includes('onclick={clearLogs}'),
		'Clear button must not call clearLogs directly; it must open the confirm first'
	);
});

test('ServerConsole filters client-side and expands fullscreen via CarbonModal', () => {
	const consolePath = path.resolve(import.meta.dirname, 'server-console.svelte');
	const content = fs.readFileSync(consolePath, 'utf-8');
	assert.ok(content.includes('CarbonSearch'), 'Console must reuse CarbonSearch for filtering');
	assert.ok(content.includes('searchQuery'), 'Console must keep client-side filter state');
	assert.ok(content.includes('filteredLogs'), 'Console must derive filtered logs');
	assert.ok(content.includes('CarbonModal'), 'Console must host fullscreen in CarbonModal');
	assert.ok(content.includes('fullscreenOpen'), 'Console must gate fullscreen behind state');
	assert.ok(
		content.includes('aria-label="Expand console"'),
		'Fullscreen button must declare aria-label'
	);
	assert.ok(content.includes('logList'), 'Console must extract the log list for reuse');
});

test('Fullscreen console reuses the complete console pane', () => {
	const consolePath = path.resolve(import.meta.dirname, 'server-console.svelte');
	const content = fs.readFileSync(consolePath, 'utf-8');

	const start = content.indexOf('{#snippet consolePane(expanded: boolean)}');
	const marker = content.search(/\{\/snippet\}\r?\n\r?\n\{#if !fullscreenOpen\}/);
	assert.ok(start !== -1 && marker > start, 'Console must share one pane snippet');
	const pane = content.slice(start, marker);

	assert.ok(pane.includes('data-testid="console-send"'), 'Fullscreen must keep the command input');
	assert.ok(pane.includes('aria-label="Refresh logs"'), 'Fullscreen must keep the toolbar');
	assert.ok(
		pane.includes("aria-label={expanded ? 'Exit fullscreen console' : 'Expand console'}"),
		'The pane must expose the fullscreen toggle in both modes'
	);
	assert.ok(
		pane.includes('aria-label="Close fullscreen console"'),
		'Fullscreen needs an explicit close affordance'
	);
	assert.ok(
		content.includes('{@render consolePane(false)}') &&
			content.includes('{@render consolePane(true)}'),
		'One pane snippet must serve the inline and fullscreen states'
	);
	assert.ok(
		content.includes('fullscreen'),
		'Fullscreen modal must opt into the edge-to-edge layout'
	);
	assert.ok(content.includes('bare'), 'Fullscreen modal must hide default modal chrome');
});
