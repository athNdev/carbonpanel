// SPDX-License-Identifier: MPL-2.0
/**
 * Page-owned, refcounted world-view polling. No global timer is started at
 * import time: a route calls watch()/watchStatus() and must call the returned
 * cleanup when it unmounts. Late responses cannot overwrite newer observations
 * or data from a previous session (reset()).
 */

import { derived, writable, type Readable } from 'svelte/store';
import type { WorldRef, WorldViewTarget } from '$lib/proto/carbonpanel/v1/worldview_pb';

export interface WorldViewRequests {
	listTargets(silent: boolean): Promise<WorldViewTarget[]>;
	status(
		serverId: string,
		worldRef: WorldRef | undefined,
		silent: boolean
	): Promise<WorldViewTarget | undefined>;
}

export interface WorldViewState {
	targets: WorldViewTarget[];
	hydrated: boolean;
	loading: boolean;
	error: unknown;
	statuses: Record<string, WorldViewTarget | undefined>;
	statusLoading: Record<string, boolean>;
	statusErrors: Record<string, unknown>;
}

interface Timers {
	setInterval: typeof globalThis.setInterval;
	clearInterval: typeof globalThis.clearInterval;
}

const DEFAULT_TIMERS: Timers = {
	setInterval: globalThis.setInterval.bind(globalThis),
	clearInterval: globalThis.clearInterval.bind(globalThis)
};

