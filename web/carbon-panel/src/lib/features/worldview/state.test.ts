// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import { TimestampSchema } from '@bufbuild/protobuf/wkt';
import { WorldViewTargetSchema, RendererState } from '$lib/proto/carbonpanel/v1/worldview_pb';
import {
	canMountEmbed,
	deriveViewState,
	formatAge,
	liveOverlaysEnabled,
	timestampToMs
} from './state';

const target = (state: RendererState) => create(WorldViewTargetSchema, { rendererState: state });

test('every renderer state maps to exactly one honest UI state', () => {
	assert.equal(deriveViewState(undefined), 'loading');
	assert.equal(deriveViewState(target(RendererState.ABSENT)), 'onboarding');
	assert.equal(deriveViewState(target(RendererState.INSTALLING)), 'installing');
	assert.equal(deriveViewState(target(RendererState.RENDERING)), 'rendering');
	assert.equal(deriveViewState(target(RendererState.READY)), 'ready');
	assert.equal(deriveViewState(target(RendererState.STALE)), 'stale');
	assert.equal(deriveViewState(target(RendererState.STOPPED)), 'stopped');
	assert.equal(deriveViewState(target(RendererState.ERROR)), 'error');
	assert.equal(deriveViewState(target(RendererState.UNSUPPORTED)), 'unsupported');
	assert.equal(deriveViewState(target(RendererState.UNSPECIFIED)), 'unknown');
});

test('only fresh or last-known maps with a path may mount a frame', () => {
	for (const state of [
		'onboarding',
		'installing',
		'rendering',
		'stopped',
		'error',
		'unsupported',
		'unknown',
		'loading'
	] as const) {
		assert.equal(canMountEmbed(state, '/api/v1/worldview/map/key/'), false);
	}
	assert.equal(canMountEmbed('ready', null), false);
	assert.equal(canMountEmbed('ready', ''), false);
	assert.equal(canMountEmbed('ready', '/api/v1/worldview/map/key/'), true);
	assert.equal(canMountEmbed('stale', '/api/v1/worldview/map/key/'), true);
	assert.equal(liveOverlaysEnabled('ready'), true);
	assert.equal(liveOverlaysEnabled('stale'), false);
	assert.equal(liveOverlaysEnabled('stopped'), false);
});

test('age uses the real observation and never invents a zero', () => {
	const now = Date.UTC(2026, 0, 1, 12, 0, 0);
	const observed = create(TimestampSchema, {
		seconds: BigInt(Math.floor(now / 1000) - 180),
		nanos: 0
	});
	assert.equal(formatAge(observed, now), '3 minutes ago');
	assert.equal(formatAge(undefined, now), 'Not reported');
	assert.equal(timestampToMs(undefined), null);
	assert.equal(formatAge({ seconds: BigInt(now / 1000 - 50), nanos: 0 }, now), 'just now');
	assert.equal(
		formatAge(
			create(TimestampSchema, { seconds: BigInt(Math.floor(now / 1000) + 500), nanos: 0 }),
			now
		),
		'just now'
	);
});
