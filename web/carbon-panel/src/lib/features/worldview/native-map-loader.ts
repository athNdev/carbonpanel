// SPDX-License-Identifier: MPL-2.0
import {
	CHUNKS_PER_TILE,
	loadNativeTile,
	tileKey,
	type MapPoint,
	type TileResult
} from './native-map';

const MAX_ACTIVE_REQUESTS = 2;
const MAX_CACHED_TILES = 96;
const RECHECK_DELAY_MS = 30_000;

function recheckAt(result: TileResult): number {
	const incomplete =
		result.status === 'saved' &&
		(result.chunks === null || result.chunks < CHUNKS_PER_TILE || result.provisionalChunks > 0);
	return incomplete ||
		result.status === 'empty' ||
		result.status === 'busy' ||
		result.status === 'unavailable'
		? Date.now() + RECHECK_DELAY_MS
		: Infinity;
}

/** The visible tiles are ordered from the center out by the viewport. */
export function createNativeTileLoader(
	root: string,
	update: (results: ReadonlyMap<string, TileResult>) => void,
	load = loadNativeTile
) {
	let visible = new Map<string, MapPoint>();
	const active = new Map<string, AbortController>();
	const cache = new Map<string, { result: TileResult; recheckAt: number }>();
	let recheckTimer: ReturnType<typeof setTimeout> | undefined;
	let disposed = false;

	function publish() {
		update(new Map([...cache].map(([key, entry]) => [key, entry.result])));
	}

	function evictOutsideViewport() {
		for (const key of cache.keys()) {
			if (cache.size <= MAX_CACHED_TILES) break;
			if (visible.has(key)) continue;
			cache.delete(key);
		}
	}

	function startNext() {
		clearTimeout(recheckTimer);
		if (disposed) return;
		const now = Date.now();
		const eligible = [...visible].filter(
			([key]) => !active.has(key) && (cache.get(key)?.recheckAt ?? now) <= now
		);
		// Pan into new terrain before rechecking a tile already on screen.
		eligible.sort(([a], [b]) => Number(cache.has(a)) - Number(cache.has(b)));
		for (const [key, tile] of eligible) {
			if (active.size >= MAX_ACTIVE_REQUESTS) return;
			const controller = new AbortController();
			active.set(key, controller);
			void load(root, tile, controller.signal)
				.catch((): TileResult => ({ status: 'unavailable' }))
				.then((result) => {
					if (disposed || controller.signal.aborted) return;
					cache.set(key, { result, recheckAt: recheckAt(result) });
					evictOutsideViewport();
					publish();
				})
				.finally(() => {
					active.delete(key);
					startNext();
				});
		}
		if (active.size >= MAX_ACTIVE_REQUESTS) return;
		let earliest = Infinity;
		for (const key of visible.keys()) {
			if (!active.has(key)) earliest = Math.min(earliest, cache.get(key)?.recheckAt ?? Infinity);
		}
		if (Number.isFinite(earliest)) {
			recheckTimer = setTimeout(startNext, Math.max(0, earliest - Date.now()));
		}
	}

	function setVisible(tiles: MapPoint[]) {
		visible = new Map(tiles.map((tile) => [tileKey(tile), tile]));
		for (const [key, controller] of active) {
			if (!visible.has(key)) controller.abort();
		}
		startNext();
	}

	function refresh() {
		for (const key of cache.keys()) {
			if (!visible.has(key)) continue;
			cache.delete(key);
		}
		for (const controller of active.values()) controller.abort();
		publish();
		startNext();
	}

	function dispose() {
		disposed = true;
		clearTimeout(recheckTimer);
		for (const controller of active.values()) controller.abort();
		cache.clear();
	}

	return { setVisible, refresh, dispose };
}
