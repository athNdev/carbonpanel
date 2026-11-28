// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Source contract for the mods browser dialog. The dialog delegates catalog
// state to the shared mods-browser-store and result rows to the shared
// ModsCatalogResultCard, so assertions read the file where each decision now
// lives (see docs/plans/mods-ui-unification.md §6).

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { sameModInstallContext, type ModInstallContext } from '../mods/dependency-plan';

const dialogPath = path.resolve(import.meta.dirname, 'mod-browser-dialog.svelte');
const content = fs.readFileSync(dialogPath, 'utf-8');
const modsDir = path.resolve(import.meta.dirname, '../mods');
const store = fs.readFileSync(path.join(modsDir, 'mods-browser-store.svelte.ts'), 'utf-8');
const resultCard = fs.readFileSync(path.join(modsDir, 'ModsCatalogResultCard.svelte'), 'utf-8');

test('mod install posts file_name so the backend accepts it', () => {
	// The backend decodes InstallModItem.file_name. The dialog once posted
	// `filename`, which decoded to empty and failed every install with
	// "missing url or filename".
	assert.ok(
		content.includes('file_name: selectedVersion.file_name'),
		'install payload must use file_name'
	);
	assert.ok(
		!content.includes('filename: selectedVersion.file_name'),
		'install payload must not use the legacy filename key'
	);
});

test('install names the selected root file so a dependency-only response is not success', () => {
	assert.ok(
		content.includes('rootFileName: selectedVersion.file_name'),
		'the dialog must tell the store which installed file is the selected mod'
	);
	assert.ok(
		store.includes('installedFiles.has(options.rootFileName)'),
		'the store must require the root file in the installed list'
	);
});

test('install button offers Install and Install with all dependencies', () => {
	assert.ok(
		content.includes("'Install with all dependencies'") && content.includes("'Install'"),
		'button must label itself Install or Install with all dependencies'
	);
	assert.ok(!content.includes('1-Click Install'), 'legacy 1-Click label must be gone');
});

test('switching versions re-selects that version�s required dependencies', () => {
	assert.ok(
		content.includes('function selectVersion('),
		'version selection must go through selectVersion'
	);
	assert.ok(
		content.includes('onclick={() => selectVersion(ver)}'),
		'version list must call selectVersion'
	);
});

test('dependencies are opt-in checkboxes that feed the install', () => {
	assert.ok(content.includes('type="checkbox"'), 'dependencies must render checkboxes');
	assert.ok(
		content.includes('selectedDependencies[dep.project_id]'),
		'checkboxes must drive selectedDependencies'
	);
	assert.ok(
		content.includes('resolveDependencyInstallItems'),
		'selected dependencies must be resolved into install items'
	);
});

test('dependency rows show the mod name, falling back to the raw id', () => {
	assert.ok(
		content.includes('{dep.title || dep.project_id}'),
		'dependency rows must prefer the resolved title over the raw project id'
	);
});

test('header chip counts mods added during this dialog session', () => {
	assert.ok(
		content.includes('<CarbonTag type="green" size="sm">{sessionInstalledCount} added</CarbonTag>'),
		'header must show an N added green chip once mods are installed this session'
	);
	assert.ok(
		content.includes('sessionInstalledCount > 0'),
		'the chip must only appear once at least one mod was added'
	);
	// The reset moved with the state machine: the store clears the session log.
	assert.ok(
		store.includes('sessionInstalled = []'),
		'the session count must reset when the dialog closes (store.closeDialog)'
	);
});

test('session summary lists mods added this session above the results', () => {
	assert.ok(
		content.includes('id="session-summary"'),
		'session summary must be present and scroll-targetable from the header chip'
	);
	assert.ok(content.includes('Added this session'), 'session summary must carry a visible label');
	assert.ok(
		content.includes('sessionSummaryOpen = !sessionSummaryOpen'),
		'session summary must be collapsible'
	);
	assert.ok(
		content.includes('sessionSummaryOpen = true') &&
			content.includes('border border-border bg-card'),
		'session summary must open on install and use the card surface'
	);
	assert.ok(
		content.includes('{entry.title}') &&
			content.includes('v{entry.version}') &&
			content.includes('{entry.depCount}'),
		'session summary rows must show name, version and dependency count'
	);
});

