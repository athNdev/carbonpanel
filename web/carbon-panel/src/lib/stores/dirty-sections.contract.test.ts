// SPDX-License-Identifier: MPL-2.0
//
// OSS-464 contract: the floating action bar's behavior markers — bottom-center
// placement, shake trigger, registry gating in front of Start/Restart/Force
// Recreate, and the persistence that keeps dirty sections alive across tab
// switches. Source-level assertions follow the layout-motion-policy convention;
// the runtime behavior is covered by the a11y-dom suite next to the component.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const read = (path: string): string =>
	readFileSync(new URL(path, import.meta.url), 'utf8').replace(/\r\n/g, '\n');

const bar = read('../components/carbon/CarbonFloatingActionBar.svelte');
const page = read('../../routes/servers/[id]/+page.svelte');
const css = read('../../app.css');
const registry = read('./dirty-sections.svelte.ts');
const settings = read('../components/server-settings.svelte');
const configuration = read('../components/server-configuration.svelte');
const routing = read('../components/server-routing.svelte');

test('the bar anchors to the bottom center and never overflows the viewport', () => {
	assert.match(bar, /data-carbon-floating-action-bar/, 'stable mount marker');
	assert.match(bar, /\bfixed\b/, 'the bar floats over the content');
	assert.match(bar, /\bbottom-0\b/, 'mobile: full-width bottom sheet');
	assert.match(bar, /\bleft-0\b/, 'mobile: spans the content pane');
	assert.match(bar, /\bz-50\b/, 'same stacking band as the other overlays');
	assert.match(bar, /\bsm:bottom-6\b/, 'desktop: lifted off the bottom edge');
	assert.match(bar, /\bsm:left-1\/2\b/, 'desktop: centered horizontally');
	assert.match(bar, /\bsm:-translate-x-1\/2\b/, 'desktop: centered horizontally');
	assert.match(bar, /\bmax-w-full\b/, 'must never force a horizontal scrollbar');
	assert.match(bar, /env\(safe-area-inset-bottom\)/, 'mobile safe-area inset is respected');
	assert.match(bar, /\bmin-h-10\b/, 'touch targets stay at least 40px tall on mobile');
});

