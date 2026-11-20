// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import { MapOverlaySchema, type MapOverlay } from '$lib/proto/carbonpanel/v1/worldview_pb';
import { nativeMapMarkers, projectNativeMapPoint } from './native-map-markers';

function overlay(
	id: string,
	patch: Partial<Omit<MapOverlay, '$typeName' | '$unknown'>> = {}
): MapOverlay {
	return create(MapOverlaySchema, {
		id,
		kind: 'marker',
		dimension: 'overworld',
		x: -8.5,
		z: 31.25,
		...patch
	});
}

test('native markers admit only supported, non-sensitive and valid Overworld points', () => {
	const markers = nativeMapMarkers([
		overlay('marker', { label: 'Spawn hall', owner: 'owner', style: 'legacy-style' }),
		overlay('waypoint', { kind: 'waypoint' }),
		overlay('poi', { kind: 'poi' }),
		overlay('poi', { kind: 'poi', label: 'Duplicate' }),
		overlay('player', { kind: 'player' }),
		overlay('sensitive', { sensitive: true }),
		overlay('region', { kind: 'region' }),
		overlay('nether', { dimension: 'nether' }),
		overlay('non-finite', { x: Number.NaN }),
		overlay('out-of-bounds', { z: 30_000_000 }),
		overlay('')
	]);
	assert.deepEqual(markers, [
		{
			id: 'marker',
			kind: 'marker',
			dimension: 'overworld',
			label: 'Spawn hall',
			x: -8.5,
			z: 31.25
		},
		{
			id: 'waypoint',
			kind: 'waypoint',
			dimension: 'overworld',
			label: 'Waypoint',
			x: -8.5,
			z: 31.25
		},
		{
			id: 'poi',
			kind: 'poi',
			dimension: 'overworld',
			label: 'Point of interest',
			x: -8.5,
			z: 31.25
		}
	]);
});

test('native End markers exclude other dimensions and sensitive points', () => {
	const markers = nativeMapMarkers(
		[
			overlay('shared'),
			overlay('shared', { dimension: 'end', kind: 'poi' }),
			overlay('private', { dimension: 'end', sensitive: true }),
			overlay('player', { dimension: 'end', kind: 'player' })
		],
		'end'
	);
	assert.deepEqual(markers, [
		{ id: 'shared', kind: 'poi', dimension: 'end', label: 'Point of interest', x: -8.5, z: 31.25 }
	]);
});

test('native projection preserves fractional coordinates through pan, zoom and reflow', () => {
	const point = { x: -130.5, z: 255.25 };
	const center = { x: -128, z: 256 };
	assert.deepEqual(projectNativeMapPoint(point, center, { width: 320, height: 240 }, 2.5), {
		left: 153.75,
		top: 118.125
	});
	assert.deepEqual(projectNativeMapPoint(point, point, { width: 960, height: 540 }, 4), {
		left: 480,
		top: 270
	});
});
