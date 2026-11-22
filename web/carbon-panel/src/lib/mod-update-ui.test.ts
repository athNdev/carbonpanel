// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
	buildPolicyConfig,
	buildUpdateSelections,
	DEFAULT_POLICY_STATE,
	policyFromPersistedTask,
	updateOutcomeLabel,
	updateOutcomeTagType,
	updateStateClass,
	updateStateTagType,
	type ModLike
} from './mod-update-ui';

const available: ModLike = {
	id: 'mod-1',
	fileName: 'mod.jar',
	updatePreview: {
		state: 'available',
		installedSha512: 'installed-hash',
		candidateVersionId: 'version-2',
		candidateSha512: 'candidate-hash',
		candidateFilename: 'mod-2.jar'
	}
};

test('installed mod update selection only includes checked available previews', () => {
	const mods: ModLike[] = [
		available,
		{
			id: 'mod-2',
			fileName: 'review.jar',
			updatePreview: { state: 'review_required', candidateSha512: 'x' }
		},
		{ id: 'mod-3', fileName: 'plain.jar' }
	];
	assert.deepEqual(buildUpdateSelections(mods, ['mod-1']), [
		{
			modId: 'mod-1',
			fileName: 'mod.jar',
			installedSha512: 'installed-hash',
			candidateVersionId: 'version-2',
			candidateSha512: 'candidate-hash',
			candidateFilename: 'mod-2.jar'
		}
	]);
	assert.deepEqual(buildUpdateSelections(mods, ['mod-2', 'mod-3']), []);
});

test('each preview state has the intended presentation treatment', () => {
	const classes = ['available', 'current', 'unknown_source', 'unavailable', 'review_required'].map(
		updateStateClass
	);
	assert.equal(new Set(classes).size, 4, 'review_required shares the neutral treatment');
	assert.match(classes[0], /text-success-text/);
	assert.match(classes[2], /text-destructive-text/);
});

test('preview states map to CarbonTag tones without color-only signaling', () => {
	assert.equal(updateStateTagType('available'), 'green');
	assert.equal(updateStateTagType('current'), 'gray');
	assert.equal(updateStateTagType('review_required'), 'yellow');
	assert.equal(updateStateTagType('unknown_source'), 'gray');
	assert.equal(updateStateTagType('unavailable'), 'red');
	assert.equal(updateStateTagType(undefined), 'gray', 'unknown states fall back to gray');
	assert.equal(updateStateTagType('mystery'), 'gray');
});

test('staging outcomes map to CarbonTag tones and spelled-out labels', () => {
	assert.equal(updateOutcomeTagType('staged'), 'green');
	assert.equal(updateOutcomeTagType('refused_drift'), 'yellow');
	assert.equal(updateOutcomeTagType('refused_provider'), 'yellow');
	assert.equal(updateOutcomeTagType('refused_download'), 'yellow');
	assert.equal(updateOutcomeTagType('refused_checksum'), 'yellow');
	assert.equal(updateOutcomeTagType('error'), 'red');
	assert.equal(updateOutcomeTagType('future_outcome'), 'red', 'unknown outcomes fail visible');
	assert.equal(updateOutcomeLabel('staged'), 'Staged');
	assert.equal(updateOutcomeLabel('refused_checksum'), 'Refused: checksum mismatch');
	assert.equal(updateOutcomeLabel('something_else'), 'something else');
});

test('a disabled first save reloads unchecked and idle duration is persisted', () => {
	const disabled = policyFromPersistedTask(
		{ config: JSON.stringify({ enabled: false, window: 'idle', idle_minutes: 30 }), status: 1 },
		1
	);
	assert.equal(disabled.enabled, false, 'the persisted opt-in must beat task status');
	assert.equal(disabled.window, 'idle');
	assert.equal(disabled.idleMinutes, 30);
	const fromConfig = buildPolicyConfig({ ...disabled, maxPerRun: disabled.maxPerRun });
	assert.deepEqual(JSON.parse(fromConfig), {
		enabled: false,
		excluded_projects: [],
		max_updates_per_run: 5,
		skip_when_running: true,
		window: 'idle',
		idle_minutes: 30
	});
	assert.deepEqual(policyFromPersistedTask(null, 1), DEFAULT_POLICY_STATE);
});

test('policy config is opt-in, excludes blank IDs and bounds the batch', () => {
	const config = JSON.parse(
		buildPolicyConfig({
			enabled: true,
			excludedProjects: ' project-a, ,project-b ',
			maxPerRun: 3,
			skipWhenRunning: true
		})
	);
	assert.deepEqual(config.excluded_projects, ['project-a', 'project-b']);
	assert.equal(config.max_updates_per_run, 3);
	assert.equal(config.skip_when_running, true);
	assert.equal(config.window, 'next_restart');
});
