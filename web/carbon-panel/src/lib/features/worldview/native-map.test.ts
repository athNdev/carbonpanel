// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { setImmediate } from 'node:timers/promises';
import {
	blockScale,
	CHUNKS_PER_TILE,
	loadNativeManifest,
	loadNativeTile,
	nativeDimensionLabel,
	nativeSessionRoot,
	parseNativeCoordinate,
	savedAgeLabel,
	tileKey,
	visibleNativeTiles,
	type NativeTileRequest,
	type TileResult
} from './native-map';
import { createNativeTileLoader } from './native-map-loader';

const session = '/api/v1/worldview/map/session_01-abc/';

test('native map accepts only a canonical keyed proxy root and exact block coordinates', () => {
	assert.equal(nativeSessionRoot(session), session);
	for (const invalid of [
		'https://example.invalid/api/v1/worldview/map/session/',
		'//api/v1/worldview/map/session/',
		'/api/v1/worldview/map/../',
		'/api/v1/worldview/map/session%2f/',
		'/api/v1/worldview/map/session/?extra=1',
		'/api/v1/worldview/map/session/#fragment',
		'/api/v1/worldview/map/session/other/',
		'/api/v1/worldview/map/session/\n'
	])
		assert.equal(nativeSessionRoot(invalid), null, invalid);

	assert.equal(parseNativeCoordinate(' -30000000 '), -30_000_000);
	assert.equal(parseNativeCoordinate('29999999'), 29_999_999);
	for (const invalid of ['-30000001', '30000000', '3.1', '1e3', '0x10', '', '∞']) {
		assert.equal(parseNativeCoordinate(invalid), null, invalid);
	}
});

test('visible tiles use floor for negative coordinates, exclude seam neighbors, and fit the request budget', () => {
	const oneTile = visibleNativeTiles({ x: -64, z: -64 }, { width: 128, height: 128 }, 1);
	assert.deepEqual(oneTile, [{ x: -1, z: -1 }]);
	const size = { width: 3840, height: 2160 };
	const wide = visibleNativeTiles({ x: 112, z: -33 }, size, blockScale(size, 0));
	assert.ok(wide.length > 36 && wide.length <= 81);
	assert.deepEqual(wide[0], { x: 0, z: -1 });
	const corner = visibleNativeTiles({ x: -30_000_000, z: 29_999_999 }, size, blockScale(size, 0));
	assert.ok(corner.length > 0 && corner.length <= 81);
	assert.ok(corner.every((tile) => tile.x >= -234375 && tile.z <= 234374));
});

test('the widest span covers every visible tile on both sides of an offset square viewport', () => {
	const size = { width: 500, height: 500 };
	const center = { x: 17, z: -21 };
	const scale = blockScale(size, 0);
	assert.equal(size.width / scale, 1024);
	const tiles = visibleNativeTiles(center, size, scale);
	assert.equal(tiles.length, 81);
	assert.ok(tiles.some((t) => t.x === -4 && t.z === -5));
	assert.ok(tiles.some((t) => t.x === 4 && t.z === 3));
});

test('invalid request paths and out-of-range tiles are rejected before fetch', async () => {
	const signal = new AbortController().signal;
	assert.deepEqual(await loadNativeManifest('/bad/', signal), { status: 'unavailable' });
	assert.deepEqual(await loadNativeTile(session, { x: 234375, z: 0 }, signal), {
		status: 'unavailable'
	});
	assert.deepEqual(await loadNativeTile(session, { x: 0, z: -234376 }, signal), {
		status: 'unavailable'
	});
});

