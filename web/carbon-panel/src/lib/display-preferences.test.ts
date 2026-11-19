// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { get } from 'svelte/store';
import {
	createDisplayPreferences,
	THEME_KEY,
	MOTION_KEY,
	DENSITY_KEY,
	type PreferenceStorage
} from './display-preferences.svelte';

// CSS contract assertions kept from the original file: the attributes owned by
// this store must still have the documented effects in app.css.
const css = readFileSync(new URL('../app.css', import.meta.url), 'utf8').replace(/\r\n/g, '\n');

test('reduced motion kills animation via the data attribute, not just the media query', () => {
	assert.ok(
		css.includes("[data-carbon-motion='reduced']"),
		'reduced motion must have a data-attribute kill-switch in app.css'
	);
	assert.match(css, /animation-duration: 0\.01ms !important/);
});

test('compact density tightens the control tokens', () => {
	assert.ok(
		css.includes("[data-carbon-density='compact']"),
		'compact density must override tokens in app.css'
	);
	assert.match(css, /--carbon-control-md: 2rem/);
});

test('mono eyebrow labels share one tracking/color contract', () => {
	for (const utility of ['.mono-label ', '.mono-label-strong', '.mono-label-accent']) {
		assert.ok(css.includes(utility), `app.css must define ${utility.trim()}`);
	}
	assert.match(css, /letter-spacing: 0\.04em/);
});

function fixture(seed: Record<string, string> = {}, failing = false) {
	const values = new Map(Object.entries(seed));
	const storage: PreferenceStorage = {
		getItem(key) {
			if (failing) throw new Error('storage unavailable');
			return values.get(key) ?? null;
		},
		setItem(key, value) {
			if (failing) throw new Error('storage unavailable');
			values.set(key, value);
		}
	};
	const attributes = new Map<string, string>();
	const documentStub = {
		documentElement: {
			setAttribute(name: string, value: string) {
				attributes.set(name, value);
			}
		}
	} as unknown as Document;
	return { storage, documentStub, attributes, values };
}

test('fresh defaults are dark/full/comfortable and apply the same document attributes', () => {
	const { storage, documentStub, attributes } = fixture();
	const prefs = createDisplayPreferences(storage, documentStub);
	prefs.hydrate();
	assert.equal(get(prefs.theme), 'dark');
	assert.equal(get(prefs.motion), 'full');
	assert.equal(get(prefs.density), 'comfortable');
	assert.equal(attributes.get('data-carbon-theme'), 'dark');
	assert.equal(attributes.get('data-carbon-motion'), 'full');
	assert.equal(attributes.get('data-carbon-density'), 'comfortable');
});

test('stored light and dark values hydrate and apply', () => {
	for (const theme of ['light', 'dark'] as const) {
		const { storage, documentStub, attributes, values } = fixture({
			[THEME_KEY]: theme,
			[MOTION_KEY]: 'reduced',
			[DENSITY_KEY]: 'compact'
		});
		const prefs = createDisplayPreferences(storage, documentStub);
		prefs.hydrate();
		assert.equal(get(prefs.theme), theme);
		assert.equal(get(prefs.motion), 'reduced');
		assert.equal(get(prefs.density), 'compact');
		assert.equal(attributes.get('data-carbon-theme'), theme);
		assert.equal(attributes.get('data-carbon-motion'), 'reduced');
		assert.equal(attributes.get('data-carbon-density'), 'compact');
		assert.equal(values.get(THEME_KEY), theme);
	}
});

test('invalid stored values fall back to defaults without changing storage', () => {
	const { storage, documentStub, attributes, values } = fixture({
		[THEME_KEY]: 'neon',
		[MOTION_KEY]: 'fast',
		[DENSITY_KEY]: 'loose'
	});
	const prefs = createDisplayPreferences(storage, documentStub);
	prefs.hydrate();
	assert.equal(get(prefs.theme), 'dark');
	assert.equal(get(prefs.motion), 'full');
	assert.equal(get(prefs.density), 'comfortable');
	assert.equal(attributes.get('data-carbon-theme'), 'dark');
	assert.equal(values.get(THEME_KEY), 'neon'); // hydration never rewrites invalid stored data
});

