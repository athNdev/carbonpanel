// SPDX-License-Identifier: MPL-2.0
// Regression coverage for explicit malformed persisted JSON handling: valid
// values are byte-for-byte equivalent to the legacy JSON.parse result, blank
// fields stay "absent", and non-blank invalid JSON is reported while retaining
// the historical empty-array interpretation.
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

	test('preserves a valid non-array JSON value exactly as legacy did', () => {
		assert.deepEqual(parsePersistedStringArray('{"a":1}'), {
			values: { a: 1 } as unknown as string[],
			malformed: false
		});
	});
});
