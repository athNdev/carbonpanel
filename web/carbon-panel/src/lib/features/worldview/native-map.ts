// SPDX-License-Identifier: MPL-2.0
import { isSameOriginProxyPath } from './session';

export const TILE_BLOCKS = 128;
export const CHUNKS_PER_TILE = 64;
export const MIN_BLOCK = -30_000_000;
export const MAX_BLOCK = 29_999_999;
const MIN_TILE = MIN_BLOCK / TILE_BLOCKS;
const MAX_TILE = Math.floor(MAX_BLOCK / TILE_BLOCKS);
// The largest span plus one partially visible tile on either side of a seam.
export const MAP_SPANS = [1024, 512, 256, 128, 64, 32] as const;
const MAX_VISIBLE_AXIS = MAP_SPANS[0] / TILE_BLOCKS + 1;

export interface MapPoint {
	x: number;
	z: number;
}

export interface MapSize {
	width: number;
	height: number;
}

export type NativeDimension = 'overworld' | 'end';

export interface NativeManifest {
	tileBlocks: typeof TILE_BLOCKS;
	dimension: 'overworld';
	dimensions: NativeDimension[];
	spawn: MapPoint | null;
}

export type NativeMapFailure = 'busy' | 'changed' | 'unsupported' | 'unavailable' | 'access';
export type ManifestResult =
	{ status: 'ready'; manifest: NativeManifest } | { status: NativeMapFailure };
export type TileResult =
	| {
			status: 'saved';
			src: string;
			chunks: number | null;
			provisionalChunks: number;
			savedAt: string | null;
	  }
	| { status: 'empty' | NativeMapFailure };

export const NATIVE_FAILURE_MESSAGE: Record<NativeMapFailure, string> = {
	busy: 'Map rendering is busy. Retry in a moment.',
	changed: 'The active world changed. Reopen the map to view the current world.',
	unsupported: 'Carbon Panel cannot read saved terrain for this world.',
	unavailable: 'Carbon Panel could not read the map. Retry in a moment.',
	access: 'The map session is no longer authorized. Refresh the World View page.'
};

/** Human labels for the vanilla dimensions the native map can serve. */
const NATIVE_DIMENSION_LABELS: Record<NativeDimension, string> = {
	overworld: 'Overworld',
	end: 'The End'
};

export function nativeDimensionLabel(dimension: NativeDimension): string {
	return NATIVE_DIMENSION_LABELS[dimension] ?? dimension;
}

/**
 * A native map serves only the dimensions its manifest lists. An unavailable
 * or unknown deep link falls back to the Overworld instead of requesting an
 * asset path the session does not offer.
 */
export function resolveNativeDimension(
	requested: string | null | undefined,
	dimensions: readonly string[]
): NativeDimension {
	return requested === 'end' && dimensions.includes('end') ? 'end' : 'overworld';
}

export function nativeSessionRoot(path: string): string | null {
	return isSameOriginProxyPath(path) && /^\/api\/v1\/worldview\/map\/[A-Za-z0-9_-]+\/$/.test(path)
		? path
		: null;
}

export function clampBlock(value: number): number {
	return Math.min(MAX_BLOCK, Math.max(MIN_BLOCK, value));
}

export function parseNativeCoordinate(value: string): number | null {
	const input = value.trim();
	if (!/^-?\d+$/.test(input)) return null;
	const block = Number(input);
	return Number.isInteger(block) && block >= MIN_BLOCK && block <= MAX_BLOCK ? block : null;
}

export function tileKey(tile: MapPoint): string {
	return tile.x + ':' + tile.z;
}

/** Span is measured along the longer viewport edge, including on mobile. */
export function blockScale(size: MapSize, zoomStep: number): number {
	return Math.max(1, size.width, size.height) / MAP_SPANS[zoomStep];
}

export function visibleNativeTiles(center: MapPoint, size: MapSize, scale: number): MapPoint[] {
	if (size.width <= 0 || size.height <= 0) return [];
	const left = center.x - size.width / (2 * scale);
	const top = center.z - size.height / (2 * scale);
	const firstX = Math.max(MIN_TILE, Math.floor(left / TILE_BLOCKS));
	const firstZ = Math.max(MIN_TILE, Math.floor(top / TILE_BLOCKS));
	// The right and bottom edges are exclusive. An edge on a seam needs no extra tile.
	const lastX = Math.min(
		MAX_TILE,
		Math.ceil((center.x + size.width / (2 * scale)) / TILE_BLOCKS) - 1,
		firstX + MAX_VISIBLE_AXIS - 1
	);
	const lastZ = Math.min(
		MAX_TILE,
		Math.ceil((center.z + size.height / (2 * scale)) / TILE_BLOCKS) - 1,
		firstZ + MAX_VISIBLE_AXIS - 1
	);
	const tiles: MapPoint[] = [];
	for (let z = firstZ; z <= lastZ; z++) {
		for (let x = firstX; x <= lastX; x++) tiles.push({ x, z });
	}
	const distance = (tile: MapPoint) =>
		((tile.x + 0.5) * TILE_BLOCKS - center.x) ** 2 + ((tile.z + 0.5) * TILE_BLOCKS - center.z) ** 2;
	return tiles.sort((a, b) => distance(a) - distance(b));
}

