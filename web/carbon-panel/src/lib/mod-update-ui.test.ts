// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
	buildPolicyConfig,
	buildUpdateSelections,
	currentUpdateSelectionIds,
	DEFAULT_POLICY_STATE,
	policyFromPersistedTask,
	pruneUpdateSelections,
	updateCandidateFingerprint,
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
	const fingerprints = { 'mod-1': updateCandidateFingerprint(available.updatePreview) };
	assert.deepEqual(buildUpdateSelections(mods, ['mod-1'], fingerprints), [
		{
			modId: 'mod-1',
			fileName: 'mod.jar',
			installedSha512: 'installed-hash',
			candidateVersionId: 'version-2',
			candidateSha512: 'candidate-hash',
			candidateFilename: 'mod-2.jar'
		}
	]);
	assert.deepEqual(
		buildUpdateSelections(mods, ['mod-2', 'mod-3'], {
			'mod-2': updateCandidateFingerprint(mods[1].updatePreview),
			'mod-3': ''
		}),
		[]
	);
});

function withPreview(changes: Partial<NonNullable<ModLike['updatePreview']>>): ModLike {
	return {
		...available,
		updatePreview: { ...available.updatePreview, ...changes }
	};
}

test('a recorded candidate fingerprint stages only while the reviewed release is still offered', () => {
	const fingerprint = updateCandidateFingerprint(available.updatePreview);
	assert.deepEqual(buildUpdateSelections([available], ['mod-1'], { 'mod-1': fingerprint }), [
		{
			modId: 'mod-1',
			fileName: 'mod.jar',
			installedSha512: 'installed-hash',
			candidateVersionId: 'version-2',
			candidateSha512: 'candidate-hash',
			candidateFilename: 'mod-2.jar'
		}
	]);
});

test('a refreshed candidate version, digest or filename invalidates the selection', () => {
	const fingerprint = updateCandidateFingerprint(available.updatePreview);
	for (const changes of [
		{ candidateVersionId: 'version-3' },
		{ candidateSha512: 'other-hash' },
		{ candidateFilename: 'mod-3.jar' }
	]) {
		assert.deepEqual(
			buildUpdateSelections([withPreview(changes)], ['mod-1'], { 'mod-1': fingerprint }),
			[],
			`${Object.keys(changes)[0]} change must not stage the unreviewed candidate`
		);
	}
});

test('a row leaving the available state invalidates the selection', () => {
	const fingerprint = updateCandidateFingerprint(available.updatePreview);
	assert.deepEqual(
		buildUpdateSelections([withPreview({ state: 'review_required' })], ['mod-1'], {
			'mod-1': fingerprint
		}),
		[]
	);
});

test('a retained non-available selection is not stageable until the reviewed candidate returns', () => {
	const fingerprint = updateCandidateFingerprint(available.updatePreview);
	const fingerprints = { 'mod-1': fingerprint };
	const unavailable = withPreview({ state: 'unavailable' });
	assert.deepEqual(currentUpdateSelectionIds([unavailable], ['mod-1'], fingerprints), []);
	assert.deepEqual(buildUpdateSelections([unavailable], ['mod-1'], fingerprints), []);
	// A provider recovery that offers the same candidate revalidates the intent.
	assert.deepEqual(currentUpdateSelectionIds([available], ['mod-1'], fingerprints), ['mod-1']);
	assert.deepEqual(buildUpdateSelections([available], ['mod-1'], fingerprints), [
		{
			modId: 'mod-1',
			fileName: 'mod.jar',
			installedSha512: 'installed-hash',
			candidateVersionId: 'version-2',
			candidateSha512: 'candidate-hash',
			candidateFilename: 'mod-2.jar'
		}
	]);
});

test('an installed digest change invalidates the reviewed selection in both helpers', () => {
	const fingerprint = updateCandidateFingerprint(available.updatePreview);
	const reinstalled = withPreview({ installedSha512: 'other-installed-hash' });
	assert.deepEqual(
		currentUpdateSelectionIds([reinstalled], ['mod-1'], { 'mod-1': fingerprint }),
		[]
	);
	assert.deepEqual(buildUpdateSelections([reinstalled], ['mod-1'], { 'mod-1': fingerprint }), []);
});

