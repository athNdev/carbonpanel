// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { summarizeModUpdateCheck } from './mod-update-summary';

test('summary counts every preview state, not only available updates', () => {
	assert.equal(
		summarizeModUpdateCheck(['available', 'current', 'review_required', 'unknown_source']),
		'Checked 4 installed files: 1 update available, 1 current, 1 review required, 1 unknown source.'
	);
});

test('summary counts provider failures separately from results', () => {
	assert.equal(
		summarizeModUpdateCheck(['unavailable', 'unavailable']),
		'Checked 2 installed files: 2 unavailable.'
	);
});

test('summary handles a single file and an empty preview set', () => {
	assert.equal(summarizeModUpdateCheck(['current']), 'Checked 1 installed file: 1 current.');
	assert.equal(summarizeModUpdateCheck([]), 'Checked 0 installed files.');
});
