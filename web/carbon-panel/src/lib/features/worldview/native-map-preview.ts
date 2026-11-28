// SPDX-License-Identifier: MPL-2.0
import {
	loadNativeManifest,
	loadNativeTile,
	TILE_BLOCKS,
	visibleNativeTiles,
	type NativeMapFailure
} from './native-map';

type PreviewResult = { status: 'saved'; src: string } | { status: 'empty' | NativeMapFailure };

function terrainBounds(rgba: Uint8ClampedArray, width: number, height: number) {
	let left = width;
	let right = -1;
	let top = height;
	let bottom = -1;
	for (let z = 0; z < height; z++) {
		for (let x = 0; x < width; x++) {
			if (!rgba[(z * width + x) * 4 + 3]) continue;
			left = Math.min(left, x);
			right = Math.max(right, x);
			top = Math.min(top, z);
			bottom = Math.max(bottom, z);
		}
	}
	return right < 0 ? null : { left, top, width: right - left + 1, height: bottom - top + 1 };
}

/** Frame a continuous patch where it still gives viewers meaningful terrain context. */
export function terrainFrame(rgba: Uint8ClampedArray, width: number, height: number) {
	const bounds = terrainBounds(rgba, width, height);
	if (!bounds) return null;
	const runs = new Uint16Array(width);
	let best = bounds;
	// Avoid zooming into a narrow edge or discarding most of the nearby terrain.
	let area = Math.floor((bounds.width * bounds.height) / 2);
	for (let z = bounds.top; z < bounds.top + bounds.height; z++) {
		// Treat the consecutive saved rows above each pixel as a histogram.
		// A shorter run closes each candidate rectangle on its left.
		const rising: { left: number; height: number }[] = [];
		const right = bounds.left + bounds.width;
		for (let x = bounds.left; x <= right; x++) {
			const saved = x < right && rgba[(z * width + x) * 4 + 3];
			const run = x < right ? (runs[x] = saved ? runs[x] + 1 : 0) : 0;
			let left = x;
			while (rising.length && rising[rising.length - 1].height > run) {
				const previous = rising.pop()!;
				left = previous.left;
				const span = x - left;
				if (span < 32 || previous.height < 32 || span * previous.height <= area) continue;
				area = span * previous.height;
				best = { left, top: z - previous.height + 1, width: span, height: previous.height };
			}
			if (run && (!rising.length || rising[rising.length - 1].height < run)) {
				rising.push({ left, height: run });
			}
		}
	}
	return best;
}

/** Sample at most nine nearby saved tiles with two requests in flight. */
export async function loadNativePreview(path: string, signal: AbortSignal): Promise<PreviewResult> {
	const manifest = await loadNativeManifest(path, signal);
	if (manifest.status !== 'ready') return manifest;
	const spawn = manifest.manifest.spawn ?? { x: 0, z: 0 };
	const tiles = visibleNativeTiles(spawn, { width: 2 * TILE_BLOCKS, height: 2 * TILE_BLOCKS }, 1);
	const left = Math.min(...tiles.map((tile) => tile.x));
	const top = Math.min(...tiles.map((tile) => tile.z));
	const canvas = document.createElement('canvas');
	canvas.width = (Math.max(...tiles.map((tile) => tile.x)) - left + 1) * TILE_BLOCKS;
	canvas.height = (Math.max(...tiles.map((tile) => tile.z)) - top + 1) * TILE_BLOCKS;
	const context = canvas.getContext('2d');
	if (!context) return { status: 'unavailable' };

	let failure: NativeMapFailure | undefined;
	let next = 0;
	const recordFailure = (status: NativeMapFailure) => {
		if (status === 'access' || (status === 'changed' && failure !== 'access')) {
			failure = status;
		} else {
			failure ??= status;
		}
	};
	const loadWorker = async () => {
		while (next < tiles.length && failure !== 'access' && failure !== 'changed') {
			signal.throwIfAborted();
			const tile = tiles[next++];
			const result = await loadNativeTile(path, tile, signal).catch(() => ({
				status: 'unavailable' as const
			}));
			signal.throwIfAborted();
			switch (result.status) {
				case 'saved': {
					const image = new Image();
					image.src = result.src;
					await image.decode().catch(() => {});
					signal.throwIfAborted();
					if (image.naturalWidth === TILE_BLOCKS && image.naturalHeight === TILE_BLOCKS) {
						context.drawImage(image, (tile.x - left) * TILE_BLOCKS, (tile.z - top) * TILE_BLOCKS);
					}
					break;
				}
				case 'empty':
				case 'unchanged':
					break;
				default:
					recordFailure(result.status);
			}
		}
	};

	await Promise.all([loadWorker(), loadWorker()]);
	if (failure === 'access' || failure === 'changed') return { status: failure };
	const pixels = context.getImageData(0, 0, canvas.width, canvas.height);
	const bounds = terrainFrame(pixels.data, canvas.width, canvas.height);
	if (!bounds) return { status: failure ?? 'empty' };
	// The interactive map differentiates provisional chunks and provides
	// coverage details. The card displays their observed surface normally
	// while retaining fully transparent gaps for chunks without a surface.
	for (let alpha = 3; alpha < pixels.data.length; alpha += 4) {
		if (pixels.data[alpha]) pixels.data[alpha] = 255;
	}
	context.putImageData(pixels, 0, 0);
	const cropped = document.createElement('canvas');
	cropped.width = bounds.width;
	cropped.height = bounds.height;
	const croppedContext = cropped.getContext('2d');
	if (!croppedContext) return { status: 'unavailable' };
	croppedContext.drawImage(
		canvas,
		bounds.left,
		bounds.top,
		bounds.width,
		bounds.height,
		0,
		0,
		bounds.width,
		bounds.height
	);
	return { status: 'saved', src: cropped.toDataURL('image/png') };
}