test('a selection without a recorded fingerprint cannot stage', () => {
	assert.deepEqual(currentUpdateSelectionIds([available], ['mod-1'], {}), []);
	assert.deepEqual(buildUpdateSelections([available], ['mod-1'], {}), []);
});

test('current selection ids keep only rows whose candidate still matches the review', () => {
	const unchanged: ModLike = { ...available, id: 'mod-1', fileName: 'unchanged.jar' };
	const changed: ModLike = {
		...withPreview({ candidateVersionId: 'version-3' }),
		id: 'mod-2',
		fileName: 'changed.jar'
	};
	const fingerprints = {
		'mod-1': updateCandidateFingerprint(unchanged.updatePreview),
		'mod-2': updateCandidateFingerprint(available.updatePreview)
	};
	assert.deepEqual(
		currentUpdateSelectionIds([unchanged, changed], ['mod-1', 'mod-2'], fingerprints),
		['mod-1']
	);
	assert.deepEqual(currentUpdateSelectionIds([unchanged, changed], ['mod-1', 'mod-2'], {}), []);
});

test('the candidate fingerprint distinguishes version, digest and filename', () => {
	const base = updateCandidateFingerprint(available.updatePreview);
	assert.notEqual(
		base,
		updateCandidateFingerprint({ ...available.updatePreview, candidateVersionId: 'v3' })
	);
	assert.notEqual(
		base,
		updateCandidateFingerprint({ ...available.updatePreview, candidateSha512: 's3' })
	);
	assert.notEqual(
		base,
		updateCandidateFingerprint({ ...available.updatePreview, candidateFilename: 'f3.jar' })
	);
	assert.notEqual(
		base,
		updateCandidateFingerprint({ ...available.updatePreview, installedSha512: 'i3' })
	);
	assert.equal(updateCandidateFingerprint(null), '');
});

test('pruning clears only a changed available candidate and keeps the rest selected', () => {
	const fingerprint = updateCandidateFingerprint(available.updatePreview);
	const unchanged: ModLike = { ...available, id: 'mod-1' };
	const changed: ModLike = {
		...withPreview({ candidateVersionId: 'version-3' }),
		id: 'mod-2',
		fileName: 'changed.jar'
	};
	const prune = pruneUpdateSelections(
		[unchanged, changed],
		['mod-1', 'mod-2'],
		{ 'mod-1': fingerprint, 'mod-2': fingerprint },
		true
	);
	assert.deepEqual(prune.kept, ['mod-1']);
	assert.deepEqual(prune.changed, ['mod-2']);
	assert.deepEqual(prune.missing, []);
	assert.deepEqual(prune.fingerprints, { 'mod-1': fingerprint });
});

test('a check that withdraws availability retains the reviewed selection quietly', () => {
	const fingerprint = updateCandidateFingerprint(available.updatePreview);
	const prune = pruneUpdateSelections(
		[withPreview({ state: 'unavailable' })],
		['mod-1'],
		{ 'mod-1': fingerprint },
		true
	);
	assert.deepEqual(prune.kept, ['mod-1']);
	assert.deepEqual(prune.changed, []);
	assert.deepEqual(prune.missing, []);
	assert.deepEqual(prune.fingerprints, { 'mod-1': fingerprint });
});

test('a reload without previews keeps reviewed candidates and drops only deleted mods', () => {
	const fingerprint = updateCandidateFingerprint(available.updatePreview);
	// listMods without checkUpdates returns rows without previews.
	const plain: ModLike = { id: 'mod-1', fileName: 'mod.jar' };
	const prune = pruneUpdateSelections(
		[plain],
		['mod-1', 'mod-deleted'],
		{ 'mod-1': fingerprint, 'mod-deleted': fingerprint },
		false
	);
	assert.deepEqual(prune.kept, ['mod-1']);
	assert.deepEqual(prune.changed, []);
	assert.deepEqual(prune.missing, ['mod-deleted']);
	assert.deepEqual(prune.fingerprints, { 'mod-1': fingerprint });
});

test('each preview state has the intended presentation treatment', () => {
	const classes = ['available', 'current', 'unknown_source', 'unavailable', 'review_required'].map(
		updateStateClass
	);
	assert.equal(new Set(classes).size, 4, 'review_required shares the neutral treatment');
	assert.match(classes[0], /text-success-text/);
	assert.match(classes[2], /text-destructive-text/);
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
