// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import { rebaselineConfigAfterSave, type ConfigFormState } from './server-configuration-save';

/**
 * State as it stands after `processConfig(response.categories)` has replaced
 * the loaded snapshot, with `inFlightValues` taken from the live form just
 * before that refresh ran.
 */
function afterResponse(response: Record<string, string | null>): ConfigFormState {
	return {
		originalValues: new Map(Object.entries(response)),
		originalEnabled: new Set(Object.keys(response)),
		currentValues: new Map(Object.entries(response)),
		currentEnabled: new Set(Object.keys(response))
	};
}

test('an edit to a submitted field during the request survives the response refresh', () => {
	// The user sent view-distance=12, then typed 16 while the request ran.
	const state = afterResponse({ 'view-distance': '12', motd: 'hello' });
	const inFlightValues = new Map<string, string | null>([
		['view-distance', '16'],
		['motd', 'hello']
	]);
	const inFlightEnabled = new Set(['view-distance', 'motd']);

	const next = rebaselineConfigAfterSave(
		state,
		{ 'view-distance': '12' },
		inFlightValues,
		inFlightEnabled
	);

	assert.equal(
		next.originalValues.get('view-distance'),
		'12',
		'the baseline moves to the value that was actually sent'
	);
	assert.equal(
		next.currentValues.get('view-distance'),
		'16',
		'the edit typed during the request is still in the form'
	);
	assert.equal(
		next.originalValues.get('motd'),
		next.currentValues.get('motd'),
		'an untouched field stays clean'
	);
	assert.notEqual(next.originalValues.get('view-distance'), next.currentValues.get('view-distance'));
});

test('an edit to a previously clean field during the request survives the response refresh', () => {
	// motd was clean when Save started, so it is not part of the sent batch.
	const state = afterResponse({ 'view-distance': '12', motd: 'hello' });
	const inFlightValues = new Map<string, string | null>([
		['view-distance', '12'],
		['motd', 'edited during save']
	]);
	const inFlightEnabled = new Set(['view-distance', 'motd']);

	const next = rebaselineConfigAfterSave(
		state,
		{ 'view-distance': '12' },
		inFlightValues,
		inFlightEnabled
	);

	assert.equal(next.originalValues.get('motd'), 'hello', 'the response value is the baseline');
	assert.equal(next.currentValues.get('motd'), 'edited during save');
	assert.notEqual(next.originalValues.get('motd'), next.currentValues.get('motd'));
});

test('a submitted field left untouched becomes clean', () => {
	const state = afterResponse({ 'view-distance': '12' });
	const inFlightValues = new Map<string, string | null>([['view-distance', '12']]);
	const inFlightEnabled = new Set(['view-distance']);

	const next = rebaselineConfigAfterSave(
		state,
		{ 'view-distance': '12' },
		inFlightValues,
		inFlightEnabled
	);

	assert.equal(next.originalValues.get('view-distance'), '12');
	assert.equal(next.currentValues.get('view-distance'), '12');
});