test('tile queue deduplicates visible tiles, fences stale results, and holds two requests at a time', async () => {
	const requests: {
		key: string;
		signal: AbortSignal;
		finish: (result: TileResult) => void;
	}[] = [];
	let result = new Map<string, TileResult>();
	const loader = createNativeTileLoader(
		session,
		'overworld',
		(next) => (result = new Map(next)),
		(_root, tile, signal) =>
			new Promise((finish) => requests.push({ key: tileKey(tile), signal, finish }))
	);
	const p1 = { x: -1, z: 0 };
	const p2 = { x: 0, z: 0 };
	const p3 = { x: 1, z: 0 };
	const p4 = { x: 2, z: 0 };

	loader.setVisible([p1, p2, p3, p1]);
	assert.deepEqual(
		requests.map((request) => request.key),
		['-1:0', '0:0']
	);
	loader.setVisible([p2, p3, p4]);
	assert.equal(requests[0].signal.aborted, true);
	assert.equal(requests[1].signal.aborted, false);
	assert.equal(requests.length, 2);
	requests[0].finish({ status: 'empty' });
	await setImmediate();
	assert.equal(result.has('-1:0'), false);
	assert.equal(requests[2].key, '1:0');
	requests[1].finish({ status: 'busy' });
	await setImmediate();
	assert.equal(requests[3].key, '2:0');
	assert.deepEqual(result.get('0:0'), { status: 'busy' });
	loader.dispose();
	assert.equal(requests[2].signal.aborted, true);
	assert.equal(requests[3].signal.aborted, true);
	requests[2].finish({ status: 'empty' });
	requests[3].finish({ status: 'empty' });
	await setImmediate();
	assert.equal(result.size, 1);
});

test('explicit retry refetches a busy visible tile without retaining its old outcome', async () => {
	const statuses: TileResult[] = [{ status: 'busy' }, { status: 'empty' }];
	let result: TileResult | undefined;
	const loader = createNativeTileLoader(
		session,
		'overworld',
		(next) => (result = next.get('0:0')),
		async () => statuses.shift() ?? { status: 'unavailable' }
	);
	try {
		loader.setVisible([{ x: 0, z: 0 }]);
		await setImmediate();
		assert.deepEqual(result, { status: 'busy' });
		loader.refresh();
		assert.equal(result, undefined);
		await setImmediate();
		assert.deepEqual(result, { status: 'empty' });
	} finally {
		loader.dispose();
	}
});

function withFetch(body: unknown, run: () => Promise<void>): Promise<void> {
	const original = globalThis.fetch;
	globalThis.fetch = (async () =>
		new Response(JSON.stringify(body), {
			status: 200,
			headers: { 'content-type': 'application/json' }
		})) as typeof fetch;
	return run().finally(() => {
		globalThis.fetch = original;
	});
}

test('the manifest dimension list is sanitized and always offers the overworld', async () => {
	await withFetch(
		{
			tileBlocks: 128,
			dimension: 'overworld',
			spawn: null,
			dimensions: ['overworld', 'nether', 'end']
		},
		async () => {
			const result = await loadNativeManifest(session, new AbortController().signal);
			assert.deepEqual(result.status === 'ready' ? result.manifest.dimensions : result.status, [
				'overworld',
				'nether',
				'end'
			]);
		}
	);
	await withFetch(
		{
			tileBlocks: 128,
			dimension: 'overworld',
			spawn: null,
			dimensions: ['../escape', 'nether', 'overworld', 'nether', '']
		},
		async () => {
			const result = await loadNativeManifest(session, new AbortController().signal);
			assert.deepEqual(result.status === 'ready' ? result.manifest.dimensions : result.status, [
				'overworld',
				'nether'
			]);
		}
	);
	await withFetch({ tileBlocks: 128, dimension: 'overworld', spawn: null }, async () => {
		const result = await loadNativeManifest(session, new AbortController().signal);
		assert.deepEqual(result.status === 'ready' ? result.manifest.dimensions : result.status, [
			'overworld'
		]);
	});
	await withFetch(
		{ tileBlocks: 128, dimension: 'overworld', spawn: null, dimensions: 'overworld' },
		async () => {
			const result = await loadNativeManifest(session, new AbortController().signal);
			assert.deepEqual(result.status === 'ready' ? result.manifest.dimensions : result.status, [
				'overworld'
			]);
		}
	);
});

test('saved-age labels stay honest for unknown or future timestamps', () => {
	const now = Date.parse('2026-11-23T12:00:00Z');
	assert.equal(savedAgeLabel(null, now), null);
	assert.equal(savedAgeLabel('not-a-time', now), null);
	assert.equal(savedAgeLabel('2026-11-23T11:59:40Z', now), 'just now');
	assert.equal(savedAgeLabel('2026-11-23T11:59:00Z', now), '1 minute ago');
	assert.equal(savedAgeLabel('2026-11-23T11:00:00Z', now), '1 hour ago');
	assert.equal(savedAgeLabel('2026-11-20T12:00:00Z', now), '3 days ago');
	assert.equal(nativeDimensionLabel('overworld'), 'Overworld');
	assert.equal(nativeDimensionLabel('nether'), 'The Nether');
	assert.equal(nativeDimensionLabel('custom_dim'), 'custom_dim');
});

