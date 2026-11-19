// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import { ConfigCategorySchema, ConfigPropertySchema } from '../../proto/carbonpanel/v1/config_pb';
import { WorldBindingSchema } from '../../proto/carbonpanel/v1/world_pb';
import {
	WorldRefSchema,
	WorldRef_Kind,
	WorldViewTargetSchema
} from '../../proto/carbonpanel/v1/worldview_pb';
import {
	configProperty,
	describeServerWorld,
	relevantWorldConfig,
	sameCompatibilityTuple,
	shouldAcceptCompatibility,
	serverLocalTargets,
	MANAGEMENT_PRESENTATION
} from './state';

const binding = (serverId = 'srv-1') =>
	create(WorldBindingSchema, {
		id: 'bind-1',
		serverId,
		levelName: 'world',
		revisionId: 'rev-1',
		nodeId: 'node-a',
		generation: 7n,
		consistency: 'verified stopped'
	});

const localTarget = (serverId = 'srv-1', levelName = 'survival') =>
	create(WorldViewTargetSchema, {
		serverId,
		worldRef: create(WorldRefSchema, {
			kind: WorldRef_Kind.SERVER_LOCAL,
			serverId,
			levelName
		})
	});

const categories = () => [
	create(ConfigCategorySchema, {
		name: 'World',
		properties: [
			create(ConfigPropertySchema, { key: 'level', value: 'configured-level' }),
			create(ConfigPropertySchema, { key: 'levelType', value: 'minecraft:flat' }),
			create(ConfigPropertySchema, { key: 'seed', value: '   ' })
		]
	})
];

test('a binding is the only authority for a managed world', () => {
	const result = describeServerWorld({
		serverId: 'srv-1',
		bindings: [binding()],
		targets: [localTarget()],
		configCategories: categories()
	});
	assert.equal(result.management, 'bound');
	assert.equal(result.levelName, 'world');
	assert.equal(result.binding?.id, 'bind-1');
	assert.equal(result.presentation.label, 'LIBRARY BOUND');
});

test('an observed server-local world with no binding is unmanaged, never bound', () => {
	const result = describeServerWorld({
		serverId: 'srv-1',
		bindings: [],
		targets: [localTarget('srv-1', 'survival')],
		configCategories: categories()
	});
	assert.equal(result.management, 'unmanaged');
	assert.equal(result.levelName, 'survival');
	assert.equal(result.binding, undefined);
	assert.match(result.presentation.hint ?? '', /not managed by the World Library/);
});

test('a configured level with no observation is honestly unbound', () => {
	const result = describeServerWorld({
		serverId: 'srv-1',
		bindings: [],
		targets: [],
		configCategories: categories()
	});
	assert.equal(result.management, 'unbound');
	assert.equal(result.levelName, 'configured-level');
});

test('no observation and no configured level yields none, not a guessed world', () => {
	const result = describeServerWorld({
		serverId: 'srv-1',
		bindings: undefined,
		targets: undefined,
		configCategories: undefined
	});
	assert.equal(result.management, 'none');
	assert.equal(result.levelName, undefined);
	assert.equal(result.presentation.label, 'NO WORLD DETECTED');
});

test('terminal states replace data-derived ones', () => {
	const base = { serverId: 'srv-1', bindings: [binding()], targets: [], configCategories: [] };
	assert.equal(describeServerWorld({ ...base, denied: true }).management, 'denied');
	assert.equal(describeServerWorld({ ...base, loading: true }).management, 'loading');
	assert.equal(describeServerWorld({ ...base, error: true }).management, 'error');
});

test('targets from another server are not attributed to this server', () => {
	assert.equal(serverLocalTargets([localTarget('srv-2'), localTarget('srv-1')], 'srv-1').length, 1);
	const result = describeServerWorld({
		serverId: 'srv-1',
		bindings: [],
		targets: [localTarget('srv-2')],
		configCategories: undefined
	});
	assert.equal(result.management, 'none');
});

test('config reads omit blank values and keep only relevant keys', () => {
	assert.equal(configProperty(categories(), 'seed'), undefined);
	assert.equal(configProperty(categories(), 'missing'), undefined);
	const rows = relevantWorldConfig(categories());
	assert.deepEqual(
		rows.map((row) => row.key),
		['level', 'levelType']
	);
	assert.equal(rows[1].value, 'minecraft:flat');
});

test('a compatibility report is accepted only for the exact captured tuple', () => {
	const a = {
		serverId: 'srv-1',
		revisionId: 'rev-a',
		targetMcVersion: '1.21.1',
		levelName: 'world'
	};
	const current = { ...a };
	assert.equal(sameCompatibilityTuple(a, current), true);
	assert.equal(sameCompatibilityTuple(a, undefined), false);
	assert.equal(sameCompatibilityTuple(a, { ...current, revisionId: 'rev-b' }), false);
	assert.equal(sameCompatibilityTuple(a, { ...current, serverId: 'srv-2' }), false);
	assert.equal(sameCompatibilityTuple(a, { ...current, targetMcVersion: '1.20.4' }), false);
	assert.equal(sameCompatibilityTuple(a, { ...current, levelName: 'other' }), false);

	assert.equal(
		shouldAcceptCompatibility({
			captured: a,
			current,
			navigationChanged: false,
			requestIsCurrent: true
		}),
		true
	);
	assert.equal(
		shouldAcceptCompatibility({
			captured: a,
			current: { ...current, revisionId: 'rev-b' },
			navigationChanged: false,
			requestIsCurrent: true
		}),
		false
	);
	assert.equal(
		shouldAcceptCompatibility({
			captured: a,
			current,
			navigationChanged: true,
			requestIsCurrent: true
		}),
		false
	);
	assert.equal(
		shouldAcceptCompatibility({
			captured: a,
			current,
			navigationChanged: false,
			requestIsCurrent: false
		}),
		false
	);
});

test('every management state carries a visible label and tone', () => {
	for (const presentation of Object.values(MANAGEMENT_PRESENTATION)) {
		assert.ok(presentation.label.length > 0);
		assert.ok(presentation.tone.length > 0);
		assert.ok(presentation.hint && presentation.hint.length > 0);
	}
});
