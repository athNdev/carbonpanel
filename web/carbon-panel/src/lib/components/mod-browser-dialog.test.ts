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
