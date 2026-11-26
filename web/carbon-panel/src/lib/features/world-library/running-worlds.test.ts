// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import { ListBindingsResponseSchema } from '../../proto/carbonpanel/v1/world_pb';
import { runningWorld, readServerRows } from './running-worlds';

test('only the binding of the configured server and level identifies a managed world', () => {
	const response = create(ListBindingsResponseSchema, {
		configuredLevelName: 'actual',
		bindings: [
			{ serverId: 'other', levelName: 'actual', revisionId: 'other-revision' },
			{ serverId: 'server', levelName: 'retired', revisionId: 'old-revision' }
		]
	});
	assert.deepEqual(runningWorld('server', response), {
		state: 'local',
		level: 'actual',
		revision: ''
	});
	assert.equal(runningWorld('other', response).revision, 'other-revision');
	response.configuredLevelName = '';
	assert.equal(runningWorld('server', response).state, 'unknown');
});

test('server reads are bounded and failed rows remain unknown', async () => {
	let active = 0,
		peak = 0;
	const rows = new Map<string, string | undefined>();
	await readServerRows(
		Array.from({ length: 12 }, (_, i) => String(i)),
		new AbortController().signal,
		async (id) => {
			active++;
			peak = Math.max(active, peak);
			await new Promise((resolve) => setTimeout(resolve, 1));
			active--;
			if (id === '4') throw new Error('permission changed');
			return id;
		},
		(id, value) => rows.set(id, value)
	);
	assert.equal(peak, 4);
	assert.equal(rows.size, 12);
	assert.equal(rows.get('4'), undefined);
});
