// SPDX-License-Identifier: MPL-2.0
import { derived, writable } from 'svelte/store';
import type { WorldEntry } from '../../proto/carbonpanel/v1/world_pb';

export interface WorldLibraryState {
	entries: WorldEntry[];
	hydrated: boolean;
	loading: boolean;
	error: unknown;
}

export interface WorldLibraryInventoryRequests {
	list(silent: boolean): Promise<WorldEntry[]>;
	detail(id: string, silent: boolean): Promise<WorldEntry | undefined>;
}

interface WatchEntry {
	timer: ReturnType<typeof setInterval>;
	users: number;
}

/**
 * Every RPC returns a complete WorldEntry. Newer zero/false/empty values are
 * authoritative (there are no partial join observations to preserve here), so
 * the incoming message replaces the cached one. Sequence checks, not truthy
 * merges, protect a richer or newer observation from an older response.
 */
export function mergeWorldObservation(
	_previous: WorldEntry | undefined,
	incoming: WorldEntry
): WorldEntry {
	return incoming;
}

export function createWorldLibraryInventory(
	requests: WorldLibraryInventoryRequests,
	timers = {
		setInterval: globalThis.setInterval.bind(globalThis),
		clearInterval: globalThis.clearInterval.bind(globalThis)
	}
) {
	let snapshot: WorldLibraryState = { entries: [], hydrated: false, loading: false, error: null };
	const state = writable(snapshot);
	let epoch = 0;
	let clock = 0;
	let listVersion = 0;
	let outcomeVersion = 0;
	// eslint-disable-next-line svelte/prefer-svelte-reactivity -- Private bookkeeping; only writable snapshots are reactive.
	const versions = new Map<string, number>();
	// eslint-disable-next-line svelte/prefer-svelte-reactivity -- Private bookkeeping; only writable snapshots are reactive.
	const removed = new Set<string>();
	// eslint-disable-next-line svelte/prefer-svelte-reactivity -- Private bookkeeping; only writable snapshots are reactive.
	const pending = new Map<string, Promise<unknown>>();
	let activeRequests = 0;
	// eslint-disable-next-line svelte/prefer-svelte-reactivity -- Private bookkeeping; only writable snapshots are reactive.
	const watches = new Map<string, WatchEntry>();

	const publish = (patch: Partial<WorldLibraryState>) => {
		snapshot = { ...snapshot, ...patch };
		state.set(snapshot);
	};
	const current = (id: string) => snapshot.entries.find((entry) => entry.id === id);
	function withEntry(entry: WorldEntry) {
		return snapshot.entries.some((existing) => existing.id === entry.id)
			? snapshot.entries.map((existing) => (existing.id === entry.id ? entry : existing))
			: [...snapshot.entries, entry];
	}

	function upsert(entry: WorldEntry) {
		removed.delete(entry.id);
		versions.set(entry.id, ++clock);
		publish({ entries: withEntry(entry) });
	}

	function remove(id: string) {
		removed.add(id);
		versions.set(id, ++clock);
		publish({ entries: snapshot.entries.filter((entry) => entry.id !== id) });
	}

	function invalidate(id: string) {
		versions.set(id, ++clock);
		outcomeVersion = clock;
		// A post-action read must start after the mutation, never reuse an older poll.
		pending.delete(`detail:${id}`);
		pending.delete('list');
	}

	function reset() {
		epoch++;
		clock++;
		listVersion = clock;
		outcomeVersion = clock;
		pending.clear();
		versions.clear();
		removed.clear();
		activeRequests = 0;
		publish({ entries: [], hydrated: false, loading: false, error: null });
	}

	function run<T>(
		key: string,
		operation: (sequence: number, valid: () => boolean) => Promise<T>
	): Promise<T> {
		const existing = pending.get(key) as Promise<T> | undefined;
		if (existing) return existing;
		const startedEpoch = epoch;
		const sequence = ++clock;
		activeRequests++;
		publish({ loading: true });
		const valid = () => startedEpoch === epoch;
		const request = operation(sequence, valid)
			.then((result) => {
				if (!key.startsWith('detail:') && valid() && sequence >= outcomeVersion) {
					outcomeVersion = sequence;
					publish({ error: null });
				}
				return result;
			})
			.catch((error) => {
				if (!key.startsWith('detail:') && valid() && sequence >= outcomeVersion) {
					outcomeVersion = sequence;
					publish({ error, hydrated: true });
				}
				throw error;
			})
			.finally(() => {
				if (pending.get(key) === request) pending.delete(key);
				if (valid()) {
					activeRequests--;
					publish({ loading: activeRequests > 0 });
				}
			});
		pending.set(key, request);
		return request;
	}

	function refresh(silent = false): Promise<WorldEntry[]> {
		return run('list', async (sequence, valid) => {
			const incoming = await requests.list(silent);
			if (!valid() || sequence < listVersion) return snapshot.entries;
			listVersion = sequence;
			// eslint-disable-next-line svelte/prefer-svelte-reactivity -- Private bookkeeping; only writable snapshots are reactive.
			const next = new Map(snapshot.entries.map((entry) => [entry.id, entry]));
			// eslint-disable-next-line svelte/prefer-svelte-reactivity -- Private bookkeeping; only writable snapshots are reactive.
			const ids = new Set(incoming.map((entry) => entry.id));
			for (const entry of snapshot.entries) {
				if (!ids.has(entry.id) && (versions.get(entry.id) ?? 0) <= sequence) {
					next.delete(entry.id);
					versions.set(entry.id, sequence);
				}
			}
			for (const entry of incoming) {
				if (removed.has(entry.id) || (versions.get(entry.id) ?? 0) > sequence) continue;
				next.set(entry.id, mergeWorldObservation(next.get(entry.id), entry));
				versions.set(entry.id, sequence);
			}
			publish({ entries: [...next.values()], hydrated: true });
			return snapshot.entries;
		});
	}

	function load(id: string, silent = false): Promise<WorldEntry | undefined> {
		if (removed.has(id)) return Promise.resolve(undefined);
		return run(`detail:${id}`, async (sequence, valid) => {
			const entry = await requests.detail(id, silent);
			// A complete list also establishes absence, including IDs not yet cached.
			if (
				!valid() ||
				sequence < listVersion ||
				removed.has(id) ||
				(versions.get(id) ?? 0) > sequence
			)
				return current(id);
			versions.set(id, sequence);
			const entries = entry
				? withEntry(mergeWorldObservation(current(id), entry))
				: snapshot.entries.filter((candidate) => candidate.id !== id);
			publish({ entries });
			return entry;
		});
	}

	function watch(options: { id?: string; interval?: number } = {}) {
		const { id, interval = id ? 5000 : 10000 } = options;
		const key = `${id ?? 'list'}:${interval}`;
		let entry = watches.get(key);
		const poll = () => {
			if (!entry || entry.users === 0 || watches.get(key) !== entry) return;
			void (id ? load(id, true) : refresh(true)).catch(() => undefined);
		};
		if (!entry) {
			entry = { timer: timers.setInterval(poll, interval), users: 0 };
			watches.set(key, entry);
		}
		entry.users++;
		let closed = false;
		return () => {
			if (closed) return;
			closed = true;
			if (--entry.users === 0) {
				timers.clearInterval(entry.timer);
				watches.delete(key);
			}
		};
	}

	return {
		subscribe: derived(state, (value) => value.entries).subscribe,
		state: { subscribe: state.subscribe },
		refresh,
		load,
		upsert,
		remove,
		invalidate,
		reset,
		watch,
		generation: () => epoch,
		get: current
	};
}

export type WorldLibraryInventory = ReturnType<typeof createWorldLibraryInventory>;
