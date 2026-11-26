// SPDX-License-Identifier: MPL-2.0
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { terrainFrame } from './native-map-preview';

test('native preview crops to saved and provisional pixels across tile boundaries', () => {
	const width = 256;
	const height = 256;
	const pixels = new Uint8ClampedArray(width * height * 4);
	pixels[(12 * width + 122) * 4 + 3] = 176;
	pixels[(143 * width + 152) * 4 + 3] = 255;
	assert.deepEqual(terrainFrame(pixels, width, height), {
		left: 122,
		top: 12,
		width: 31,
		height: 132
	});
});

test('native preview omits empty terrain', () => {
	assert.equal(terrainFrame(new Uint8ClampedArray(10 * 10 * 4), 10, 10), null);
});

test('native preview frames the continuous terrain beside missing edge chunks', () => {
	const width = 96;
	const height = 64;
	const pixels = new Uint8ClampedArray(width * height * 4);
	for (let z = 0; z < height; z++) {
		for (let x = z < 16 ? 16 : 0; x < width; x++) pixels[(z * width + x) * 4 + 3] = 255;
	}
	assert.deepEqual(terrainFrame(pixels, width, height), {
		left: 16,
		top: 0,
		width: 80,
		height: 64
	});
});
