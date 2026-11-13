// SPDX-License-Identifier: MPL-2.0
import { derived, writable } from 'svelte/store';
import type { Server } from '../../proto/carbonpanel/v1/common_pb';

export interface InventoryState {
	servers: Server[];
	hydrated: boolean;
	loading: boolean;
	error: unknown;
}
export interface InventoryRequests {
	list(fullStats: boolean, silent: boolean): Promise<Server[]>;
	detail(id: string, silent: boolean): Promise<Server | undefined>;
}

// Both current RPCs return complete Server messages via toProtoServer. Newer
// zero/false/empty values are authoritative, especially stale join evidence.
// Sequence checks, rather than truthy-field merges, protect richer observations.
export function mergeServerObservation(_previous: Server | undefined, incoming: Server): Server {
	return incoming;
}

export function createServerInventory(
	requests: InventoryRequests,
	timers = {
		setInterval: globalThis.setInterval.bind(globalThis),
		clearInterval: globalThis.clearInterval.bind(globalThis)
	}
) {
	let snapshot: InventoryState = { servers: [], hydrated: false, loading: false, error: null };
	const state = writable(snapshot);
	let epoch = 0;
	let clock = 0;
	let listVersion = 0;
	let outcomeVersion = 0;
	const versions = new Map<string, number>();
	const removed = new Set<string>();
	const pending = new Map<string, Promise<unknown>>();
	let activeRequests = 0;
	const watches = new Map<string, { timer: ReturnType<typeof setInterval>; users: number }>();
	const publish = (patch: Partial<InventoryState>) => {
		snapshot = { ...snapshot, ...patch };
		state.set(snapshot);
	};
	const current = (id: string) => snapshot.servers.find((server) => server.id === id);
	function withServer(server: Server) {
		const existing = snapshot.servers.some((entry) => entry.id === server.id);
		return existing
			? snapshot.servers.map((entry) => (entry.id === server.id ? server : entry))
			: [...snapshot.servers, server];
	}

	function upsert(server: Server) {
		removed.delete(server.id);
		versions.set(server.id, ++clock);
		publish({ servers: withServer(server) });
	}
	function remove(id: string) {
		removed.add(id);
		versions.set(id, ++clock);
		publish({ servers: snapshot.servers.filter((server) => server.id !== id) });
	}
	function invalidate(id: string) {
		versions.set(id, ++clock);
		outcomeVersion = clock;
		// A post-action read must start after the mutation, not reuse an older poll.
		pending.delete(`detail:${id}`);
		pending.delete('summary');
		pending.delete('full');
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
		publish({ servers: [], hydrated: false, loading: false, error: null });
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
					publish({ error, ...(key.startsWith('detail:') ? {} : { hydrated: true }) });
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
	function refresh(fullStats = false, silent = false): Promise<Server[]> {
		return run(fullStats ? 'full' : 'summary', async (sequence, valid) => {
			const incoming = await requests.list(fullStats, silent);
			if (!valid() || sequence < listVersion) return snapshot.servers;
			listVersion = sequence;
			const next = new Map(snapshot.servers.map((server) => [server.id, server]));
			const ids = new Set(incoming.map((server) => server.id));
			for (const server of snapshot.servers) {
				if (!ids.has(server.id) && (versions.get(server.id) ?? 0) <= sequence)
					next.delete(server.id);
			}
			for (const server of incoming) {
				if (removed.has(server.id) || (versions.get(server.id) ?? 0) > sequence) continue;
				next.set(server.id, mergeServerObservation(next.get(server.id), server));
				versions.set(server.id, sequence);
			}
			publish({ servers: [...next.values()], hydrated: true });
			return snapshot.servers;
		});
	}
	function load(id: string, silent = false): Promise<Server | undefined> {
		if (removed.has(id)) return Promise.resolve(undefined);
		return run(`detail:${id}`, async (sequence, valid) => {
			const server = await requests.detail(id, silent);
			if (!valid() || removed.has(id) || (versions.get(id) ?? 0) > sequence) return current(id);
			versions.set(id, sequence);
			const entries = server
				? withServer(mergeServerObservation(current(id), server))
				: snapshot.servers.filter((entry) => entry.id !== id);
			publish({ servers: entries });
			return server;
		});
	}
	function watch(options: { id?: string; fullStats?: boolean; interval?: number } = {}) {
		const { id, fullStats = false, interval = id ? 5000 : 10000 } = options;
		const key = `${id ?? (fullStats ? 'full' : 'summary')}:${interval}`;
		const poll = () => {
			if (!entry || entry.users === 0 || watches.get(key) !== entry) return;
			void (id ? load(id, true) : refresh(fullStats, true)).catch(() => {});
		};
		let entry = watches.get(key);
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
		subscribe: derived(state, (value) => value.servers).subscribe,
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