function failure(response: Response): NativeMapFailure {
	switch (response.status) {
		case 401:
		case 403:
			return 'access';
		case 409:
			return 'changed';
		case 422:
			return 'unsupported';
		case 429:
			return 'busy';
		case 503:
			return response.headers.get('Retry-After') === '1' ? 'busy' : 'unavailable';
		default:
			return 'unavailable';
	}
}

function request(path: string, signal: AbortSignal): Promise<Response> {
	return fetch(path, {
		signal,
		mode: 'same-origin',
		credentials: 'same-origin',
		redirect: 'error',
		cache: 'no-store'
	});
}

function mediaType(response: Response): string | undefined {
	return response.headers.get('content-type')?.split(';')[0].trim().toLowerCase();
}

function isPoint(value: unknown): value is MapPoint {
	if (!value || typeof value !== 'object') return false;
	const point = value as Partial<MapPoint>;
	return [point.x, point.z].every(
		(block) =>
			typeof block === 'number' &&
			Number.isInteger(block) &&
			block >= MIN_BLOCK &&
			block <= MAX_BLOCK
	);
}

export async function loadNativeManifest(
	path: string,
	signal: AbortSignal
): Promise<ManifestResult> {
	const root = nativeSessionRoot(path);
	if (!root) return { status: 'unavailable' };
	const response = await request(root + 'native/manifest.json', signal);
	if (!response.ok) return { status: failure(response) };
	if (mediaType(response) !== 'application/json') return { status: 'unavailable' };
	const manifest: (Partial<Omit<NativeManifest, 'dimensions'>> & { dimensions?: unknown }) | null =
		await response.json();
	if (
		manifest?.tileBlocks !== TILE_BLOCKS ||
		manifest.dimension !== 'overworld' ||
		(manifest.spawn !== null && !isPoint(manifest.spawn))
	)
		return { status: 'unavailable' };
	const options = manifest.dimensions;
	const dimensions: NativeDimension[] = ['overworld'];
	if (Array.isArray(options) && options.includes('overworld') && options.includes('end')) {
		dimensions.push('end');
	}
	return {
		status: 'ready',
		manifest: { tileBlocks: TILE_BLOCKS, dimension: 'overworld', dimensions, spawn: manifest.spawn }
	};
}

// The panel CSP permits PNG data URLs and does not need a blob: exception.
function toImageSource(image: Blob): Promise<string> {
	return new Promise((resolve, reject) => {
		const reader = new FileReader();
		reader.onload = () => resolve(String(reader.result));
		reader.onerror = () => reject(reader.error);
		reader.readAsDataURL(image);
	});
}

export async function loadNativeTile(
	path: string,
	tile: MapPoint,
	signal: AbortSignal,
	dimension: NativeDimension = 'overworld'
): Promise<TileResult> {
	const root = nativeSessionRoot(path);
	if (
		!root ||
		(dimension !== 'overworld' && dimension !== 'end') ||
		![tile.x, tile.z].every(
			(index) => Number.isInteger(index) && index >= MIN_TILE && index <= MAX_TILE
		)
	) {
		return { status: 'unavailable' };
	}
	const url = root + 'native/' + dimension + '/' + tile.x + '/' + tile.z + '.png';
	let response = await request(url, signal);
	if ((response.status === 503 || response.status === 429) && failure(response) === 'busy') {
		// Minecraft may rewrite the region header during a read. Give that save
		// one second to finish without changing the session or losing the camera.
		signal.throwIfAborted();
		await new Promise<void>((resolve, reject) => {
			const timeout = setTimeout(() => {
				signal.removeEventListener('abort', abort);
				resolve();
			}, 1_000);
			function abort() {
				clearTimeout(timeout);
				reject(signal.reason);
			}
			signal.addEventListener('abort', abort, { once: true });
		});
		response = await request(url, signal);
	}
	if (response.status === 404) return { status: 'empty' };
	if (!response.ok) return { status: failure(response) };
	if (mediaType(response) !== 'image/png') return { status: 'unavailable' };

	const count = response.headers.get('X-CarbonPanel-Rendered-Chunks');
	const number = count !== null && /^\d+$/.test(count) ? Number(count) : null;
	const chunks = number !== null && number <= CHUNKS_PER_TILE ? number : null;
	const provisional = Number(response.headers.get('X-CarbonPanel-Provisional-Chunks'));
	const provisionalChunks =
		Number.isInteger(provisional) && provisional > 0 && provisional <= (chunks ?? 0)
			? provisional
			: 0;
	if (chunks === 0) return { status: 'empty' };
	const timestamp = response.headers.get('X-CarbonPanel-Tile-Saved-At');
	const savedAt =
		timestamp && /^\d{4}-\d{2}-\d{2}T/.test(timestamp) && Number.isFinite(Date.parse(timestamp))
			? timestamp
			: null;
	const image = await response.blob();
	if (!image.size || image.size > 256 * 1024) return { status: 'unavailable' };
	return { status: 'saved', src: await toImageSource(image), chunks, provisionalChunks, savedAt };
}
