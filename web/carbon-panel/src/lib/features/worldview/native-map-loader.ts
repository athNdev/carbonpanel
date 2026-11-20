// SPDX-License-Identifier: MPL-2.0
import { loadNativeTile, tileKey, type MapPoint, type TileResult } from './native-map';

const MAX_ACTIVE_REQUESTS = 2;
const MAX_CACHED_TILES = 96;

/** The visible tiles are ordered from the center out by the viewport. */
export function createNativeTileLoader(
	root: string,
	update: (results: ReadonlyMap<string, TileResult>) => void,
	load = loadNativeTile
) {
	let visible = new Map<string, MapPoint>();
	const active = new Map<string, AbortController>();
	const cache = new Map<string, TileResult>();
	let disposed = false;

	function publish() {
		update(new Map(cache));
	}

	function evictOutsideViewport() {
		for (const key of cache.keys()) {
			if (cache.size <= MAX_CACHED_TILES) break;
			if (visible.has(key)) continue;
			cache.delete(key);
		}
	}

	function startNext() {
		if (disposed) return;
		for (const [key, tile] of visible) {
			if (active.size >= MAX_ACTIVE_REQUESTS) break;
			if (cache.has(key) || active.has(key)) continue;
			const controller = new AbortController();
			active.set(key, controller);
			void load(root, tile, controller.signal)
				.catch((): TileResult => ({ status: 'unavailable' }))
				.then((result) => {
					if (disposed || controller.signal.aborted) return;
					cache.set(key, result);
					evictOutsideViewport();
					publish();
				})
				.finally(() => {
					active.delete(key);
					startNext();
				});
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
		for (const controller of active.values()) controller.abort();
		cache.clear();
	}

	return { setVisible, refresh, dispose };
}
