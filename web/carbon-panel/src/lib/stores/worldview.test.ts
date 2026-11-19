// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { get } from 'svelte/store';
import { create } from '@bufbuild/protobuf';
import {
	WorldRefSchema,
	WorldRef_Kind,
	WorldViewTargetSchema,
	RendererState,
	type WorldViewTarget
} from '$lib/proto/carbonpanel/v1/worldview_pb';
import { createWorldViewStore } from './worldview.svelte';

const ref = create(WorldRefSchema, {
	kind: WorldRef_Kind.SERVER_LOCAL,
	serverId: 'srv-1',
	levelName: 'world'
});
const target = (state: RendererState) =>
	create(WorldViewTargetSchema, { serverId: 'srv-1', worldRef: ref, rendererState: state });

function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (error: Error) => void;
	const promise = new Promise<T>((yes, no) => {
		resolve = yes;
		reject = no;
	});
	return { promise, resolve, reject };
}

test('a late target list cannot overwrite a newer one or clobber a new session', async () => {
	const lists = [deferred<WorldViewTarget[]>(), deferred<WorldViewTarget[]>()];
	let call = 0;
	const store = createWorldViewStore({
		listTargets() {
			return lists[call++].promise;
		},
		status: async () => undefined
	});
	const old = store.refreshTargets();
	assert.equal(store.refreshTargets(), old, 'in-flight lists are shared');
	store.reset();
	const fresh = store.refreshTargets();
	lists[1].resolve([target(RendererState.READY)]);
	await fresh;
	lists[0].resolve([target(RendererState.ERROR)]);
	await old;
	assert.equal(get(store).targets[0].rendererState, RendererState.READY);
	assert.equal(get(store).hydrated, true);
	assert.equal(get(store).loading, false);
});

test('a failed refresh keeps the last known targets so the route can show them as stale', async () => {
	const lists = [deferred<WorldViewTarget[]>(), deferred<WorldViewTarget[]>()];
	let call = 0;
	const store = createWorldViewStore({
		listTargets() {
			return lists[call++].promise;
		},
		status: async () => undefined
	});
	lists[0].resolve([target(RendererState.READY)]);
	await store.refreshTargets();
	assert.equal(get(store).targets.length, 1);

	const failed = store.refreshTargets();
	const reason = new Error('refresh unavailable');
	lists[1].reject(reason);
	await assert.rejects(failed, reason);
	assert.equal(get(store).targets.length, 1, 'last-good rows are not cleared by a failed refresh');
	assert.equal(get(store).error, reason);
	assert.equal(get(store).hydrated, true);
});

test('status errors are isolated per key and reset hides stale data', async () => {
	const status = deferred<WorldViewTarget | undefined>();
	const store = createWorldViewStore({
		listTargets: async () => [],
		status() {
			return status.promise;
		}
	});
	const pending = store.loadStatus('srv-1:key', 'srv-1', ref);
	status.resolve(target(RendererState.STALE));
	await pending;
	assert.equal(get(store).statuses['srv-1:key']?.rendererState, RendererState.STALE);
	store.reset();
	assert.deepEqual(get(store).statuses, {});
	assert.deepEqual(get(store).statusErrors, {});
});

test('target watchers are refcounted and stop on the last release', () => {
	const timers = new Map<number, () => void>();
	let next = 0;
	let calls = 0;
	const store = createWorldViewStore(
		{
			listTargets: async () => {
				calls++;
				return [];
			},
			status: async () => undefined
		},
		{
			setInterval: ((cb: () => void) => {
				timers.set(++next, cb);
				return next;
			}) as unknown as typeof setInterval,
			clearInterval: ((id: number) => {
				timers.delete(id);
			}) as unknown as typeof clearInterval
		}
	);
	const a = store.watchTargets();
	const b = store.watchTargets();
	assert.equal(timers.size, 1);
	a();
	a();
	assert.equal(timers.size, 1);
	b();
	assert.equal(timers.size, 0);
	assert.equal(calls, 0);
});

test('status watchers are refcounted per world key', () => {
	const timers = new Map<string, () => void>();
	const store = createWorldViewStore(
		{ listTargets: async () => [], status: async () => undefined },
		{
			setInterval: ((cb: () => void) => {
				const id = String(timers.size + 1);
				timers.set(id, cb);
				return id as unknown as ReturnType<typeof setInterval>;
			}) as unknown as typeof setInterval,
			clearInterval: ((id: unknown) => {
				timers.delete(String(id));
			}) as unknown as typeof clearInterval
		}
	);
	const closeA = store.watchStatus('key-a', 'srv-1', ref);
	const closeB = store.watchStatus('key-a', 'srv-1', ref);
	const closeOther = store.watchStatus('key-b', 'srv-1', ref);
	assert.equal(timers.size, 2);
	closeA();
	assert.equal(timers.size, 2);
	closeB();
	assert.equal(timers.size, 1);
	closeOther();
	assert.equal(timers.size, 0);
});
