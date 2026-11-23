// SPDX-License-Identifier: MPL-2.0

// Source contract for the mod update review/apply UX in server-mods.svelte.
// The tab is too wired to the live RPC client to mount cheaply, so these
// assertions pin the operator-visible behavior at the source level, mirroring
// the mod-browser-dialog contract test style.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

const componentPath = path.resolve(import.meta.dirname, 'server-mods.svelte');
const content = fs.readFileSync(componentPath, 'utf-8');

test('an explicit Check for updates action drives the preview fetch', () => {
	assert.ok(content.includes('Check for updates'), 'the affordance must be labelled');
	assert.ok(
		content.includes('onclick={() => loadMods(true)}'),
		'only the button click may request a hash-heavy update check'
	);
	assert.ok(
		content.includes('loadMods(checkUpdates = false)'),
		'list loads must default to skipping the update check'
	);
	assert.ok(
		content.includes('rpcClient.mod.listMods({ serverId, checkUpdates })'),
		'the checkUpdates flag must reach ListMods'
	);
	// The passive tab activation must not pass a truthy first argument.
	const effectBody = content.slice(content.indexOf('if (active && !hasLoaded)'));
	assert.match(
		effectBody,
		/loadMods\(\);\s*\n\s*loadPolicy\(\);/,
		'activating the tab lists mods without hashing jars for updates'
	);
});

test('update previews render as a CarbonTag badge with the reason visible', () => {
	assert.ok(
		content.includes('type={updateStateTagType(mod.updatePreview.state)}'),
		'the preview state must pick the CarbonTag tone through the helper'
	);
	assert.ok(
		content.includes('title={mod.updatePreview.reason}'),
		'the reason must be available as a tooltip on the badge'
	);
	assert.ok(
		content.includes('{mod.updatePreview.reason}'),
		'the reason must also render as subtext, not color alone'
	);
	assert.ok(
		content.includes("{mod.updatePreview.state.replace(/_/g, ' ')}"),
		'the badge must spell the state out'
	);
	assert.ok(
		content.includes("import type { Mod } from '$lib/proto/carbonpanel/v1/mod_pb'"),
		'previews come from the generated Mod.updatePreview contract'
	);
});

test('staging is an explicit select, window and apply flow', () => {
	// Selection checkboxes exist only for available rows and are labelled.
	const availableGuard = content.indexOf("{#if mod.updatePreview.state === 'available'}");
	const checkbox = content.indexOf('onchange={() => toggleUpdateSelection(mod.id)}');
	assert.ok(availableGuard > -1, 'only state=available rows may offer selection');
	assert.ok(checkbox > availableGuard, 'the checkbox must live inside the available guard');
	assert.ok(content.includes('Stage this update'), 'checkboxes must carry a visible label');

	assert.ok(
		content.includes('buildUpdateSelections(') && content.includes('currentSelectedUpdates'),
		'the request payload must be built by the shared helper from the current selections'
	);
	assert.ok(content.includes('id="mod-update-window"'), 'an apply window select is offered');
	assert.ok(
		content.includes('<option value="next">Next restart</option>') &&
			content.includes('<option value="time">At a chosen time (browser local)</option>') &&
			content.includes('<option value="idle">When the server is empty</option>'),
		'all three apply windows are selectable'
	);
	assert.ok(
		content.includes('updateSelections: selections') &&
			content.includes('updateWindow: applyWindowEnum()') &&
			content.includes('updateApplyAtUnix: BigInt(applyAtUnix)') &&
			content.includes('updateIdleMinutes: updateWindow'),
		'the UpdateMod request carries selections and the window fields'
	);
	assert.ok(
		content.includes('Stage {currentSelectedUpdates.length} selected update'),
		'the stage button counts the current selection'
	);
});

