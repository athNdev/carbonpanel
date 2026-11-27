// SPDX-License-Identifier: MPL-2.0
// Test-only browser environment for public-UI-seam DOM tests.
//
// Bun's test runner has no DOM. Happy DOM supplies standards-based globals and
// a small Bun plugin compiles .svelte files with Svelte's client generator,
// so tests mount the real components rather than asserting on source text.
// This module is intentionally plain JS: it is imported for side effects
// before any dynamic component import, and is not part of the shipped bundle.
// eslint-disable-next-line @typescript-eslint/ban-ts-comment
// @ts-nocheck -- Bun test-only harness; Bun plugin/global types are not in this app's tsconfig.

import { GlobalRegistrator } from '@happy-dom/global-registrator';
import { compile, compileModule } from 'svelte/compiler';
import { readFileSync } from 'node:fs';

if (!GlobalRegistrator.isRegistered) {
	GlobalRegistrator.register();
}

// Happy DOM has no layout engine. Components use offsetParent/offsetWidth to
// decide whether a focusable control is visible before trapping focus; a
// test-only layout shim lets that real code path run. No component internals
// are replaced.
if (!Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetParent')) {
	Object.defineProperty(HTMLElement.prototype, 'offsetParent', {
		configurable: true,
		get() {
			return document.body;
		}
	});
}

if (typeof globalThis.ResizeObserver === 'undefined') {
	globalThis.ResizeObserver = class {
		observe() {}
		unobserve() {}
		disconnect() {}
	};
}

if (typeof globalThis.matchMedia !== 'function') {
	globalThis.matchMedia = (query) => ({
		// Exercise the real reduced-motion path: Svelte JS transitions snap to
		// zero duration instead of depending on an animation frame scheduler.
		matches: query.includes('prefers-reduced-motion: reduce'),
		media: query,
		onchange: null,
		addListener() {},
		removeListener() {},
		addEventListener() {},
		removeEventListener() {},
		dispatchEvent() {
			return false;
		}
	});
}

// Svelte outro transitions keep a node mounted until the Web Animations
// `onfinish` callback. Happy DOM's animations do not fire it deterministically
// without a frame loop, which left portaled dialogs on document.body between
// tests. A resolved no-op animation makes the real transition callbacks run;
// no component code is replaced.
const nativeAnimate = Element.prototype.animate;
Element.prototype.animate = function animateForKeyframes(keyframes, options) {
	if (process.env.CARBON_DOM_REAL_ANIMATIONS === '1') {
		return nativeAnimate.call(this, keyframes, options);
	}
	const animation = {
		playState: 'finished',
		currentTime: options?.duration ?? 0,
		effect: null,
		onfinish: null,
		oncancel: null,
		cancel() {
			this.playState = 'idle';
		},
		finish() {},
		addEventListener() {},
		removeEventListener() {}
	};
	queueMicrotask(() => animation.onfinish?.());
	return animation;
};

Bun.plugin({
	name: 'svelte-client-test',
	setup(build) {
		build.onLoad({ filter: /\.svelte$/ }, async (args) => {
			const source = readFileSync(args.path, 'utf8');
			const { js } = compile(source, {
				filename: args.path,
				generate: 'client',
				dev: true,
				css: 'injected'
			});
			return { contents: js.code, loader: 'js' };
		});
		// Runes modules (.svelte.js) carry reactive state for stores the DOM tests
		// mount; they need Svelte's module transform, not the component transform.
		build.onLoad({ filter: /\.svelte\.js$/ }, async (args) => {
			const source = readFileSync(args.path, 'utf8');
			const { js } = compileModule(source, {
				filename: args.path,
				generate: 'client',
				dev: true
			});
			return { contents: js.code, loader: 'js' };
		});
	}
});
