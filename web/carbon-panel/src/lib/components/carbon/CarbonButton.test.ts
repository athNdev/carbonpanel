// SPDX-License-Identifier: MPL-2.0
// Real-DOM tests for CarbonButton's public seams. These mount the actual
// component (see test/dom-setup.mjs, which compiles the .svelte graph with
// relative imports intact) rather than duplicating its disabled-state logic.
import test from 'node:test';
import assert from 'node:assert/strict';
import '../../../../test/dom-setup.mjs';

// `dom-setup` registers the DOM and the .svelte compiler plugin before this
// dynamic import resolves; CarbonButton now also imports CarbonSpinner, so a
// static or inlined import would not resolve the component graph.
const { mount, unmount } = await import('svelte');
const { default: Button } = await import('./CarbonButton.svelte');

function mountButton(props: Record<string, unknown>) {
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(Button as never, { target, props });
	return {
		anchor: target.querySelector('a'),
		button: target.querySelector('button'),
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

for (const state of ['disabled', 'loading'] as const) {
	test(`a ${state} link button cannot navigate or enter the tab order`, () => {
		const ui = mountButton({
			href: '/destination',
			[state]: true,
			tabindex: 0,
			'aria-disabled': 'false'
		});
		try {
			assert.ok(ui.anchor, 'Expected a rendered link button');
			assert.ok(!ui.anchor.hasAttribute('href'), 'a disabled link button must not navigate');
			assert.equal(ui.anchor.getAttribute('aria-disabled'), 'true');
			assert.equal(ui.anchor.getAttribute('role'), 'link');
			assert.equal(ui.anchor.getAttribute('tabindex'), '-1');
			assert.ok(ui.anchor.classList.contains('opacity-50'), 'disabled link button must dim');
		} finally {
			ui.unmount();
		}
	});
}

test('tertiary follows the Carbon spec: accent text, no border', () => {
	// Tertiary is the quiet action next to a primary button (Deploy/Export
	// beside Save). A full foreground border competes with the primary, so
	// per Carbon it renders accent text on transparent with a hover layer.
	const ui = mountButton({ kind: 'tertiary' });
	try {
		assert.ok(ui.button, 'Expected a rendered button');
		assert.ok(ui.button.classList.contains('text-accent-text'), 'tertiary text must be the accent');
		assert.ok(
			ui.button.classList.contains('border-transparent'),
			'tertiary must not draw a border'
		);
		assert.ok(
			!ui.button.className.includes('border-foreground'),
			'tertiary must not use a foreground border'
		);
	} finally {
		ui.unmount();
	}
});

test('an enabled link button retains its destination', () => {
	const ui = mountButton({ href: '/destination' });
	try {
		assert.equal(ui.anchor?.getAttribute('href'), '/destination');
	} finally {
		ui.unmount();
	}
});

test('a loading button renders the arc spinner, not a square border loader', () => {
	const ui = mountButton({ loading: true });
	try {
		assert.ok(ui.button, 'Expected a rendered button');
		const svg = ui.button.querySelector('svg');
		assert.ok(svg, 'loading state must render the CarbonSpinner arc');
		assert.ok(
			!ui.button.querySelector('.animate-spin.border-t-transparent'),
			'the three-sided border loader must be gone'
		);
	} finally {
		ui.unmount();
	}
});
