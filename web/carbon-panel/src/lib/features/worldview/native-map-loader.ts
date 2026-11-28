// SPDX-License-Identifier: MPL-2.0
import {
	CHUNKS_PER_TILE,
	loadNativeTile,
	tileKey,
	type MapPoint,
	type NativeTileRequest,
	type TileResult
} from './native-map';

const MAX_ACTIVE_REQUESTS = 2;
const MAX_CACHED_TILES = 96;
const RECHECK_DELAY_MS = 30_000;
/**
 * Live cadence for fully rendered tiles. Minecraft's vanilla autosave runs
 * every five minutes and chunk unloads save continuously, so a 60 s
 * revalidation lands within one save cycle. Conditional requests make a
 * revalidation cheap: an unchanged region answers 304 without a re-render.
 */
const LIVE_RECHECK_DELAY_MS = 60_000;

function recheckAt(result: TileResult, live: boolean, liveRecheckDelayMs: number): number {
	const incomplete =
		result.status === 'saved' &&
		(result.chunks === null || result.chunks < CHUNKS_PER_TILE || result.provisionalChunks > 0);
	if (result.status === 'saved' && !incomplete) {
		return live ? Date.now() + liveRecheckDelayMs : Infinity;
	}
	if (result.status === 'saved' && incomplete) {
		return live ? Date.now() + liveRecheckDelayMs : Infinity;
	}
	return result.status === 'empty' ||
		result.status === 'busy' ||
		result.status === 'unavailable' ||
		result.status === 'unchanged'
		? Date.now() + RECHECK_DELAY_MS
		: Infinity;
}

export interface NativeTileLoaderOptions {
	/** Live defaults on: complete tiles revalidate on a cadence. */
	live?: boolean;
	/** Test seam for the live cadence. */
	liveRecheckDelayMs?: number;
}

/** The visible tiles are ordered from the center out by the viewport. */
export function createNativeTileLoader(
	root: string,
	dimension: string,
	update: (results: ReadonlyMap<string, TileResult>) => void,
	load: (
		root: string,
		tile: MapPoint,
		signal: AbortSignal,
		request?: NativeTileRequest
	) => Promise<TileResult> = loadNativeTile,
	options: NativeTileLoaderOptions = {}
) {
	let visible = new Map<string, MapPoint>();
	const active = new Map<string, AbortController>();
	const cache = new Map<string, { result: TileResult; recheckAt: number }>();
	let recheckTimer: ReturnType<typeof setTimeout> | undefined;
	let disposed = false;
	const liveRecheckDelayMs = options.liveRecheckDelayMs ?? LIVE_RECHECK_DELAY_MS;
	// Live keeps fully rendered tiles revalidating on a cadence; off behaves
	// exactly like the classic map (manual refresh only).
	let live = options.live ?? true;

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
		const eligible: { key: string; tile: MapPoint; request: NativeTileRequest }[] = [];
		for (const [key, tile] of visible) {
			if (active.has(key)) continue;
			const entry = cache.get(key);
			if ((entry?.recheckAt ?? now) > now) continue;
			const request: NativeTileRequest = { dimension };
			// Conditional revalidation only applies to complete tiles that the
			// region validator still covers; gaps re-render unconditionally.
			if (
				entry?.result.status === 'saved' &&
				entry.result.regionModified &&
				entry.result.chunks === CHUNKS_PER_TILE &&
				entry.result.provisionalChunks === 0
			) {
				request.ifNoneMatch = entry.result.regionModified;
			}
			eligible.push({ key, tile, request });
		}
		// Pan into new terrain before rechecking a tile already on screen.
		eligible.sort(({ key: a }, { key: b }) => Number(cache.has(a)) - Number(cache.has(b)));
		for (const { key, tile, request } of eligible) {
			if (active.size >= MAX_ACTIVE_REQUESTS) return;
			const controller = new AbortController();
			active.set(key, controller);
			void load(root, tile, controller.signal, request)
				.catch((): TileResult => ({ status: 'unavailable' }))
				.then((result) => {
					if (disposed || controller.signal.aborted) return;
					if (result.status === 'unchanged') {
						// The region was not rewritten: keep the cached image and
						// reschedule without a re-render.
						const entry = cache.get(key);
						if (entry?.result.status === 'saved') {
							entry.recheckAt = recheckAt(entry.result, live, liveRecheckDelayMs);
							scheduleNext();
							return;
						}
						// The cache entry vanished mid-flight; leave the tile to a
						// later pan or manual refresh.
						scheduleNext();
						return;
					}
					cache.set(key, { result, recheckAt: recheckAt(result, live, liveRecheckDelayMs) });
					evictOutsideViewport();
					publish();
				})
				.finally(() => {
					active.delete(key);
					startNext();
				});
		}
		scheduleNext();
	}

	function scheduleNext() {
		clearTimeout(recheckTimer);
		if (disposed || active.size >= MAX_ACTIVE_REQUESTS) return;
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

	/** Live off freezes fully rendered tiles until the next manual refresh. */
	function setLive(enabled: boolean) {
		if (live === enabled) return;
		live = enabled;
		for (const [key, entry] of cache) {
			if (!visible.has(key)) continue;
			entry.recheckAt = recheckAt(entry.result, live, liveRecheckDelayMs);
		}
		if (live) startNext();
		else scheduleNext();
	}

	function dispose() {
		disposed = true;
		clearTimeout(recheckTimer);
		for (const controller of active.values()) controller.abort();
		cache.clear();
	}

	return { setVisible, refresh, setLive, dispose };
}