export function createWorldViewStore(requests: WorldViewRequests, timers: Timers = DEFAULT_TIMERS) {
	const initial: WorldViewState = {
		targets: [],
		hydrated: false,
		loading: false,
		error: null,
		statuses: {},
		statusLoading: {},
		statusErrors: {}
	};
	let snapshot = initial;
	const state = writable(snapshot);
	let epoch = 0;
	let clock = 0;
	let listVersion = 0;
	// eslint-disable-next-line svelte/prefer-svelte-reactivity -- Private bookkeeping; only the writable snapshot is reactive.
	const statusVersions = new Map<string, number>();
	let listInFlight: Promise<WorldViewTarget[]> | null = null;
	// eslint-disable-next-line svelte/prefer-svelte-reactivity -- Private bookkeeping.
	const statusPending = new Map<string, Promise<WorldViewTarget | undefined>>();
	let activeLists = 0;
	// eslint-disable-next-line svelte/prefer-svelte-reactivity -- Private bookkeeping.
	const targetWatches = new Map<number, { timer: ReturnType<typeof setInterval>; users: number }>();
	// eslint-disable-next-line svelte/prefer-svelte-reactivity -- Private bookkeeping.
	const statusWatches = new Map<
		string,
		{
			timer: ReturnType<typeof setInterval>;
			users: number;
			key: string;
			serverId: string;
			worldRef: WorldRef | undefined;
		}
	>();

	const publish = (patch: Partial<WorldViewState>) => {
		snapshot = { ...snapshot, ...patch };
		state.set(snapshot);
	};

	function refreshTargets(silent = false): Promise<WorldViewTarget[]> {
		if (listInFlight) return listInFlight;
		const startedEpoch = epoch;
		const sequence = ++clock;
		activeLists++;
		publish({ loading: true });
		const valid = () => startedEpoch === epoch;
		const request = requests
			.listTargets(silent)
			.then((targets) => {
				if (valid() && sequence >= listVersion) {
					listVersion = sequence;
					publish({ targets, hydrated: true, error: null });
				}
				return targets;
			})
			.catch((error) => {
				if (valid() && sequence >= listVersion) {
					listVersion = sequence;
					publish({ error, hydrated: true });
				}
				throw error;
			})
			.finally(() => {
				if (listInFlight === request) listInFlight = null;
				if (valid()) {
					activeLists = Math.max(0, activeLists - 1);
					publish({ loading: activeLists > 0 });
				}
			});
		listInFlight = request;
		return request;
	}

	function loadStatus(
		key: string,
		serverId: string,
		worldRef: WorldRef | undefined,
		silent = false
	): Promise<WorldViewTarget | undefined> {
		const existing = statusPending.get(key);
		if (existing) return existing;
		const startedEpoch = epoch;
		const sequence = ++clock;
		publish({ statusLoading: { ...snapshot.statusLoading, [key]: true } });
		const valid = () => startedEpoch === epoch;
		const request = requests
			.status(serverId, worldRef, silent)
			.then((target) => {
				if (valid() && sequence >= listVersion && sequence >= (statusVersions.get(key) ?? 0)) {
					statusVersions.set(key, sequence);
					publish({
						statuses: { ...snapshot.statuses, [key]: target },
						statusErrors: { ...snapshot.statusErrors, [key]: null }
					});
				}
				return target;
			})
			.catch((error) => {
				if (valid() && sequence >= (statusVersions.get(key) ?? 0)) {
					statusVersions.set(key, sequence);
					publish({ statusErrors: { ...snapshot.statusErrors, [key]: error } });
				}
				throw error;
			})
			.finally(() => {
				if (statusPending.get(key) === request) statusPending.delete(key);
				if (valid()) publish({ statusLoading: { ...snapshot.statusLoading, [key]: false } });
			});
		statusPending.set(key, request);
		return request;
	}

	/** Refcounted target poll. The timer stops when the last subscriber leaves. */
	function watchTargets(interval = 15_000) {
		let entry = targetWatches.get(interval);
		if (!entry) {
			entry = {
				timer: timers.setInterval(() => {
					if (entry && entry.users > 0) void refreshTargets(true).catch(() => {});
				}, interval),
				users: 0
			};
			targetWatches.set(interval, entry);
		}
		const watch = entry;
		watch.users++;
		let closed = false;
		return () => {
			if (closed) return;
			closed = true;
			if (--watch.users === 0 && targetWatches.get(interval) === watch) {
				timers.clearInterval(watch.timer);
				targetWatches.delete(interval);
			}
		};
	}

	/** Refcounted per-world status poll. Keys are independent. */
	function watchStatus(
		key: string,
		serverId: string,
		worldRef: WorldRef | undefined,
		interval = 5_000
	) {
		const identity = `${key}:${interval}`;
		let entry = statusWatches.get(identity);
		if (!entry) {
			const created = {
				timer: 0 as unknown as ReturnType<typeof setInterval>,
				users: 0,
				key,
				serverId,
				worldRef
			};
			created.timer = timers.setInterval(() => {
				if (created.users > 0 && statusWatches.get(identity) === created) {
					void loadStatus(created.key, created.serverId, created.worldRef, true).catch(() => {});
				}
			}, interval);
			entry = created;
			statusWatches.set(identity, entry);
		}
		const watch = entry;
		watch.users++;
		let closed = false;
		return () => {
			if (closed) return;
			closed = true;
			if (--watch.users === 0 && statusWatches.get(identity) === watch) {
				timers.clearInterval(watch.timer);
				statusWatches.delete(identity);
			}
		};
	}

	/** Invalidate status after an action so the next poll is a fresh read. */
	function invalidateStatus(key: string) {
		statusVersions.set(key, ++clock);
		statusPending.delete(key);
	}

	function reset() {
		epoch++;
		clock++;
		listVersion = clock;
		statusVersions.clear();
		listInFlight = null;
		statusPending.clear();
		activeLists = 0;
		for (const entry of targetWatches.values()) timers.clearInterval(entry.timer);
		targetWatches.clear();
		for (const entry of statusWatches.values()) timers.clearInterval(entry.timer);
		statusWatches.clear();
		publish({
			targets: [],
			hydrated: false,
			loading: false,
			error: null,
			statuses: {},
			statusLoading: {},
			statusErrors: {}
		});
	}

	return {
		subscribe: state.subscribe,
		state: { subscribe: state.subscribe } as Readable<WorldViewState>,
		targets: derived(state, (value) => value.targets),
		refreshTargets,
		loadStatus,
		watchTargets,
		watchStatus,
		invalidateStatus,
		reset
	};
}

export type WorldViewStore = ReturnType<typeof createWorldViewStore>;
