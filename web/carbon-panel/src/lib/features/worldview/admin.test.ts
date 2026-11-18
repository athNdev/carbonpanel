// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { adminActions, buildAdminRequest } from './admin';

test('admin actions are permission-gated with explicit consequences', () => {
	const denied = adminActions(false);
	assert.equal(denied.length, 3);
	assert.ok(denied.every((action) => !action.allowed));
	assert.ok(denied.every((action) => action.consequence.length > 0));
	assert.match(denied[0].deniedReason, /servers\/command/);
	const allowed = adminActions(true);
	assert.ok(allowed.every((action) => action.allowed));
});

test('a denied action builds no request regardless of map state', () => {
	const denied = adminActions(false)[0];
	assert.equal(buildAdminRequest(denied, 'srv-1', { x: 1, z: 2 }), null);
});

test('allowed actions reuse SendCommand with honest commands', () => {
	const [teleport, save, pause] = adminActions(true);
	assert.deepEqual(buildAdminRequest(teleport, 'srv-1', { x: 1.5, z: -2 }), {
		serverId: 'srv-1',
		command: '/execute at @s run tp @s 1.5 ~ -2'
	});
	assert.equal(buildAdminRequest(teleport, 'srv-1', null), null);
	assert.equal(buildAdminRequest(save, 'srv-1', null)?.command, 'save-all flush');
	assert.equal(buildAdminRequest(pause, 'srv-1', null)?.command, 'save-off');
});
