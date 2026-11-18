// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import { WorldRefSchema, WorldRef_Kind } from '$lib/proto/carbonpanel/v1/worldview_pb';
import { decodeWorldKey, encodeWorldKey, worldIdentity } from './world-ref';

test('world key round-trips every tagged field including non-Latin names', () => {
	const ref = create(WorldRefSchema, {
		kind: WorldRef_Kind.SERVER_LOCAL,
		serverId: 'srv-1',
		levelName: 'wörld — 生存',
		bindingId: '',
		snapshotId: ''
	});
	const key = encodeWorldKey(ref);
	assert.match(key, /^w1\./);
	assert.ok(!key.includes('/'));
	const decoded = decodeWorldKey(key);
	assert.equal(decoded?.kind, WorldRef_Kind.SERVER_LOCAL);
	assert.equal(decoded?.serverId, 'srv-1');
	assert.equal(decoded?.levelName, 'wörld — 生存');
});

test('library and snapshot identities stay distinct', () => {
	const library = create(WorldRefSchema, {
		kind: WorldRef_Kind.LIBRARY_BINDING,
		serverId: 'srv-1',
		bindingId: 'bind-9'
	});
	const snapshot = create(WorldRefSchema, {
		kind: WorldRef_Kind.STATIC_IMPORT,
		snapshotId: 'snap-9'
	});
	assert.equal(worldIdentity(library), 'library:bind-9');
	assert.equal(worldIdentity(snapshot), 'snapshot:snap-9');
	assert.notEqual(encodeWorldKey(library), encodeWorldKey(snapshot));
});

test('malformed or foreign keys degrade to null, never a plausible world', () => {
	assert.equal(decodeWorldKey(undefined), null);
	assert.equal(decodeWorldKey(''), null);
	assert.equal(decodeWorldKey('https://evil/w1.abc'), null);
	assert.equal(decodeWorldKey('w1.not-base64!!'), null);
	assert.equal(decodeWorldKey('w1.' + btoa('[9,"s"]')), null);
});
