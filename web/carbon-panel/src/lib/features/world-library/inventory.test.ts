// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { get } from 'svelte/store';
import { create } from '@bufbuild/protobuf';
import { WorldEntrySchema } from '../../proto/carbonpanel/v1/world_pb';
import type { WorldEntry } from '../../proto/carbonpanel/v1/world_pb';
import { createWorldLibraryInventory } from './inventory.svelte';

function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (error: Error) => void;
	const promise = new Promise<T>((yes, no) => {
		resolve = yes;
		reject = no;
	});
	return { promise, resolve, reject };
}

const entry = (id: string, name = id) => create(WorldEntrySchema, { id, name, revisionCount: 1 });

function fixture() {
	const lists: ReturnType<typeof deferred<WorldEntry[]>>[] = [];
	const details: ReturnType<typeof deferred<WorldEntry | undefined>>[] = [];
	const inventory = createWorldLibraryInventory({
		list() {
			const request = deferred<WorldEntry[]>();
			lists.push(request);
			return request.promise;
		},
		detail() {
			const request = deferred<WorldEntry | undefined>();
			details.push(request);
			return request.promise;
		}
	});
	return { inventory, lists, details };
}

test('deduplicates in-flight reads; after invalidation a newer list wins over an older one', async () => {
	const { inventory, lists } = fixture();
	assert.equal(inventory.refresh(), inventory.refresh());
	const older = inventory.refresh();
	// A mutation deletes the dedup entry, so a post-mutation read can overlap.
	inventory.invalidate('a');
	const recent = inventory.refresh();
	assert.notEqual(recent, older);
	lists[1].resolve([entry('new')]);
	await recent;
	lists[0].resolve([entry('old')]);
	await older;
	assert.deepEqual(
		get(inventory).map((candidate) => candidate.id),
		['new']
	);
	assert.equal(get(inventory.state).hydrated, true);
	assert.equal(get(inventory.state).loading, false);
});

test('late lists cannot resurrect a removal or replace a newer detail', async () => {
	const { inventory, lists, details } = fixture();
	inventory.upsert(entry('removed'));
	const list = inventory.refresh();
	const detail = inventory.load('rich');
	inventory.remove('removed');
	details[0].resolve(entry('rich', 'rich detail'));
	await detail;
	lists[0].resolve([entry('removed'), entry('rich', 'stale')]);
	await list;
	assert.deepEqual(
		get(inventory).map((candidate) => candidate.id),
		['rich']
	);
	assert.equal(inventory.get('rich')?.name, 'rich detail');
	const later = inventory.refresh();
	lists[1].resolve([entry('removed'), entry('rich', 'newer')]);
	await later;
	assert.equal(inventory.get('removed'), undefined);
});

test('invalidate forces a post-mutation read instead of reusing an older detail', async () => {
	const { inventory, details } = fixture();
	inventory.upsert(entry('a', 'before'));
	const old = inventory.load('a');
	inventory.invalidate('a');
	const after = inventory.load('a');
	assert.notEqual(after, old);
	details[1].resolve(entry('a', 'after'));
	await after;
	details[0].resolve(entry('a', 'stale'));
	await old;
	assert.equal(inventory.get('a')?.name, 'after');
});

test('list failures set the error and hydrated state once', async () => {
	const { inventory, lists } = fixture();
	const request = inventory.refresh();
	const failure = new Error('rpc down');
	lists[0].reject(failure);
	await assert.rejects(request, /rpc down/);
	assert.equal(get(inventory.state).error, failure);
	assert.equal(get(inventory.state).hydrated, true);
	assert.equal(get(inventory.state).loading, false);
});

test('watch is refcounted: one timer per key, closed only by the last user', () => {
	const scheduled: Array<{ interval: number }> = [];
	const cleared: ReturnType<typeof setInterval>[] = [];
	const inventory = createWorldLibraryInventory(
		{
			async list() {
				return [];
			},
			async detail() {
				return undefined;
			}
		},
		{
			setInterval: ((_fn: () => void, interval?: number) => {
				scheduled.push({ interval: interval ?? 0 });
				return 1 as unknown as ReturnType<typeof setInterval>;
			}) as unknown as typeof globalThis.setInterval,
			clearInterval: ((id: ReturnType<typeof setInterval>) => {
				cleared.push(id);
			}) as unknown as typeof globalThis.clearInterval
		}
	);
	const first = inventory.watch({ interval: 1000 });
	const second = inventory.watch({ interval: 1000 });
	assert.equal(scheduled.length, 1);
	first();
	assert.equal(cleared.length, 0);
	second();
	assert.equal(cleared.length, 1);
	inventory.watch({ id: 'a', interval: 1000 })();
	assert.equal(scheduled.length, 2);
	assert.equal(cleared.length, 2);
	inventory.reset();
	assert.equal(inventory.generation(), 1);
});
