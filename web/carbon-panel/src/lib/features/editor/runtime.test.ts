// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { cachedRuntime } from './runtime';

test('concurrent editor loads share initialization and failed loads can retry', async () => {
	let calls = 0;
	let resolve!: (value: object) => void;
	const load = cachedRuntime(() => {
		calls++;
		return new Promise<object>((done) => {
			resolve = done;
		});
	});
	const first = load();
	assert.equal(load(), first);
	const runtime = {};
	resolve(runtime);
	assert.equal(await first, runtime);
	assert.equal(await load(), runtime);
	assert.equal(calls, 1);
	let attempts = 0;
	const retry = cachedRuntime(async () => {
		if (++attempts === 1) throw new Error('unavailable');
		return runtime;
	});
	await assert.rejects(retry(), /unavailable/);
	assert.equal(await retry(), runtime);
	assert.equal(attempts, 2);
});
