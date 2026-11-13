// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { get } from 'svelte/store';
import { create } from '@bufbuild/protobuf';
import { ServerSchema, ServerStatus } from '../../proto/carbonpanel/v1/common_pb';
import type { Server } from '../../proto/carbonpanel/v1/common_pb';
import { createServerInventory } from './inventory.svelte';

function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (error: Error) => void;
	const promise = new Promise<T>((yes, no) => {
		resolve = yes;
		reject = no;
	});
	return { promise, resolve, reject };
}
const server = (id: string, name = id) =>
	create(ServerSchema, { id, name, status: ServerStatus.RUNNING });
function fixture() {
	const lists: ReturnType<typeof deferred<Server[]>>[] = [];
	const details: ReturnType<typeof deferred<Server | undefined>>[] = [];
	const inventory = createServerInventory({
		list() {
			const r = deferred<Server[]>();
			lists.push(r);
			return r.promise;
		},
		detail() {
			const r = deferred<Server | undefined>();
			details.push(r);
			return r.promise;
		}
	});
	return { inventory, lists, details };
}

test('deduplicates matching reads and rejects an older summary after full inventory', async () => {
	const { inventory, lists } = fixture();
	const old = inventory.refresh();
	assert.equal(inventory.refresh(), old);
	const recent = inventory.refresh(true);
	lists[1].resolve([server('new')]);
	await recent;
	lists[0].resolve([server('old')]);
	await old;
	assert.deepEqual(
		get(inventory).map((s) => s.id),
		['new']
	);
	assert.equal(get(inventory.state).hydrated, true);
	assert.equal(get(inventory.state).loading, false);
});

test('late lists cannot resurrect deletion or replace a newer detail observation', async () => {
	const { inventory, lists, details } = fixture();
	inventory.upsert(server('deleted'));
	const list = inventory.refresh();
	const detail = inventory.load('rich');
	inventory.remove('deleted');
	details[0].resolve(
		create(ServerSchema, {
			id: 'rich',
			memoryUsage: 123n,
			slpAvailable: true,
			playerSample: ['Alice']
		})
	);
	await detail;
	lists[0].resolve([server('deleted'), server('rich')]);
	await list;
	assert.deepEqual(
		get(inventory).map((s) => s.id),
		['rich']
	);
	assert.equal(inventory.get('rich')?.memoryUsage, 123n);
	assert.deepEqual(inventory.get('rich')?.playerSample, ['Alice']);
	const later = inventory.refresh();
	lists[1].resolve([server('deleted'), server('rich')]);
	await later;
	assert.equal(inventory.get('deleted'), undefined);
});

test('a newer complete list may clear stale playable fields and zero metrics', async () => {
	const { inventory, lists } = fixture();
	inventory.upsert(
		create(ServerSchema, {
			id: 'a',
			memoryUsage: 123n,
			slpAvailable: true,
			playersOnline: 1,
			playerSample: ['Alice'],
			motd: 'old'
		})
	);
	const request = inventory.refresh();
	lists[0].resolve([create(ServerSchema, { id: 'a', joinObservationKind: 'stale' })]);
	await request;
	const current = inventory.get('a');
	assert.equal(current?.memoryUsage, 0n);
	assert.equal(current?.slpAvailable, false);
	assert.equal(current?.playersOnline, 0);
	assert.deepEqual(current?.playerSample, []);
	assert.equal(current?.motd, '');
});

test('mutation invalidates older detail and a post-action read does not reuse it', async () => {
	const { inventory, details } = fixture();
	inventory.upsert(server('a', 'before'));
	const old = inventory.load('a');
	inventory.invalidate('a');
	const after = inventory.load('a');
	assert.notEqual(after, old);
	details[1].resolve(server('a', 'after'));
	await after;
	details[0].resolve(server('a', 'stale'));
	await old;
	assert.equal(inventory.get('a')?.name, 'after');
});

test('reset blocks late account data and errors; a fresh empty result hydrates', async () => {
	const { inventory, lists } = fixture();
	const old = inventory.refresh();
	inventory.reset();
	const fresh = inventory.refresh();
	lists[1].resolve([]);
	await fresh;
	lists[0].resolve([server('private')]);
	await old;
	assert.deepEqual(get(inventory), []);
	assert.equal(get(inventory.state).hydrated, true);
	assert.equal(get(inventory.state).loading, false);
	const stale = inventory.refresh();
	inventory.reset();
	lists[2].reject(new Error('old account'));
	await assert.rejects(stale);
	assert.equal(get(inventory.state).error, null);
	assert.equal(get(inventory.state).hydrated, false);
});

test('an older failure cannot replace a newer successful hydration', async () => {
	const { inventory, lists } = fixture();
	const old = inventory.refresh();
	const fresh = inventory.refresh(true);
	lists[1].resolve([]);
	await fresh;
	lists[0].reject(new Error('stale'));
	await assert.rejects(old);
	assert.equal(get(inventory.state).error, null);
	const failed = inventory.refresh();
	lists[2].reject(new Error('offline'));
	await assert.rejects(failed);
	assert.equal(get(inventory.state).hydrated, true);
	assert.equal(get(inventory.state).loading, false);
	assert.match(String(get(inventory.state).error), /offline/);
});

test('refresh subscriptions share one timer and close on last unmount', () => {
	const callbacks = new Map<number, () => void>();
	let next = 0;
	let calls = 0;
	const inventory = createServerInventory(
		{
			list: async () => {
				calls++;
				return [];
			},
			detail: async () => undefined
		},
		{
			setInterval: ((cb: () => void) => {
				callbacks.set(++next, cb);
				return next;
			}) as unknown as typeof setInterval,
			clearInterval: ((id: number) => {
				callbacks.delete(id);
			}) as unknown as typeof clearInterval
		}
	);
	const closeA = inventory.watch(),
		closeB = inventory.watch();
	assert.equal(callbacks.size, 1);
	closeA();
	closeA();
	assert.equal(callbacks.size, 1);
	closeB();
	assert.equal(callbacks.size, 0);
	assert.equal(calls, 0);
});

for (const cached of [true, false]) {
	test(`authoritative list absence fences older detail, cached=${cached}`, async () => {
		const { inventory, lists, details } = fixture();
		if (cached) inventory.upsert(server('a'));
		const old = inventory.load('a');
		const list = inventory.refresh();
		lists[0].resolve([]);
		await list;
		details[0].resolve(server('a', 'stale'));
		await old;
		assert.equal(inventory.get('a'), undefined);
		const fresh = inventory.load('a');
		details[1].resolve(server('a', 'fresh'));
		await fresh;
		assert.equal(inventory.get('a')?.name, 'fresh');
	});
}
