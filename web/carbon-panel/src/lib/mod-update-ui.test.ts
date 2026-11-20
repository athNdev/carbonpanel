// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
	buildPolicyConfig,
	buildUpdateSelections,
	updateStateClass,
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