test('unavailable storage tolerates reads and writes without crashing', () => {
	const { storage, documentStub, attributes } = fixture({}, true);
	const prefs = createDisplayPreferences(storage, documentStub);
	prefs.hydrate();
	prefs.setTheme('light');
	prefs.setMotion('reduced');
	prefs.setDensity('compact');
	prefs.reset();
	assert.equal(get(prefs.theme), 'dark');
	assert.equal(attributes.get('data-carbon-density'), 'comfortable');
});

test('live switches and reset update state, storage and attributes together', () => {
	const { storage, documentStub, attributes, values } = fixture();
	const prefs = createDisplayPreferences(storage, documentStub);
	prefs.setTheme('light');
	assert.equal(get(prefs.theme), 'light');
	assert.equal(values.get(THEME_KEY), 'light');
	assert.equal(attributes.get('data-carbon-theme'), 'light');
	prefs.setMotion('reduced');
	prefs.setDensity('compact');
	assert.equal(attributes.get('data-carbon-motion'), 'reduced');
	assert.equal(attributes.get('data-carbon-density'), 'compact');
	prefs.reset();
	assert.equal(get(prefs.theme), 'dark');
	assert.equal(get(prefs.motion), 'full');
	assert.equal(get(prefs.density), 'comfortable');
	assert.equal(values.get(THEME_KEY), 'dark');
	assert.equal(values.get(MOTION_KEY), 'full');
	assert.equal(values.get(DENSITY_KEY), 'comfortable');
	assert.equal(attributes.get('data-carbon-theme'), 'dark');
});

test('a theme flip suppresses transitions for one frame, then restores them', () => {
	const attributes = new Map<string, string>();
	const appended: Array<{ textContent: string; removed: boolean; remove(): void }> = [];
	const frames: Array<() => void> = [];
	const documentStub = {
		documentElement: {
			getAttribute: (name: string) => attributes.get(name) ?? null,
			setAttribute: (name: string, value: string) => attributes.set(name, value)
		},
		head: { appendChild: (node: unknown) => appended.push(node as never) },
		createElement: () => {
			const node = {
				textContent: '',
				removed: false,
				remove() {
					this.removed = true;
				}
			};
			return node;
		},
		body: { offsetHeight: 0 },
		defaultView: {
			requestAnimationFrame: (callback: () => void) => {
				frames.push(callback);
				return frames.length;
			}
		}
	} as unknown as Document;
	const prefs = createDisplayPreferences({ getItem: () => null, setItem: () => {} }, documentStub);

	prefs.setTheme('light');
	assert.equal(attributes.get('data-carbon-theme'), 'light');
	assert.equal(appended.length, 1, 'the swap must inject one transition override');
	assert.match(appended[0].textContent, /transition:none !important/);

	frames.shift()?.();
	frames.shift()?.();
	assert.equal(appended[0].removed, true, 'transitions must be restored after the swap paints');

	// A density change under the same theme must not disable transitions.
	prefs.setDensity('compact');
	assert.equal(appended.length, 1);
});

