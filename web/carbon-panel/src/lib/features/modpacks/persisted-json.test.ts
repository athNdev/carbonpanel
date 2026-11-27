// SPDX-License-Identifier: MPL-2.0
// Stored catalogue metadata must remain safe to render even if its shape changed.
import { describe, test } from 'node:test';
import assert from 'node:assert/strict';

import { parsePersistedStringArray } from './persisted-json';

describe('indexed modpack persisted JSON', () => {
	test('returns valid arrays unchanged and not malformed', () => {
		assert.deepEqual(parsePersistedStringArray('["fabric","forge"]'), {
			values: ['fabric', 'forge'],
			malformed: false
		});
		assert.deepEqual(parsePersistedStringArray('[]'), { values: [], malformed: false });
	});

	test('blank fields are absent, not malformed', () => {
		assert.deepEqual(parsePersistedStringArray(''), { values: [], malformed: false });
		assert.deepEqual(parsePersistedStringArray('   '), { values: [], malformed: false });
		assert.deepEqual(parsePersistedStringArray(null), { values: [], malformed: false });
		assert.deepEqual(parsePersistedStringArray(undefined), { values: [], malformed: false });
	});

	test('reports malformed JSON but keeps the legacy empty fallback', () => {
		assert.deepEqual(parsePersistedStringArray('not json'), { values: [], malformed: true });
		assert.deepEqual(parsePersistedStringArray('["fabric"'), { values: [], malformed: true });
		assert.deepEqual(parsePersistedStringArray('{bad}'), { values: [], malformed: true });
	});

	test('reports parseable metadata with an invalid shape', () => {
		for (const raw of ['null', '{"a":1}', '"fabric"', '["fabric",42]']) {
			assert.deepEqual(parsePersistedStringArray(raw), { values: [], malformed: true });
		}
	});
});
