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

export type Palette = 'dark' | 'light';
export type ThemeChoice = Palette | 'system';
export type MotionChoice = 'full' | 'reduced';
export type DensityChoice = 'comfortable' | 'compact';

// Storage keys are a compatibility contract: keep the existing names.
export const THEME_KEY = 'carbon-theme';
export const MOTION_KEY = 'carbon-motion';
export const SERVER_METRICS_KEY = 'carbon-server-metrics-expanded';
export const DENSITY_KEY = 'carbon-density';

const THEMES: ThemeChoice[] = ['system', 'dark', 'light'];
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
	const themeValue = writable<Palette>('dark');
	const themeChoice = writable<ThemeChoice>('system');
	const motionValue = writable<MotionChoice>('full');
	const metricsExpanded = writable(true);
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

	let choice: ThemeChoice = 'system';
	let systemMedia: MediaQueryList | undefined;
	let motion: MotionChoice = 'full';
	let density: DensityChoice = 'comfortable';
	themeChoice.subscribe((value) => (choice = value));
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
		const theme = choice === 'system' ? (systemMedia?.matches ? 'light' : 'dark') : choice;
		themeValue.set(theme);
		if (!target) return;
		const root = target.documentElement;
		// A theme flip changes color/background/border/shadow on nearly every
		// element at once, so every transition on those properties fires in the
		// same frame and the switch smears instead of snapping. Skip the
		// transition for the swap itself; ordinary interactions keep theirs.
		const themeChanged =
			typeof root.getAttribute === 'function' && root.getAttribute('data-carbon-theme') !== theme;
		if (themeChanged) suppressTransitionsForThemeSwap(target);
		root.setAttribute('data-carbon-theme', theme);
		root.classList?.toggle('dark', theme === 'dark');
		root.setAttribute('data-carbon-motion', motion);
		root.setAttribute('data-carbon-density', density);
	}

	/**
	 * Disable transitions for one frame, force a style flush so the new palette
	 * commits while the override still applies, then restore them after paint.
	 * Test document stubs without a head/view go straight through.
	 */
	function suppressTransitionsForThemeSwap(doc: Document) {
		const head = doc.head;
		const view = doc.defaultView;
		if (
			!head ||
			typeof doc.createElement !== 'function' ||
			typeof view?.requestAnimationFrame !== 'function'
		)
			return;
		const style = doc.createElement('style');
		style.textContent = '*,*::before,*::after{transition:none !important}';
		head.appendChild(style);
		// Reading a layout property flushes style synchronously.
		void doc.body?.offsetHeight;
		view.requestAnimationFrame(() => {
			view.requestAnimationFrame(() => style.remove());
		});
	}

	function hydrate() {
		const storedTheme = read(THEME_KEY);
		const storedMotion = read(MOTION_KEY);
		const storedDensity = read(DENSITY_KEY);
		const storedMetrics = read(SERVER_METRICS_KEY);
		if (storedMetrics === 'true' || storedMetrics === 'false')
			metricsExpanded.set(storedMetrics === 'true');
		themeChoice.set(
			THEMES.includes(storedTheme as ThemeChoice) ? (storedTheme as ThemeChoice) : 'system'
		);
		if (MOTIONS.includes(storedMotion as MotionChoice))
			motionValue.set(storedMotion as MotionChoice);
		if (DENSITIES.includes(storedDensity as DensityChoice))
			densityValue.set(storedDensity as DensityChoice);
		apply();
	}

	function watchSystemTheme() {
		systemMedia = target?.defaultView?.matchMedia?.('(prefers-color-scheme: light)');
		const onChange = () => {
			if (choice === 'system') apply();
		};
		systemMedia?.addEventListener('change', onChange);
		apply();
		return () => systemMedia?.removeEventListener('change', onChange);
	}

	function setTheme(next: ThemeChoice) {
		if (!THEMES.includes(next)) return;
		themeChoice.set(next);
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

	function setMetricsExpanded(expanded: boolean) {
		metricsExpanded.set(expanded);
		write(SERVER_METRICS_KEY, String(expanded));
	}

	function reset() {
		setMetricsExpanded(true);
		themeChoice.set('system');
		motionValue.set('full');
		densityValue.set('comfortable');
		write(THEME_KEY, choice);
		write(MOTION_KEY, motion);
		write(DENSITY_KEY, density);
		apply();
	}

	return {
		metricsExpanded: metricsExpanded as Readable<boolean>,
		setMetricsExpanded,
		theme: themeValue as Readable<Palette>,
		themeChoice: themeChoice as Readable<ThemeChoice>,
		watchSystemTheme,
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
