// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createServerActions } from './actions';
import { serverStatusLabel, serverStatusTone } from './presentation';
import { ServerStatus } from '../../proto/carbonpanel/v1/common_pb';

for (const action of [
	'start',
	'stop',
	'restart',
	'recreate',
	'delete',
	'keep',
	'detach',
	'export'
] as const) {
	test(`shared ${action} action preserves payload and disposition`, async () => {
		const calls: { name: string; payload: unknown }[] = [];
		const removed: string[] = [];
		const invalidated: string[] = [];
		let release!: () => void;
		const wait = new Promise<void>((resolve) => {
			release = resolve;
		});
		const rpc = (name: string) => async (payload: unknown) => {
			calls.push({ name, payload });
			await wait;
		};
		const actions = createServerActions(
			{
				startServer: rpc('start'),
				stopServer: rpc('stop'),
				restartServer: rpc('restart'),
				recreateServer: rpc('recreate'),
				deleteServer: rpc('delete'),
				exportWorld: async (payload) => {
					calls.push({ name: 'export', payload });
					await wait;
					return { sessionId: 'session', filename: 'world.zip' };
				}
			},
			{
				generation: () => 0,
				invalidate: (id) => {
					invalidated.push(id);
				},
				remove: (id) => {
					removed.push(id);
				},
				load: async () => undefined
			}
		);
		const deleting = ['delete', 'keep', 'detach', 'export'].includes(action);
		const first = deleting
			? actions.perform('server', 'delete', action as 'delete' | 'keep' | 'detach' | 'export')
			: actions.perform('server', action as 'start' | 'stop' | 'restart' | 'recreate');
		const second = deleting
			? actions.perform('server', 'delete', action as 'delete' | 'keep' | 'detach' | 'export')
			: actions.perform('server', action as 'start' | 'stop' | 'restart' | 'recreate');
		assert.equal(first, second);
		assert.equal(calls.length, 1);
		const payload = calls[0].payload as {
			id?: string;
			requestId?: string;
			keepData?: boolean;
			detach?: boolean;
			serverId?: string;
		};
		if (deleting && action !== 'export') {
			assert.equal(payload.keepData, action === 'keep');
			assert.equal(payload.detach, action === 'detach');
		}
		if (action === 'export') {
			assert.deepEqual(payload, { serverId: 'server' });
			assert.deepEqual(invalidated, []);
		} else {
			assert.equal(payload.id, 'server');
			if (action === 'recreate') assert.deepEqual(payload, { id: 'server' });
			else assert.ok(payload.requestId);
		}

		release();
		const outcome = await first;
		assert.equal(outcome.kind, action === 'export' ? 'exported' : deleting ? 'removed' : 'updated');
		assert.deepEqual(removed, deleting && action !== 'export' ? ['server'] : []);
		if (action === 'export') assert.deepEqual(invalidated, []);
	});
}

test('failed lifecycle attempts release idempotency and do not delete inventory', async () => {
	const ids: string[] = [];
	let attempts = 0;
	const fail = async (request: { requestId: string }) => {
		ids.push(request.requestId);
		if (++attempts === 1) throw new Error('offline');
	};
	const actions = createServerActions(
		{
			startServer: fail,
			stopServer: async () => {},
			restartServer: async () => {},
			recreateServer: async () => {},
			deleteServer: async () => {},
			exportWorld: async () => ({ sessionId: 's', filename: 'f' })
		},
		{
			generation: () => 0,
			invalidate: () => {},
			remove: () => assert.fail('failed start deleted server'),
			load: async () => undefined
		}
	);
	await assert.rejects(actions.perform('a', 'start'), /offline/);
	await actions.perform('a', 'start');
	assert.notEqual(ids[0], ids[1]);
});

test('error and unhealthy retain different severity and labels', () => {
	assert.equal(serverStatusTone(ServerStatus.ERROR), 'red');
	assert.equal(serverStatusTone(ServerStatus.UNHEALTHY), 'yellow');
	assert.equal(serverStatusLabel(ServerStatus.ERROR), 'ERROR');
	assert.equal(serverStatusLabel(ServerStatus.UNHEALTHY), 'UNHEALTHY');
	assert.equal(serverStatusLabel(ServerStatus.DEEPSLEEP), 'DEEP SLEEP');
});

test('account reset isolates pending action results and new idempotency keys', async () => {
	let generation = 0;
	const releases: (() => void)[] = [];
	const ids: string[] = [];
	let removals = 0;
	const actions = createServerActions(
		{
			startServer: async () => {},
			stopServer: async () => {},
			restartServer: async () => {},
			recreateServer: async () => {},
			deleteServer: async (request) => {
				ids.push(request.requestId);
				await new Promise<void>((resolve) => releases.push(resolve));
			},
			exportWorld: async () => ({ sessionId: 's', filename: 'f' })
		},
		{
			generation: () => generation,
			invalidate: () => {},
			remove: () => {
				removals++;
			},
			load: async () => undefined
		}
	);
	const old = actions.perform('same-server', 'delete');
	generation++;
	const fresh = actions.perform('same-server', 'delete');
	assert.notEqual(old, fresh);
	assert.notEqual(ids[0], ids[1]);
	releases[0]();
	assert.equal((await old).kind, 'superseded');
	assert.equal(removals, 0);
	releases[1]();
	assert.equal((await fresh).kind, 'removed');
	assert.equal(removals, 1);
});

for (const reset of [false, true]) {
	test(`export failure obeys account generation, reset=${reset}`, async () => {
		let generation = 0;
		let reject!: (error: Error) => void;
		const actions = createServerActions(
			{
				startServer: async () => {},
				stopServer: async () => {},
				restartServer: async () => {},
				recreateServer: async () => {},
				deleteServer: async () => assert.fail('export deleted'),
				exportWorld: () =>
					new Promise((_yes, no) => {
						reject = no;
					})
			},
			{
				generation: () => generation,
				invalidate: () => assert.fail('export invalidated'),
				remove: () => assert.fail('export removed'),
				load: async () => undefined
			}
		);
		const attempt = actions.perform('a', 'delete', 'export');
		if (reset) generation++;
		reject(new Error('export offline'));
		if (reset) assert.equal((await attempt).kind, 'superseded');
		else await assert.rejects(attempt, /export offline/);
	});
}
