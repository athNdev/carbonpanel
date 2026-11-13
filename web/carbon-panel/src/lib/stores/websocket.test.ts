// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { resolve } from 'node:path';

test('WebSocket generation ownership and subscription options', () => {
	// Isolate Bun's module and rune mocks from the rest of the frontend suite.
	const fixture = resolve(
		import.meta.dirname,
		'../../../../../scripts/manual-review/e2e/websocket-browser/socket.test.ts'
	);
	const result = spawnSync(process.execPath, ['test', fixture], {
		encoding: 'utf8',
		timeout: 30000
	});
	assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
});
