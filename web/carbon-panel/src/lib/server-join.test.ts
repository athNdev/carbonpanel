// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import { joinIsPlayable, joinKindLabel } from './server-join';

test('joinIsPlayable is only true for a fresh successful join', () => {
	assert.equal(joinIsPlayable({ joinObservationKind: 'playable' }), true);
	assert.equal(joinIsPlayable({ joinObservationKind: 'unobserved' }), false);
	assert.equal(joinIsPlayable({ joinObservationKind: 'stale' }), false);
	assert.equal(joinIsPlayable({ joinObservationKind: 'unjoinable' }), false);
	assert.equal(joinIsPlayable({ joinObservationKind: '' }), false);
});

test('joinKindLabel keeps stale and unobserved distinct from running success', () => {
	assert.equal(joinKindLabel('stale'), 'stale');
	assert.equal(joinKindLabel('unobserved'), 'unobserved');
	assert.equal(joinKindLabel('unjoinable'), 'unjoinable');
	assert.equal(joinKindLabel('playable'), 'playable');
});
