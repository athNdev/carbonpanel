// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import { parseCapacity } from './node-capacity';

test('parseCapacity keeps string semantics for valid values', () => {
	assert.equal(parseCapacity('16'), 16);
	assert.equal(parseCapacity('0'), 0);
	assert.equal(parseCapacity('4096'), 4096);
	assert.equal(parseCapacity(' 16 '), 16);
});

test('parseCapacity treats blank strings as unlimited (0)', () => {
	assert.equal(parseCapacity(''), 0);
	assert.equal(parseCapacity('   '), 0);
});

test('parseCapacity rejects invalid string values', () => {
	assert.equal(parseCapacity('-1'), null);
	assert.equal(parseCapacity('1.5'), null);
	assert.equal(parseCapacity('1e3'), null);
	assert.equal(parseCapacity('abc'), null);
	assert.equal(parseCapacity('16MB'), null);
	assert.equal(parseCapacity('9007199254740992'), null);
});

test('parseCapacity accepts numbers from number-input bindings without throwing', () => {
	// Regression: `type="number"` + `bind:value` assigns a number at runtime,
	// which used to crash with `raw.trim is not a function`.
	assert.equal(parseCapacity(16), 16);
	assert.equal(parseCapacity(0), 0);
	assert.equal(parseCapacity(1024), 1024);
});

test('parseCapacity rejects invalid numeric values', () => {
	assert.equal(parseCapacity(-1), null);
	assert.equal(parseCapacity(1.5), null);
	assert.equal(parseCapacity(NaN), null);
	assert.equal(parseCapacity(Infinity), null);
	assert.equal(parseCapacity(-Infinity), null);
	assert.equal(parseCapacity(Number.MAX_SAFE_INTEGER + 1), null);
});

test('parseCapacity treats null and undefined as unlimited (0)', () => {
	// A cleared number input binds to undefined rather than an empty string.
	assert.equal(parseCapacity(null), 0);
	assert.equal(parseCapacity(undefined), 0);
});

test('parseCapacity rejects unexpected runtime types', () => {
	assert.equal(parseCapacity(true), null);
	assert.equal(parseCapacity(false), null);
	assert.equal(parseCapacity({}), null);
	assert.equal(parseCapacity([]), null);
});
