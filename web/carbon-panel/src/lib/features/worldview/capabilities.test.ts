// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import { CapabilitySchema, RendererType } from '$lib/proto/carbonpanel/v1/worldview_pb';
import { CAP, createCapabilityMatrix, rendererName } from './capabilities';

const cap = (name: string, supported: boolean, detail = '') =>
	create(CapabilitySchema, { name, supported, detail });

test('missing capability is unavailable; explicit false names the renderer', () => {
	const matrix = createCapabilityMatrix([], RendererType.BLUEMAP);
	assert.deepEqual(matrix.dimensions(), { supported: false, reason: 'Unavailable' });
	const denied = createCapabilityMatrix(
		[cap(CAP.dimensions, false, 'webgl unsupported')],
		RendererType.BLUEMAP
	);
	assert.match(denied.dimensions().reason, /Unsupported by BlueMap/);
	assert.match(denied.dimensions().reason, /webgl unsupported/);
	assert.equal(rendererName(RendererType.STATIC), 'static tiles');
});

test('2D and 3D are advertised independently, never substituted', () => {
	const matrix = createCapabilityMatrix([cap(CAP.map2d, true)], RendererType.SQUAREMAP);
	const modes = matrix.viewModes();
	assert.equal(modes[0].supported, true);
	assert.equal(modes[1].supported, false);
	assert.equal(modes[1].reason, 'Unavailable');
	const explicitFalse = createCapabilityMatrix([cap(CAP.map3d, false)], RendererType.SQUAREMAP);
	assert.match(explicitFalse.viewModes()[1].reason, /Unsupported by squaremap/);
	assert.equal(matrix.coordinateSearch().supported, true);
	assert.equal(createCapabilityMatrix([], RendererType.STATIC).coordinateSearch().supported, false);
});

test('players and overlay editing require both capability and permission', () => {
	const matrix = createCapabilityMatrix(
		[cap(CAP.players, true), cap(CAP.markers, true)],
		RendererType.BLUEMAP
	);
	assert.equal(matrix.players(false).supported, false);
	assert.match(matrix.players(false).reason, /permission/);
	assert.equal(matrix.players(true).supported, true);
	assert.equal(matrix.overlayEditing(false).supported, false);
	assert.equal(matrix.overlayEditing(true).supported, true);
	const noPlayers = createCapabilityMatrix([cap(CAP.markers, true)], RendererType.BLUEMAP);
	assert.equal(noPlayers.players(true).reason, 'Unavailable');
});

test('time/weather and telemetry are only shown when TRUE', () => {
	const matrix = createCapabilityMatrix(
		[cap(CAP.timeWeather, false), cap(CAP.telemetry, true)],
		RendererType.DYNMAP
	);
	assert.equal(matrix.timeWeather().supported, false);
	assert.equal(matrix.telemetry().supported, true);
});