test('reviewed selections are bound to the candidate fingerprint', () => {
	assert.ok(
		content.includes('selectedUpdateFingerprints'),
		'the reviewed candidate fingerprint must be captured with the selection'
	);
	assert.ok(
		content.includes(
			'currentUpdateSelectionIds(mods, selectedUpdates, selectedUpdateFingerprints)'
		),
		'current selections must be filtered by the captured candidate fingerprint'
	);
	assert.ok(
		content.includes('checked={currentSelectedUpdates.includes(mod.id)}'),
		'a changed candidate must no longer render as checked'
	);
	assert.match(
		content,
		/buildUpdateSelections\([\s\S]*selectedUpdateFingerprints/,
		'the staging payload must recheck the candidate fingerprint'
	);
});

test('per-item staging outcomes are surfaced inline and via toast', () => {
	assert.ok(
		content.includes('stagedOutcomes = response.stagedUpdates.map'),
		'responses must feed the inline outcome list'
	);
	assert.ok(
		content.includes('{#each stagedOutcomes as row (row.modId)}'),
		'each outcome renders as its own row'
	);
	assert.ok(
		content.includes('type={updateOutcomeTagType(row.outcome)}') &&
			content.includes('{updateOutcomeLabel(row.outcome)}'),
		'outcome rows are tagged with a tone and a spelled-out label'
	);
	assert.ok(content.includes('{row.reason}'), 'outcome rows must show the refusal reason');
	assert.ok(content.includes('toast.error'), 'failures must also reach the toast stream');
	assert.ok(content.includes('await loadMods(true)'), 'a successful stage must refresh previews');
});

test('the automatic update policy panel is present and persisted via tasks', () => {
	assert.ok(content.includes('Automatic update policy'), 'the policy panel must be discoverable');
	assert.ok(
		content.includes('rpcClient.task.listTasks') && content.includes('TaskType.MOD_UPDATE'),
		'the policy must load the typed mod_update task'
	);
	assert.ok(
		content.includes('rpcClient.task.createTask') &&
			content.includes('rpcClient.task.updateTask') &&
			content.includes('rpcClient.task.toggleTask'),
		'saving must create, update and toggle through the task service'
	);
	assert.ok(
		content.includes('buildPolicyConfig(') && content.includes('policyFromPersistedTask('),
		'persistence must go through the shared policy helpers'
	);
	assert.ok(
		content.includes('bind:checked={policyEnabled}'),
		'opt-in is an explicit labelled checkbox'
	);
	assert.ok(
		content.includes('Skip while running') &&
			content.includes('Excluded project IDs') &&
			content.includes('Max per run') &&
			content.includes('Schedule (cron)') &&
			content.includes('Timezone'),
		'all policy knobs are operator visible'
	);
	assert.ok(
		content.includes('policySkipRunning = state.skipWhenRunning ?? false') &&
			content.includes('policySkipRunningExplicit ? policySkipRunning : undefined'),
		'an omitted skip_when_running setting must render false and remain omitted on save'
	);
	assert.ok(
		content.includes('Retry loading policy'),
		'a failed policy load offers a visible retry instead of faking readiness'
	);
});

test('spinner states use the shared Carbon spinner at small size', () => {
	const spinnerUses = content.match(/<CarbonSpinner[^>]*size="sm"/g) ?? [];
	assert.ok(spinnerUses.length >= 2, 'checking and staging states must use CarbonSpinner size sm');
	assert.ok(!content.includes('Loader2'), 'ad-hoc lucide spinners must not return');
});

test('existing mods tab functionality stays intact', () => {
	assert.ok(
		content.includes(
			'<ModBrowserDialog bind:open={browserDialogOpen} {server} onInstalled={loadMods} />'
		),
		'the browse dialog integration must be preserved'
	);
	assert.ok(
		content.includes('rpcClient.mod.importUploadedMod') &&
			content.includes('rpcClient.mod.deleteMod') &&
			content.includes('rpcClient.file.getFile'),
		'upload, delete and download flows must remain'
	);
	assert.ok(
		content.includes('modFilter') && content.includes('aria-pressed={modFilter ==='),
		'the filter chip toolbar must remain'
	);
	assert.ok(
		content.includes('role="status"'),
		'update progress must be announced via a status region'
	);
});
