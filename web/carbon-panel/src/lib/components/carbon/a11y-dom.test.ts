// SPDX-License-Identifier: MPL-2.0
// Real DOM tests for public UI seams. These mount the actual Svelte components
// in a standards DOM (see test/dom-setup.mjs) and drive them through keyboard
// and click events; they do not inspect component source or mock internals.

import test from 'node:test';
import assert from 'node:assert/strict';
import '../../../../test/dom-setup.mjs';

// `dom-setup` registers the DOM and the .svelte compiler plugin before these
// dynamic imports resolve. Svelte itself must also use its client build.
const { mount, unmount } = await import('svelte');
const { default: CarbonModal } = await import('./CarbonModal.svelte');
const { default: CarbonTabs } = await import('./CarbonTabs.svelte');
const { default: CarbonButton } = await import('./CarbonButton.svelte');
const { default: CarbonErrorState } = await import('./CarbonErrorState.svelte');
const { default: CarbonTextInput } = await import('./CarbonTextInput.svelte');
const { default: CarbonBreadcrumbs } = await import('./CarbonBreadcrumbs.svelte');
const { default: UpdateBatch } = await import('../fragments/UpdateBatch.svelte');

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

function mountUi(Component: unknown, props: Record<string, unknown> = {}) {
	const target = document.createElement('div');
	document.body.appendChild(target);
	// Components are compiler-generated constructors; the dynamic import types
	// deliberately stay loose so the test drives only the public prop seam.
	const instance = mount(Component as never, { target, props });
	return {
		target,
		instance,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

function key(keyName: string, init: KeyboardEventInit = {}) {
	return new KeyboardEvent('keydown', { key: keyName, bubbles: true, cancelable: true, ...init });
}

test('CarbonModal exposes dialog semantics and moves focus into the dialog on open', async () => {
	const trigger = document.createElement('button');
	trigger.textContent = 'Delete backup';
	document.body.appendChild(trigger);
	trigger.focus();
	assert.equal(document.activeElement, trigger);

	const ui = mountUi(CarbonModal, {
		open: true,
		title: 'Delete backup?',
		description: 'This cannot be undone.',
		primaryButtonText: 'Delete',
		secondaryButtonText: 'Keep it'
	});
	try {
		await flush();
		const dialog = ui.target.ownerDocument.querySelector('[role="dialog"]');
		assert.ok(dialog, 'dialog is rendered through the public open prop');
		assert.equal(dialog?.getAttribute('aria-modal'), 'true');
		const labelledBy = dialog?.getAttribute('aria-labelledby');
		const describedBy = dialog?.getAttribute('aria-describedby');
		assert.ok(labelledBy, 'an accessible name is wired through aria-labelledby');
		assert.ok(describedBy, 'a description is wired through aria-describedby');
		assert.equal(document.getElementById(labelledBy ?? '')?.textContent?.trim(), 'Delete backup?');
		assert.equal(
			document.getElementById(describedBy ?? '')?.textContent?.trim(),
			'This cannot be undone.'
		);
		assert.equal(document.activeElement?.getAttribute('aria-label'), 'Close modal');
	} finally {
		ui.unmount();
		trigger.remove();
	}
});

test('CarbonModal traps Tab in both directions and Escape closes and restores focus', async () => {
	const trigger = document.createElement('button');
	document.body.appendChild(trigger);
	trigger.focus();

	let closed = 0;
	const ui = mountUi(CarbonModal, {
		open: true,
		title: 'Confirm action',
		primaryButtonText: 'Confirm',
		secondaryButtonText: 'Cancel',
		onclose: () => {
			closed += 1;
		}
	});
	try {
		await flush();
		const dialog = document.querySelector('[role="dialog"]');
		assert.ok(dialog);
		const focusable = Array.from(
			dialog?.querySelectorAll<HTMLElement>('button:not([disabled])') ?? []
		);
		assert.equal(focusable.length, 3, 'close, cancel and confirm are in the tab cycle');
		const [first, , last] = focusable;

		// Forward wrap: Tab on the last control returns to the first.
		last.focus();
		window.dispatchEvent(key('Tab'));
		assert.equal(document.activeElement, first, 'Tab wraps from last to first');

		// Backward wrap: Shift+Tab on the first control returns to the last.
		first.focus();
		window.dispatchEvent(key('Tab', { shiftKey: true }));
		assert.equal(document.activeElement, last, 'Shift+Tab wraps from first to last');

		// Focus outside the dialog is pulled back in on Tab.
		trigger.focus();
		window.dispatchEvent(key('Tab'));
		assert.ok(dialog?.contains(document.activeElement), 'Tab cannot escape the dialog');

		const escape = key('Escape');
		window.dispatchEvent(escape);
		assert.equal(escape.defaultPrevented, true, 'Escape is consumed by the dialog');
		assert.equal(closed, 1, 'onclose fires exactly once');
		await flush(0);
		assert.equal(document.querySelector('[role="dialog"]'), null, 'dialog is removed');
		assert.equal(document.activeElement, trigger, 'focus returns to the invoking control');
	} finally {
		ui.unmount();
		trigger.remove();
	}
});

test('CarbonTabs implements the APG keyboard model with roving tabindex', async () => {
	const selected: string[] = [];
	const ui = mountUi(CarbonTabs, {
		tabs: [
			{ id: 'overview', label: 'Overview' },
			{ id: 'files', label: 'Files', badge: 4 },
			{ id: 'logs', label: 'Logs' }
		],
		selectedTab: 'overview',
		onselect: (id: string) => selected.push(id)
	});
	try {
		await flush(0);
		const tablist = document.querySelector('[role="tablist"]');
		assert.ok(tablist);
		assert.equal(tablist?.getAttribute('aria-label'), 'Sections');
		const tabs = Array.from(document.querySelectorAll<HTMLButtonElement>('[role="tab"]'));
		assert.deepEqual(
			tabs.map((tab) => tab.textContent?.trim()),
			['Overview', 'Files 4', 'Logs']
		);
		assert.deepEqual(
			tabs.map((tab) => tab.getAttribute('tabindex')),
			['0', '-1', '-1']
		);
		assert.equal(tabs[0].getAttribute('aria-selected'), 'true');

		tabs[0].focus();
		tabs[0].dispatchEvent(key('ArrowRight'));
		await flush(0);
		assert.equal(selected.at(-1), 'files');
		assert.equal(document.activeElement, tabs[1], 'ArrowRight moves focus and selection');
		assert.equal(tabs[1].getAttribute('aria-selected'), 'true');
		assert.equal(tabs[1].getAttribute('tabindex'), '0');

		tabs[1].dispatchEvent(key('End'));
		await flush(0);
		assert.equal(document.activeElement, tabs[2], 'End selects the last tab');
		tabs[2].dispatchEvent(key('ArrowRight'));
		await flush(0);
		assert.equal(document.activeElement, tabs[0], 'ArrowRight wraps to the first tab');
		tabs[0].dispatchEvent(key('ArrowLeft'));
		await flush(0);
		assert.equal(document.activeElement, tabs[2], 'ArrowLeft wraps to the last tab');
		tabs[2].dispatchEvent(key('Home'));
		await flush(0);
		assert.equal(document.activeElement, tabs[0], 'Home selects the first tab');
	} finally {
		ui.unmount();
	}
});

test('CarbonTabs renders an empty tablist without throwing', async () => {
	const ui = mountUi(CarbonTabs, { tabs: [] });
	try {
		await flush(0);
		assert.ok(document.querySelector('[role="tablist"]'));
		assert.equal(document.querySelectorAll('[role="tab"]').length, 0);
	} finally {
		ui.unmount();
	}
});

test('CarbonButton exposes an accessible name for icon-only actions and a safe disabled link', async () => {
	const icon = mountUi(CarbonButton, { iconOnly: true, 'aria-label': 'Stop server' });
	try {
		const button = document.querySelector('button');
		assert.ok(button);
		assert.equal(button?.getAttribute('aria-label'), 'Stop server');
		assert.equal(button?.getAttribute('type'), 'button');
	} finally {
		icon.unmount();
	}

	const link = mountUi(CarbonButton, { href: '/servers', disabled: true, 'aria-label': 'Servers' });
	try {
		const anchor = document.querySelector('a');
		assert.ok(anchor);
		assert.equal(anchor?.hasAttribute('href'), false, 'disabled link has no destination');
		assert.equal(anchor?.getAttribute('aria-disabled'), 'true');
		assert.equal(anchor?.getAttribute('tabindex'), '-1');
		assert.equal(anchor?.getAttribute('aria-label'), 'Servers');
	} finally {
		link.unmount();
	}
});

test('CarbonErrorState announces errors and exposes a keyboard-operable retry', async () => {
	let retries = 0;
	const ui = mountUi(CarbonErrorState, {
		title: 'Failed to load backups',
		message: 'The server returned an error.',
		retryLabel: 'Try again',
		onretry: () => {
			retries += 1;
		}
	});
	try {
		const alert = document.querySelector('[role="alert"]');
		assert.ok(alert, 'error is announced with role=alert');
		assert.match(alert?.textContent ?? '', /Failed to load backups/);
		assert.match(alert?.textContent ?? '', /The server returned an error\./);
		const retry = Array.from(document.querySelectorAll('button')).find(
			(button) => button.textContent?.trim() === 'Try again'
		);
		assert.ok(retry, 'retry button has a visible text label');
		retry?.focus();
		retry?.click();
		await flush(0);
		assert.equal(retries, 1);
	} finally {
		ui.unmount();
	}
});

test('CarbonTextInput associates its label and announces validation errors', async () => {
	const ui = mountUi(CarbonTextInput, { label: 'Server name', error: 'Name is required' });
	try {
		const label = document.querySelector('label');
		const input = document.querySelector('input');
		assert.ok(label && input);
		assert.equal(label?.textContent?.trim(), 'Server name');
		assert.equal(input?.getAttribute('id'), label?.getAttribute('for'));
		assert.equal(input?.getAttribute('aria-invalid'), 'true');
		const describedBy = input?.getAttribute('aria-describedby');
		assert.ok(describedBy);
		assert.equal(document.getElementById(describedBy ?? '')?.getAttribute('role'), 'alert');
		assert.match(document.getElementById(describedBy ?? '')?.textContent ?? '', /Name is required/);
	} finally {
		ui.unmount();
	}
});

test('CarbonBreadcrumbs gives the navigation an accessible name and a current page', async () => {
	const ui = mountUi(CarbonBreadcrumbs, {
		items: [{ href: '/servers', label: 'Servers' }, { label: 'Current server' }]
	});
	try {
		const nav = document.querySelector('nav');
		assert.ok(nav);
		assert.equal(nav?.getAttribute('aria-label'), 'Breadcrumbs');
		assert.equal(nav?.querySelector('a')?.textContent?.trim(), 'Servers');
		assert.equal(nav?.querySelector('span:last-child')?.textContent?.trim(), 'Current server');
	} finally {
		ui.unmount();
	}
});

test('UpdateBatch renders empty and populated states with named discard actions', async () => {
	const empty = mountUi(UpdateBatch, { batchOpen: true, batchItems: [], discardStaged: () => {} });
	try {
		const section = document.querySelector('section');
		assert.equal(section?.getAttribute('aria-label'), 'Staged update batch');
		assert.match(section?.textContent ?? '', /No staged changes\./);
		assert.equal(document.querySelectorAll('li').length, 0);
	} finally {
		empty.unmount();
	}

	const discarded: string[] = [];
	const populated = mountUi(UpdateBatch, {
		batchOpen: true,
		phase: 'draft',
		batchItems: [
			{ id: 'mod-1', kind: 'mod', summary: 'Update JourneyMap', action: 'upgrade' },
			{ id: 'file-1', kind: 'file', summary: 'Edit server.properties', before: 'old', after: 'new' }
		],
		discardStaged: (id: string) => discarded.push(id)
	});
	try {
		const section = document.querySelector('section');
		assert.equal(section?.getAttribute('aria-label'), 'Review changes');
		const items = Array.from(document.querySelectorAll('li'));
		assert.equal(items.length, 2, 'each staged change is a list item');
		const remove = Array.from(document.querySelectorAll('button')).find(
			(button) => button.getAttribute('aria-label') === 'Remove Update JourneyMap'
		);
		assert.ok(remove, 'discard action has a per-item accessible name');
		remove?.click();
		await flush(0);
		assert.deepEqual(discarded, ['mod-1']);
		assert.match(section?.textContent ?? '', /2\s+changes across 2 areas/);
	} finally {
		populated.unmount();
	}
});
