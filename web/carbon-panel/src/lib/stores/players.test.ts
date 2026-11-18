// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { get } from 'svelte/store';
import { create } from '@bufbuild/protobuf';
import {
	ListPlayersResponseSchema,
	PlayerRefSchema,
	PlayerSummarySchema
} from '$lib/proto/carbonpanel/v1/player_pb';
import { DEFAULT_ROSTER_FILTER } from '$lib/features/players/presentation';
import { createPlayersStore } from '$lib/features/players/roster-store';

function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (error: Error) => void;
	const promise = new Promise<T>((yes, no) => {
		resolve = yes;
		reject = no;
	});
	return { promise, resolve, reject };
}

const response = (name: string) =>
	create(ListPlayersResponseSchema, {
		players: [
			create(PlayerSummarySchema, {
				ref: create(PlayerRefSchema, { identityKey: name, name }),
				serverId: 's'
			})
		],
		sourceDetail: 'presence + panel db'
	});

test('a slower older roster response cannot overwrite the newest one', async () => {
	const calls: ReturnType<typeof deferred<ReturnType<typeof response>>>[] = [];
	const store = createPlayersStore(() => {
		const call = deferred<ReturnType<typeof response>>();
		calls.push(call);
		return call.promise;
	});
	const old = store.load({ ...DEFAULT_ROSTER_FILTER, query: 'old' });
	const fresh = store.load({ ...DEFAULT_ROSTER_FILTER, query: 'new' });
	calls[1].resolve(response('new'));
	await fresh;
	calls[0].resolve(response('old'));
	await old;
	assert.deepEqual(
		get(store).players.map((p) => p.ref?.name),
		['new']
	);
	assert.equal(get(store).error, null);
});

test('a failed refresh marks existing rows stale instead of clearing them', async () => {
	const calls: ReturnType<typeof deferred<ReturnType<typeof response>>>[] = [];
	const store = createPlayersStore(() => {
		const call = deferred<ReturnType<typeof response>>();
		calls.push(call);
		return call.promise;
	});
	const first = store.load({ ...DEFAULT_ROSTER_FILTER });
	calls[0].resolve(response('kept'));
	await first;
	assert.equal(get(store).loaded, true);

	const second = store.load({ ...DEFAULT_ROSTER_FILTER, query: 'fresh' });
	calls[1].reject(new Error('offline'));
	await second;
	const state = get(store);
	assert.equal(state.players.length, 1, 'rows are retained');
	assert.equal(state.stale, true, 'the retained rows are marked stale');
	assert.match(state.error ?? '', /\S+/, 'an error is reported for the retry banner');
	assert.equal(state.loading, false);
});