const flush = () => new Promise((resolve) => setTimeout(resolve, 20));

test('live mode revalidates complete tiles with the region validator and keeps a 304 image', async () => {
	const saved: TileResult = {
		status: 'saved',
		src: 'data:image/png;base64,',
		chunks: CHUNKS_PER_TILE,
		provisionalChunks: 0,
		savedAt: '2026-11-23T00:00:00Z',
		regionModified: '"123-456"'
	};
	let result: TileResult | undefined;
	const requests: {
		request: NativeTileRequest | undefined;
		finish: (result: TileResult) => void;
	}[] = [];
	const loader = createNativeTileLoader(
		session,
		'overworld',
		(next) => (result = next.get('0:0')),
		(_root, _tile, _signal, request) => new Promise((finish) => requests.push({ request, finish })),
		{ live: false, liveRecheckDelayMs: 5 }
	);
	try {
		loader.setVisible([{ x: 0, z: 0 }]);
		await setImmediate();
		requests[0].finish(saved);
		await setImmediate();
		assert.equal(result, saved);

		// Live off (the classic map): a complete tile is never rechecked.
		await flush();
		assert.equal(requests.length, 1);

		// Live on: the revalidation echoes the region validator. A 304 keeps
		// the cached image without publishing a replacement.
		loader.setLive(true);
		await flush();
		assert.equal(requests.length, 2);
		assert.deepEqual(requests[1].request, { dimension: 'overworld', ifNoneMatch: '"123-456"' });
		requests[1].finish({ status: 'unchanged' });
		await setImmediate();
		assert.equal(result, saved);
		const afterUnchanged = requests.length;

		// Turning live off again stops the revalidation cadence.
		loader.setLive(false);
		await flush();
		assert.equal(requests.length, afterUnchanged);

		// A live toggle only changes the cadence: the cached tile and its
		// validator survive, so the next revalidation stays conditional
		// instead of refetching the tile unconditionally.
		assert.equal(result, saved);
		loader.setLive(true);
		await flush();
		assert.equal(requests.length, afterUnchanged + 1);
		assert.deepEqual(requests[afterUnchanged].request, {
			dimension: 'overworld',
			ifNoneMatch: '"123-456"'
		});
	} finally {
		loader.dispose();
	}
});

test('a changed region re-renders and the tile dimension flows into the request', async () => {
	let result: TileResult | undefined;
	const requests: {
		request: NativeTileRequest | undefined;
		finish: (result: TileResult) => void;
	}[] = [];
	const loader = createNativeTileLoader(
		session,
		'nether',
		(next) => (result = next.get('-3:4')),
		(_root, _tile, _signal, request) => new Promise((finish) => requests.push({ request, finish })),
		{ live: false, liveRecheckDelayMs: 5 }
	);
	try {
		loader.setVisible([{ x: -3, z: 4 }]);
		await setImmediate();
		assert.deepEqual(requests[0].request, { dimension: 'nether' });
		requests[0].finish({
			status: 'saved',
			src: 'data:image/png;base64,',
			chunks: CHUNKS_PER_TILE,
			provisionalChunks: 0,
			savedAt: null,
			regionModified: '"a-1"'
		});
		await setImmediate();
		loader.setLive(true);
		await flush();
		assert.equal(requests.length, 2);
		assert.deepEqual(requests[1].request, { dimension: 'nether', ifNoneMatch: '"a-1"' });
		requests[1].finish({
			status: 'saved',
			src: 'data:image/png;base64,2',
			chunks: CHUNKS_PER_TILE,
			provisionalChunks: 0,
			savedAt: null,
			regionModified: '"a-2"'
		});
		await setImmediate();
		assert.equal(result?.status, 'saved');
		assert.equal(result !== undefined && result.regionModified, '"a-2"');
	} finally {
		loader.dispose();
	}
});
