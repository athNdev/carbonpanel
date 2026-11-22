// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

const dialogPath = path.resolve(import.meta.dirname, 'mod-browser-dialog.svelte');
const content = fs.readFileSync(dialogPath, 'utf-8');

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
	assert.ok(
		content.includes('sessionInstalled = []'),
		'the session count must reset when the dialog closes'
	);
});

test('session summary lists mods added this session above the results', () => {
	assert.ok(
		content.includes('id="session-summary"'),
		'session summary must be present and scroll-targetable from the header chip'
	);
	assert.ok(
		content.includes('Added this session'),
		'session summary must carry a visible label'
	);
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
		content.includes('{entry.title}') && content.includes('v{entry.version}') && content.includes('{entry.depCount}'),
		'session summary rows must show name, version and dependency count'
	);
});

test('result cards use the Carbon green tag for installed state', () => {
	assert.ok(
		content.includes('<CarbonTag type="green" size="sm">Installed</CarbonTag>'),
		'pre-existing installs must show the CarbonTag Installed badge'
	);
	assert.ok(
		content.includes('<CarbonTag type="green" size="sm">Added just now</CarbonTag>'),
		'mods installed during this session must show the Added just now badge'
	);
	assert.ok(
		!content.includes('bg-emerald-600'),
		'the tiny emerald installed badge must be replaced by the Carbon tag'
	);
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
