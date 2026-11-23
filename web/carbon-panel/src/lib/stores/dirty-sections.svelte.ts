// SPDX-License-Identifier: MPL-2.0

/**
 * OSS-464: registry of server-view sections that can hold unsaved edits.
 *
 * Each editable section (Server Settings, Server Configuration) registers an
 * entry on mount and unregisters on unmount, so the floating action bar and
 * the lifecycle gate always describe live sections only — a dead section can
 * never gate an action again. Registration is idempotent by id: registering a
 * live id replaces the earlier entry instead of duplicating it.
 *
 * Classic stores (not runes) keep this module importable from the plain Bun
 * test runner, matching display-preferences.svelte.ts. Live dirtiness stays
 * reactive through each section's own Svelte 5 state: reactive consumers (the
 * floating action bar) read `entry.isDirty()` inside a `$derived`, which
 * tracks the section's signals through the registered closure. Imperative
 * consumers (the lifecycle gate) read the current values at decision time.
 */

import { derived, get, writable } from 'svelte/store';

export interface DirtySectionEntry {
	/** Stable id — doubles as the server-view tab id so bar chips can deep-link. */
	readonly id: string;
	/** Human label used by the bar chips and the refusal toasts. */
	readonly label: string;
	/** True when the section currently holds unsaved edits. */
	readonly isDirty: () => boolean;
	/** Persist the section's edits. Resolves on success, rejects on failure. */
	readonly save: () => Promise<void>;
	/** Revert to the loaded snapshot (client-side only, no RPC). */
	readonly undo: () => void;
	/** True while the section's save request is in flight. */
	readonly saving: () => boolean;
}

interface RegistryState {
	entries: readonly DirtySectionEntry[];
	/** Bumped when a gated action is refused; the bar shakes on each bump. */
	shakeTick: number;
}

const registry = writable<RegistryState>({ entries: [], shakeTick: 0 });

export const dirtySections = {
	/** Idempotent by id: registering a live id replaces the earlier entry. */
	register(entry: DirtySectionEntry): void {
		registry.update((state) => {
			const index = state.entries.findIndex((existing) => existing.id === entry.id);
			if (index === -1) return { ...state, entries: [...state.entries, entry] };
			const entries = [...state.entries];
			entries[index] = entry;
			return { ...state, entries };
		});
	},
	/** Unmount cleanup: the entry stops existing for the bar and the gate. */
	unregister(id: string): void {
		registry.update((state) => ({
			...state,
			entries: state.entries.filter((existing) => existing.id !== id)
		}));
	},
	/** Ask the floating bar to shake (refusal feedback for a gated action). */
	requestShake(): void {
		registry.update((state) => ({ ...state, shakeTick: state.shakeTick + 1 }));
	}
};

/** Every registered section, in registration order. Subscribe from the bar. */
export const dirtySectionEntries = derived(registry, ($registry) => $registry.entries);

/** Current shake counter; each bump re-fires the bar's shake animation. */
export const dirtySectionShakeTick = derived(registry, ($registry) => $registry.shakeTick);

/** Labels of the sections that currently hold unsaved edits, read at decision time. */
export function dirtySectionLabels(): string[] {
	return get(registry)
		.entries.filter((entry) => entry.isDirty())
		.map((entry) => entry.label);
}

/** True when any registered section currently holds unsaved edits. */
export function hasDirtySections(): boolean {
	return get(registry).entries.some((entry) => entry.isDirty());
}