test('the shake is a token-driven keyframe that reduced motion zeroes', () => {
	assert.match(css, /@keyframes carbon-shake \{/);
	const rule = css.match(/\.carbon-floating-action-bar\[data-shake\] \{([^}]*)\}/);
	assert.ok(rule, 'the bar owns a [data-shake] animation rule');
	assert.match(rule[1], /var\(--motion-slow\)/, 'the duration comes from the motion tokens');
	assert.match(rule[1], /\b2\s*;/, 'two cycles per refusal');
	assert.match(css, /transform: translateX\(-4px\)/, 'the brief translateX sequence');
	// Exactly one carbon-floating-action-bar rule exists: the global
	// prefers-reduced-motion and data-carbon-motion kill switches already zero
	// the animation, and the shake adds no exemption of its own.
	assert.equal(css.match(/carbon-floating-action-bar/g)?.length, 1);
	assert.match(
		css,
		/@media \(prefers-reduced-motion: reduce\) \{[\s\S]*animation-duration: 0\.01ms !important/
	);
	assert.match(css, /\[data-carbon-motion='reduced'\] \*,[\s\S]{0,200}animation-duration/);
});

test('the bar re-fires the shake and announces state to screen readers', () => {
	assert.match(bar, /data-shake=/, 'the attribute toggle re-fires the keyframes');
	assert.match(bar, /requestAnimationFrame/, 'repeat refusals restart from a clean frame');
	assert.match(bar, /motionDuration\(/, 'JS-triggered state honors the reduced-motion gate');
	assert.match(bar, /role="status"/, 'aria-live announcements for dirty and save/undo states');
	assert.match(bar, /aria-live="polite"/);
});

test('a refusal moves focus to the bar without stealing focus from inside it', () => {
	assert.match(bar, /tabindex="-1"/, 'the bar container is programmatically focusable');
	assert.match(
		bar,
		/!barHost\.contains\(document\.activeElement\)/,
		'focus already inside the bar is not stolen'
	);
	assert.match(bar, /barHost\.focus\(/, 'focus moves to the bar when the shake tick fires');
});

test('save all skips sections that became clean mid-run', () => {
	assert.match(
		bar,
		/if \(!entry\.isDirty\(\)\) continue;/,
		'a section whose chip save completed while Save all runs is not re-saved'
	);
});

test('the registry exposes the brief entry shape and is idempotent by id', () => {
	assert.match(registry, /readonly id: string/);
	assert.match(registry, /readonly label: string/);
	assert.match(registry, /readonly isDirty: \(\) => boolean/);
	assert.match(registry, /readonly save: \(\) => Promise<void>/);
	assert.match(registry, /readonly undo: \(\) => void/);
	assert.match(registry, /readonly saving: \(\) => boolean/);
	assert.match(registry, /register\(entry: DirtySectionEntry\): void/);
	assert.match(registry, /unregister\(id: string\): void/);
	assert.match(registry, /requestShake\(\): void/);
	assert.match(
		registry,
		/findIndex\(\(existing\) => existing\.id === entry\.id\)/,
		'duplicate registration replaces the entry instead of stacking it'
	);
});

test('start, restart and force recreate are gated on unsaved edits; stop and delete are not', () => {
	assert.match(
		page,
		/gatedServerActions[^=]*=\s*new Set\(\['start',\s*'restart',\s*'recreate'\]\)/,
		'the gated set is exactly the actions that consume staged state'
	);
	const gateIndex = page.indexOf('refuseGatedAction');
	const performIndex = page.indexOf('serverActions.perform(target.id, action)');
	assert.ok(gateIndex > -1, 'the gate is wired into handleServerAction');
	assert.ok(performIndex > -1);
	assert.ok(gateIndex < performIndex, 'the gate runs before the action is performed');
	assert.match(page, /dirtySections\.requestShake\(\)/, 'refusal shakes the bar');
	assert.match(page, /Unsaved changes in: /, 'multi-section refusal names every section');
	assert.match(page, /Save or undo your changes in /, 'single-section refusal names the section');

	const deleteStart = page.indexOf('async function confirmDeleteServer');
	const deleteEnd = page.indexOf('function copyConnection');
	assert.ok(deleteStart > -1 && deleteEnd > deleteStart);
	assert.doesNotMatch(
		page.slice(deleteStart, deleteEnd),
		/dirtySection/,
		'delete keeps its own confirm flow and never consults the registry'
	);
});

test('dirty sections stay mounted across tab switches', () => {
	assert.match(page, /visitedOverview/);
	assert.match(page, /visitedConfiguration/);
	assert.match(page, /visitedRouting/);
	assert.match(page, /hidden=\{activeTab !== 'overview'\}/);
	assert.match(page, /hidden=\{activeTab !== 'configuration'\}/);
	assert.match(page, /hidden=\{activeTab !== 'routing'\}/);
	assert.match(page, /server-detail-tabs-panel-overview/);
	assert.match(page, /server-detail-tabs-panel-configuration/);
	assert.match(page, /server-detail-tabs-panel-routing/);
	assert.match(
		page,
		/new Set\(\['overview', 'configuration', 'routing'\]\)/,
		'routing is registered, persistent and keep-mounted with the other editable sections'
	);
	assert.match(page, /persistentTabs/, 'transient tabs keep mounting on demand');
});

test('unvisited persistent tabs render placeholder panels so aria-controls resolves', () => {
	// CarbonInactiveTabPanels only covers the transient tabs, so a deep link to
	// ?tab=configuration would leave the Overview/Configuration tab buttons
	// referencing nonexistent panel ids until first visit.
	const placeholder = (id: string) =>
		new RegExp(
			`id="server-detail-tabs-panel-${id}"\\s+role="tabpanel"\\s+aria-labelledby="server-detail-tabs-tab-${id}"\\s+hidden\\s*>`
		);
	assert.match(page, placeholder('overview'), 'inert placeholder until first visit');
	assert.match(page, placeholder('configuration'), 'inert placeholder until first visit');
	assert.match(page, placeholder('routing'), 'inert placeholder until first visit');
});

test('the page mounts the bar and deep-links chips through tab selection', () => {
	assert.match(page, /<CarbonFloatingActionBar onSelectSection=\{selectServerTab\} \/>/);
});

test('settings and configuration register and keep their existing save semantics', () => {
	assert.match(settings, /id: 'overview'/);
	assert.match(settings, /label: 'Server Settings'/);
	assert.match(settings, /save: \(\) => saveSettings\(true\)/);
	assert.match(settings, /unregister\('overview'\)/, 'unmount cleanup cannot leave a dead gate');
	assert.match(settings, /rpcClient\.server\.updateServer\(request\)/, 'the save RPC is unchanged');

	assert.match(configuration, /id: 'configuration'/);
	assert.match(configuration, /label: 'Server Configuration'/);
	assert.match(configuration, /save: \(\) => runStage\(true, true\)/, 'bar save keeps staging');
	assert.match(configuration, /undo: handleReset/, 'Reset maps to the undo affordance');
	assert.match(configuration, /unregister\('configuration'\)/);
	assert.match(
		configuration,
		/rpcClient\.config\.updateServerConfig\(/,
		'the save RPC is unchanged'
	);

	assert.match(routing, /id: 'routing'/);
	assert.match(routing, /label: 'Routing'/);
	assert.match(routing, /save: \(\) => saveRouting\(true\)/);
	assert.match(routing, /unregister\('routing'\)/, 'unmount cleanup cannot leave a dead gate');
	assert.match(routing, /rpcClient\.proxy\.updateServerRouting\(/, 'the save RPC is unchanged');
});

test('settings dirty state uses a captured baseline, not the live server store', () => {
	assert.match(
		settings,
		/let baseline = \$state<UpdateServerRequest>\(serverFormValues\(server\)\)/,
		'the baseline is captured at load'
	);
	assert.match(
		settings,
		/let isDirty = \$derived\(formDiffersFromBaseline\(formData\)\)/,
		'isDirty compares the form against the baseline'
	);
	assert.doesNotMatch(
		settings,
		/formData\.name !== server\.name/,
		'the live-store comparison is gone'
	);
	assert.match(
		settings,
		/baseline = sent;/,
		'a successful save re-baselines to the sent values, before the store refresh lands'
	);
	assert.match(
		settings,
		/formData = snapshotForm\(baseline\);/,
		'undo reverts to the captured baseline'
	);
});

test('configuration keeps edits typed during an in-flight save', () => {
	const rpcAt = configuration.indexOf('await rpcClient.config.updateServerConfig(');
	const captureAt = configuration.indexOf(
		'const inFlightValues = new SvelteMap(currentValues);',
		rpcAt
	);
	const processAt = configuration.indexOf('processConfig(response.categories);', rpcAt);
	assert.ok(
		rpcAt > -1 && captureAt > rpcAt,
		'the in-flight form is captured after the request resolves'
	);
	assert.ok(
		processAt > captureAt,
		'the capture runs before the response refresh overwrites the form'
	);
	assert.match(
		configuration,
		/rebaselineAfterSave\(sent, inFlightValues, inFlightEnabled\);/,
		'the response refresh re-baselines to the sent values and restores concurrent edits'
	);
});
