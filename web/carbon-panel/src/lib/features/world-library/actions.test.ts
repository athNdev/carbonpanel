// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import {
	BindWorldResponseSchema,
	ConfigDraftPreviewSchema,
	WorldConfigSchemaSchema,
	WorldEntrySchema,
	WorldImportJobSchema
} from '../../proto/carbonpanel/v1/world_pb';
import type { WorldLibraryActionRequests } from './actions';
import { createWorldLibraryActions } from './actions';

function fixture(bindOutcome = 'applied') {
	let generation = 0;
	const invalidated: string[] = [];
	const calls = {
		bind: 0,
		resolve: 0,
		compat: 0,
		import: 0,
		preview: 0,
		create: 0,
		schema: 0
	};
	const requests: WorldLibraryActionRequests = {
		async importWorld(_input) {
			calls.import++;
			return {
				job: create(WorldImportJobSchema, {
					id: 'job-1',
					worldEntryId: 'wld-1',
					operator: 'tester'
				}),
				revision: undefined
			};
		},
		async checkCompatibility() {
			calls.compat++;
			return undefined;
		},
		async bindWorld() {
			calls.bind++;
			return create(BindWorldResponseSchema, {
				operationId: 'op-1',
				outcome: bindOutcome,
				consistency: 'verified stopped'
			});
		},
		async unbindWorld() {
			return { removed: true };
		},
		async resolveWorldOperation() {
			calls.resolve++;
			return { outcome: 'recovered' };
		},
		async getWorldConfigSchema() {
			calls.schema++;
			return create(WorldConfigSchemaSchema, { edition: 'java' });
		},
		async previewWorldConfig() {
			calls.preview++;
			return create(ConfigDraftPreviewSchema, { summary: 'no changes' });
		},
		async createWorldFromConfig() {
			calls.create++;
			return { entry: create(WorldEntrySchema, { id: 'wld-created' }) };
		},
		async searchWorldArtifacts() {
			return { artifacts: [], nextCursor: '', rateLimited: false };
		},
		async getProviderCapabilities() {
			return [];
		},
		async getRevision() {
			return undefined;
		},
		async listRevisions() {
			return [];
		},
		async listBindings() {
			return [];
		},
		async listImportJobs() {
			return [];
		}
	};
	const actions = createWorldLibraryActions(requests, {
		generation: () => generation,
		invalidate: (id) => invalidated.push(id)
	});
	return {
		actions,
		calls,
		invalidated,
		bumpGeneration: () => generation++
	};
}

test('bind is deduplicated by request id and a stale generation is superseded', async () => {
	const { actions, calls, bumpGeneration } = fixture();
	const input = {
		serverId: 'srv-1',
		levelName: 'world',
		revisionId: 'rev-1',
		window: 'now' as const,
		requestId: 'bind-1'
	};
	const first = actions.bindWorld(input);
	assert.equal(actions.bindWorld(input), first);
	bumpGeneration();
	const outcome = await first;
	assert.deepEqual(outcome, { kind: 'superseded' });
	assert.equal(calls.bind, 1);
});

test('compatibility dedup includes the target level so a level change gets a fresh check', async () => {
	const { actions, calls } = fixture();
	const target = { revisionId: 'rev-1', serverId: 'srv-1', targetMcVersion: '1.21.1' };
	const first = actions.checkCompatibility({ ...target, levelName: 'world' });
	assert.equal(actions.checkCompatibility({ ...target, levelName: 'world' }), first);
	assert.notEqual(actions.checkCompatibility({ ...target, levelName: 'survival' }), first);
	await Promise.all([first, actions.checkCompatibility({ ...target, levelName: 'survival' })]);
	assert.equal(calls.compat, 2);
});

test('an uncertain bind outcome is reported and never auto-retried', async () => {
	const { actions, calls } = fixture('unknown');
	const outcome = await actions.bindWorld({
		serverId: 'srv-1',
		levelName: 'world',
		revisionId: 'rev-1',
		window: 'now',
		requestId: 'bind-unknown'
	});
	assert.equal(outcome.kind, 'bound');
	if (outcome.kind === 'bound') {
		assert.equal(outcome.value.outcome, 'unknown');
		assert.ok(outcome.value.operationId);
	}
	assert.equal(calls.resolve, 0);
	const resolved = await actions.resolveOperation({
		serverId: 'srv-1',
		operationId: 'op-1',
		action: 'recover',
		note: 'observed stopped'
	});
	assert.equal(resolved.kind, 'resolved');
	assert.equal(calls.resolve, 1);
});

test('import keeps its idempotency key and invalidates the resulting entry', async () => {
	const { actions, calls, invalidated } = fixture();
	const outcome = await actions.importWorld({
		source: 'url',
		url: 'https://example.invalid/world.zip',
		displayName: 'World',
		requestId: 'import-1'
	});
	assert.equal(outcome.kind, 'imported');
	assert.equal(calls.import, 1);
	assert.deepEqual(invalidated, ['wld-1']);
});

test('schema, preview and create are separate typed steps; create invalidates the new entry', async () => {
	const { actions, calls, invalidated } = fixture();
	const target = { edition: 'java', mcVersion: '1.21.1', modLoader: 1, dataVersion: '3953' };
	const schema = actions.getWorldConfigSchema(target);
	assert.equal(actions.getWorldConfigSchema(target), schema);
	assert.equal((await schema).kind, 'schema');
	const preview = await actions.previewWorldConfig({ ...target, values: { level_seed: '42' } });
	assert.equal(preview.kind, 'previewed');
	const created = await actions.createWorld({
		...target,
		name: 'Fresh',
		values: { level_seed: '42' },
		requestId: 'create-1'
	});
	assert.equal(created.kind, 'created');
	assert.equal(calls.schema, 1);
	assert.equal(calls.preview, 1);
	assert.equal(calls.create, 1);
	assert.deepEqual(invalidated, ['wld-created']);
});