test('result cards use the Carbon green tag for installed state', () => {
	// The badge vocabulary moved to the shared result card used by every
	// mods surface.
	assert.ok(
		resultCard.includes(
			'<CarbonTag type={STATE_TAGS[state].tone} size="sm">{STATE_TAGS[state].label}</CarbonTag>'
		) || resultCard.includes('Installed'),
		'pre-existing installs must show the CarbonTag Installed badge'
	);
	assert.ok(
		resultCard.includes('Added just now'),
		'session installs show the Added just now badge'
	);
	assert.ok(
		!content.includes('bg-emerald-600') && !resultCard.includes('bg-emerald-600'),
		'the tiny emerald installed badge must be replaced by the Carbon tag'
	);
	assert.ok(!content.includes("from '$lib/components/ui/badge'"), 'shadcn badges are gone');
	assert.ok(!content.includes("from '$lib/components/ui/card'"), 'shadcn cards are gone');
	assert.ok(!content.includes("from '$lib/components/ui/alert'"), 'shadcn alerts are gone');
	assert.ok(!content.includes("from '$lib/components/ui/tabs'"), 'shadcn tabs are gone');
});

test('version rows carry an explicit selected check indicator', () => {
	assert.ok(
		content.includes('<Check class="h-4 w-4 text-primary" />'),
		'the selected version row must render a check icon beside the highlight'
	);
	assert.ok(
		content.includes('{#if selectedVersion?.id === ver.id}'),
		'the check must be driven by the current version selection'
	);
});

test('install copy names the real content directory for plugin loaders', () => {
	assert.ok(!content.includes('Installing to /mods'), 'the hardcoded /mods copy must be gone');
	assert.ok(
		content.includes('Installing to ${contentDirectory}/'),
		'the install button derives mods/ vs plugins/ from the game loader'
	);
	assert.ok(
		content.includes('contentDirectoryFor(gameLoader)'),
		'the directory comes from the shared loader mapping'
	);
});

test('Hangar results are labeled as a first-class platform', () => {
	assert.ok(
		content.includes('platform.toUpperCase()') || content.includes('PLATFORM_LABELS'),
		'version view keeps the platform badge'
	);
	assert.ok(resultCard.includes('hangar'), 'the shared result card labels Hangar results');
});

test('closing the dialog mid-install cannot wedge the install button (E7)', () => {
	// CLOSE resets the whole install region, including the installing flag.
	const closeBody = store.slice(store.indexOf('closeDialog()'));
	assert.ok(
		closeBody.includes('installing = false'),
		'the store closeDialog must reset installing'
	);
});

test('unresolved dependencies surface in the install outcomes (E9)', () => {
	assert.ok(
		content.includes('store.appendOutcomes'),
		'the dialog appends unresolved dependencies to the outcome list'
	);
	assert.ok(
		content.includes("tag: 'Unresolved'"),
		'unresolved rows are spelled out, not silently dropped'
	);
});

test('dialog state resets when the target server identity changes (E22)', () => {
	assert.ok(
		content.includes('store.setTarget('),
		'a server/loader/version change re-keys the store'
	);
	assert.ok(
		content.includes('syncedTargetKey'),
		'the target sync is guarded so it only fires on real changes'
	);
});

test('the install context is revalidated after dependency planning', () => {
	const context: ModInstallContext = {
		serverId: 'server-1',
		serverMcVersion: '1.21.1',
		serverLoader: 'fabric',
		lookupLoader: 'fabric',
		lookupMcVersion: '1.21.1'
	};
	assert.equal(sameModInstallContext(context, { ...context }), true);
	assert.equal(
		sameModInstallContext(context, { ...context, serverId: 'server-2' }),
		false,
		'a changed server must invalidate the plan'
	);
	assert.equal(
		sameModInstallContext(context, { ...context, serverMcVersion: '1.20.6' }),
		false,
		'a changed Minecraft version must invalidate the plan'
	);
	assert.equal(
		sameModInstallContext(context, { ...context, lookupLoader: 'neoforge' }),
		false,
		'a changed loader must invalidate the plan'
	);
	assert.equal(
		sameModInstallContext(context, { ...context, lookupMcVersion: '1.20.6' }),
		false,
		'a changed lookup version must invalidate the plan'
	);

	const planning = content.indexOf('const depItems = await resolveDependencyInstallItems(');
	const validation = content.indexOf('sameModInstallContext(installContext, currentContext)');
	const install = content.indexOf('await store.installServerItems(');
	assert.ok(planning > -1, 'dependency planning must be captured in the source contract');
	assert.ok(
		validation > planning && install > validation,
		'the install request must be refused when the context changed after planning'
	);
	assert.ok(
		content.includes('store.target.id !== installContext.serverId'),
		'the store target must still point at the server used for planning'
	);
});
