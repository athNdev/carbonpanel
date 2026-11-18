// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import { MapOverlaySchema } from '$lib/proto/carbonpanel/v1/worldview_pb';
import { filterOverlays, overlayDisplay, overlayKindLabel } from './overlays';

const overlay = (dimension: string, sensitive: boolean, label = 'Base') =>
	create(MapOverlaySchema, {
		id: `${dimension}-${sensitive}`,
		dimension,
		kind: 'waypoint',
		sensitive,
		label,
		x: 12.6,
		z: -40.2
	});

test('overlays are scoped by dimension and sensitive ones are opt-in', () => {
	const all = [overlay('overworld', false), overlay('nether', false), overlay('overworld', true)];
	assert.equal(filterOverlays(all, 'overworld', false).length, 1);
	assert.equal(filterOverlays(all, 'overworld', true).length, 2);
	assert.equal(filterOverlays(all, '', false).length, 2);
});

test('sensitive markers keep an opaque label and never reveal coordinates', () => {
	const sensitive = overlay('overworld', true, '');
	const display = overlayDisplay(sensitive);
	assert.equal(display.coordinates, null);
	assert.equal(display.label, 'Sensitive marker');
	const normal = overlayDisplay(overlay('overworld', false, 'Spawn'));
	assert.equal(normal.label, 'Spawn');
	assert.equal(normal.coordinates, '13, -40');
	assert.equal(overlayKindLabel('poi'), 'Point of interest');
});
