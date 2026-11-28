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

export interface NativeManifest {
	tileBlocks: typeof TILE_BLOCKS;
	dimension: 'overworld';
	spawn: MapPoint | null;
	/** Saved vanilla dimensions this world offers tiles for, overworld first. */
	dimensions: string[];
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
			/**
			 * Opaque region freshness validator (region size + mtime). Live
			 * revalidation echoes it back in If-None-Match; a 304 keeps the
			 * cached image without re-rendering.
			 */
			regionModified: string | null;
	  }
	| { status: 'empty' | 'unchanged' | NativeMapFailure };

/** The dimension whose terrain a tile fetch addresses. */
export interface NativeTileRequest {
	dimension?: string;
	ifNoneMatch?: string | null;
}

export const NATIVE_FAILURE_MESSAGE: Record<NativeMapFailure, string> = {
	busy: 'Map rendering is busy. Retry in a moment.',
	changed: 'The active world changed. Reopen the map to view the current world.',
	unsupported: 'Carbon Panel cannot read saved terrain for this world.',
	unavailable: 'Carbon Panel could not read the map. Retry in a moment.',
	access: 'The map session is no longer authorized. Refresh the World View page.'
};

/** Human labels for the vanilla dimensions the native map can serve. */
const NATIVE_DIMENSION_LABELS: Record<string, string> = {
	overworld: 'Overworld',
	nether: 'The Nether',
	end: 'The End'
};

export function nativeDimensionLabel(dimension: string): string {
	return NATIVE_DIMENSION_LABELS[dimension] ?? dimension;
}

/**
 * A tile-path dimension segment must stay a plain identifier: it is
 * interpolated into the proxy URL and validated again server-side.
 */
function isDimensionId(value: unknown): value is string {
	return typeof value === 'string' && /^[a-z0-9_-]{1,32}$/.test(value);
}

/**
 * Relative age of the newest chunk save in a view, refreshed by the caller
 * as its clock ticks. Unknown timestamps report null rather than a
 * plausible-looking zero.
 */
export function savedAgeLabel(savedAt: string | null, nowMs: number = Date.now()): string | null {
	if (!savedAt) return null;
	const saved = Date.parse(savedAt);
	if (!Number.isFinite(saved)) return null;
	const seconds = Math.max(0, Math.floor((nowMs - saved) / 1000));
	if (seconds < 60) return 'just now';
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return `${minutes} minute${minutes === 1 ? '' : 's'} ago`;
	const hours = Math.floor(minutes / 60);
	if (hours < 24) return `${hours} hour${hours === 1 ? '' : 's'} ago`;
	const days = Math.floor(hours / 24);
	return `${days} day${days === 1 ? '' : 's'} ago`;
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

function request(path: string, signal: AbortSignal, headers?: HeadersInit): Promise<Response> {
	return fetch(path, {
		signal,
		mode: 'same-origin',
		credentials: 'same-origin',
		redirect: 'error',
		cache: 'no-store',
		...(headers ? { headers } : {})
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
	const manifest: Partial<NativeManifest> | null = await response.json();
	if (
		manifest?.tileBlocks !== TILE_BLOCKS ||
		manifest.dimension !== 'overworld' ||
		(manifest.spawn !== null && !isPoint(manifest.spawn))
	)
		return { status: 'unavailable' };
	// The dimension list is additive and optional for older panels. A world
	// always offers the overworld (kept first); every listed dimension stays
	// a plain identifier that the proxy validates again per tile request.
	const listed = Array.isArray(manifest.dimensions) ? manifest.dimensions : ['overworld'];
	const dimensions: string[] = ['overworld'];
	for (const dimension of listed) {
		if (dimension === 'overworld') continue;
		if (!isDimensionId(dimension) || dimensions.includes(dimension)) continue;
		dimensions.push(dimension);
	}
	return {
		status: 'ready',
		manifest: { tileBlocks: TILE_BLOCKS, dimension: 'overworld', spawn: manifest.spawn, dimensions }
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
	tileRequest: NativeTileRequest = {}
): Promise<TileResult> {
	const root = nativeSessionRoot(path);
	const dimension = tileRequest.dimension ?? 'overworld';
	if (
		!root ||
		!isDimensionId(dimension) ||
		![tile.x, tile.z].every(
			(index) => Number.isInteger(index) && index >= MIN_TILE && index <= MAX_TILE
		)
	) {
		return { status: 'unavailable' };
	}
	const url = root + 'native/' + dimension + '/' + tile.x + '/' + tile.z + '.png';
	const conditional = tileRequest.ifNoneMatch || null;
	const fetchTile = async (): Promise<Response | TileResult> => {
		const response = await request(
			url,
			signal,
			conditional ? { 'If-None-Match': conditional } : undefined
		);
		// A live client revalidates a fully rendered tile by echoing the
		// region freshness validator; 304 keeps the cached image.
		if (conditional && response.status === 304) return { status: 'unchanged' };
		return response;
	};
	let response = await fetchTile();
	if (
		response instanceof Response &&
		(response.status === 503 || response.status === 429) &&
		failure(response) === 'busy'
	) {
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
		response = await fetchTile();
	}
	if (!(response instanceof Response)) return response;
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
	const regionModified = response.headers.get('X-CarbonPanel-Region-Modified');
	const image = await response.blob();
	if (!image.size || image.size > 256 * 1024) return { status: 'unavailable' };
	return {
		status: 'saved',
		src: await toImageSource(image),
		chunks,
		provisionalChunks,
		savedAt,
		regionModified: regionModified !== null && regionModified !== '' ? regionModified : null
	};
}