test('rapid theme flips own separate overrides and remove all of them', () => {
	const attributes = new Map<string, string>();
	const appended: Array<{ textContent: string; removed: boolean; remove(): void }> = [];
	const frames: Array<() => void> = [];
	const documentStub = {
		documentElement: {
			getAttribute: (name: string) => attributes.get(name) ?? null,
			setAttribute: (name: string, value: string) => attributes.set(name, value)
		},
		head: { appendChild: (node: unknown) => appended.push(node as never) },
		createElement: () => ({
			textContent: '',
			removed: false,
			remove() {
				this.removed = true;
			}
		}),
		body: { offsetHeight: 0 },
		defaultView: {
			requestAnimationFrame: (callback: () => void) => {
				frames.push(callback);
				return frames.length;
			}
		}
	} as unknown as Document;
	const prefs = createDisplayPreferences({ getItem: () => null, setItem: () => {} }, documentStub);

	prefs.setTheme('light');
	prefs.setTheme('dark');
	assert.equal(attributes.get('data-carbon-theme'), 'dark');
	assert.equal(appended.length, 2, 'each flip owns its own override');

	// Drain both two-frame chains; no style node may leak across flips.
	for (let i = 0; i < 4; i++) frames.shift()?.();
	assert.equal(
		appended.every((node) => node.removed),
		true,
		'every override is removed'
	);
});

test('login-before-shell is safe: hydration with no document does not throw', () => {
	const { storage } = fixture({ [THEME_KEY]: 'light' });
	const prefs = createDisplayPreferences(storage, null);
	prefs.hydrate();
	assert.equal(get(prefs.theme), 'light');
	prefs.setTheme('dark');
	assert.equal(get(prefs.theme), 'dark');
});

// Narrow new-debt ratchet: files migrated in this change must not introduce
// hardcoded six-digit hex colors. Purposeful third-party/status colors and the
// remaining legacy shell colors are out of scope for this ratchet.
test('a throwing localStorage property getter cannot break import or acquisition', async () => {
	const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');
	Object.defineProperty(globalThis, 'localStorage', {
		configurable: true,
		get() {
			throw new Error('blocked by security policy');
		}
	});
	try {
		const { documentStub } = fixture();
		// This test file imports the module at the top with a normal getter;
		// module initialization therefore never touches localStorage. We pin
		// acquisition with the getter throwing from here on.
		const prefs = createDisplayPreferences(undefined, documentStub);
		prefs.hydrate();
		prefs.setTheme('light');
		assert.equal(get(prefs.theme), 'light');
	} finally {
		if (descriptor) Object.defineProperty(globalThis, 'localStorage', descriptor);
		else delete (globalThis as { localStorage?: unknown }).localStorage;
	}
});

// Source-seam check (no browser harness in this runner): the live theme effect
// in each editor must use only an already-loaded runtime, never bootstrap
// Monaco. A closed/unmounted editor must not trigger a runtime fetch or a
// failed bootstrap, and disposal must clear the active reference (no leaked
// runtime/model).
test('editor theme effects never bootstrap Monaco and clear active runtimes', () => {
	for (const file of [
		'./components/files/file-editor-dialog.svelte',
		'./components/ui/code-editor.svelte'
	]) {
		const source = readFileSync(new URL(file, import.meta.url), 'utf8');
		const marker = source.indexOf('const next = $preferenceTheme');
		assert.notEqual(marker, -1, `${file} must follow the shared theme`);
		const effect = source.slice(Math.max(0, marker - 120), marker + 260);
		assert.doesNotMatch(
			effect,
			/loadEditorRuntime/,
			`${file} must not lazy-load Monaco for theming`
		);
		assert.ok(
			/activeRuntime|if \(monaco\)/.test(effect),
			`${file} must guard theme changes on an already-loaded runtime`
		);
	}
	const dialog = readFileSync(
		new URL('./components/files/file-editor-dialog.svelte', import.meta.url),
		'utf8'
	);
	assert.match(dialog, /activeRuntime = null/, 'disposal must release the active runtime');
});

test('migrated surfaces introduce no new hardcoded six-digit hex colors', () => {
	const files = [
		'./components/PreferencesDialog.svelte',
		'./components/ui/sonner/sonner.svelte',
		'./display-preferences.svelte.ts'
	];
	for (const file of files) {
		const source = readFileSync(new URL(file, import.meta.url), 'utf8');
		const hex = source.match(/#[0-9a-fA-F]{6}\b/g) || [];
		assert.deepEqual(hex, [], `${file} introduced hardcoded hex colors: ${hex.join(', ')}`);
	}
});
