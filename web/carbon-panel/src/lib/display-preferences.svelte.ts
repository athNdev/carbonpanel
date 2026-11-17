// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * The single owner of display preferences. Defaults, validation, storage keys,
 * current values and document attributes live here; the shell, preferences
 * dialog, toast host and code editors consume this instance instead of
 * hydrating attributes independently. Stores (not runes) keep the module
 * importable from the plain Bun test runner used by this project.
 */

import { writable, type Readable } from 'svelte/store';

export type ThemeChoice = 'dark' | 'light';
export type MotionChoice = 'full' | 'reduced';
export type DensityChoice = 'comfortable' | 'compact';

// Storage keys are a compatibility contract: keep the existing names.
export const THEME_KEY = 'carbon-theme';
export const MOTION_KEY = 'carbon-motion';
export const DENSITY_KEY = 'carbon-density';

const THEMES: ThemeChoice[] = ['dark', 'light'];
const MOTIONS: MotionChoice[] = ['full', 'reduced'];
const DENSITIES: DensityChoice[] = ['comfortable', 'compact'];

export interface PreferenceStorage {
	getItem(key: string): string | null;
	setItem(key: string, value: string): void;
}

function browserStorage(): PreferenceStorage | null {
	// The `localStorage` property getter itself can throw SecurityError in
	// blocked/sandboxed browser contexts. Acquire lazily inside try: even the
	// typeof check touches the getter.
	try {
		if (typeof localStorage === 'undefined') return null;
		return localStorage;
	} catch {
		return null;
	}
}

function browserDocument(): Document | null {
	return typeof document === 'undefined' ? null : document;
}

/**
 * Create an independent owner. Production uses the exported singleton; tests
 * pass a storage fixture and a document stub to cover unavailable storage and
 * live switching without a browser.
 */
export function createDisplayPreferences(
	storage?: PreferenceStorage | null,
	doc?: Document | null
) {
	const themeValue = writable<ThemeChoice>('dark');
	const motionValue = writable<MotionChoice>('full');
	const densityValue = writable<DensityChoice>('comfortable');

	const useBrowserStorage = storage === undefined;
	let resolvedStore: PreferenceStorage | null | undefined = useBrowserStorage
		? undefined
		: (storage ?? null);
	const target = doc === undefined ? browserDocument() : doc;

	function currentStore(): PreferenceStorage | null {
		if (resolvedStore !== undefined) return resolvedStore;
		resolvedStore = browserStorage();
		return resolvedStore;
	}

	let theme: ThemeChoice = 'dark';
	let motion: MotionChoice = 'full';
	let density: DensityChoice = 'comfortable';
	themeValue.subscribe((value) => (theme = value));
	motionValue.subscribe((value) => (motion = value));
	densityValue.subscribe((value) => (density = value));

	function read(key: string): string | null {
		const store = currentStore();
		if (!store) return null;
		try {
			return store.getItem(key);
		} catch {
			// Private mode / unavailable storage: defaults stand.
			return null;
		}
	}

	function write(key: string, value: string) {
		const store = currentStore();
		if (!store) return;
		try {
			store.setItem(key, value);
		} catch {
			// Preferences are best effort; the in-memory value still applies.
		}
	}

	function apply() {
		if (!target) return;
		target.documentElement.setAttribute('data-carbon-theme', theme);
		target.documentElement.setAttribute('data-carbon-motion', motion);
		target.documentElement.setAttribute('data-carbon-density', density);
	}

	function hydrate() {
		const storedTheme = read(THEME_KEY);
		const storedMotion = read(MOTION_KEY);
		const storedDensity = read(DENSITY_KEY);
		if (THEMES.includes(storedTheme as ThemeChoice)) themeValue.set(storedTheme as ThemeChoice);
		if (MOTIONS.includes(storedMotion as MotionChoice))
			motionValue.set(storedMotion as MotionChoice);
		if (DENSITIES.includes(storedDensity as DensityChoice))
			densityValue.set(storedDensity as DensityChoice);
		apply();
	}

	function setTheme(next: ThemeChoice) {
		if (!THEMES.includes(next)) return;
		themeValue.set(next);
		write(THEME_KEY, next);
		apply();
	}

	function setMotion(next: MotionChoice) {
		if (!MOTIONS.includes(next)) return;
		motionValue.set(next);
		write(MOTION_KEY, next);
		apply();
	}

	function setDensity(next: DensityChoice) {
		if (!DENSITIES.includes(next)) return;
		densityValue.set(next);
		write(DENSITY_KEY, next);
		apply();
	}

	function reset() {
		themeValue.set('dark');
		motionValue.set('full');
		densityValue.set('comfortable');
		write(THEME_KEY, theme);
		write(MOTION_KEY, motion);
		write(DENSITY_KEY, density);
		apply();
	}

	return {
		theme: themeValue as Readable<ThemeChoice>,
		motion: motionValue as Readable<MotionChoice>,
		density: densityValue as Readable<DensityChoice>,
		hydrate,
		apply,
		setTheme,
		setMotion,
		setDensity,
		reset
	};
}

export const displayPreferences = createDisplayPreferences();

// Named readable stores let Svelte components auto-subscribe with `$theme`,
// `$motion` and `$density` while tests can create independent instances.
export const theme = displayPreferences.theme;
export const motion = displayPreferences.motion;
export const density = displayPreferences.density;
