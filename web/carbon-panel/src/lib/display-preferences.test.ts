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
