// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for keeping a selected tab inside a narrow scroll strip.

import test from 'node:test';
import assert from 'node:assert/strict';
import '../../../../test/dom-setup.mjs';

const TAB_WIDTH = 100;
const VIEWPORT_WIDTH = 300;
const tabs = Array.from({ length: 12 }, (_, i) => ({ id: `tab-${i}`, label: `Section ${i}` }));

function rect(left: number, width: number): DOMRect {
	return {
		x: left,
		y: 0,
		width,
		height: 40,
		top: 0,
		right: left + width,
		bottom: 40,
		left,
		toJSON() {
			return { left, top: 0, right: left + width, bottom: 40, width, height: 40 };
		}
	} as DOMRect;
}

const originalRect = HTMLElement.prototype.getBoundingClientRect;
const originalClientWidth = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'clientWidth');

// Happy DOM has no layout engine. Model a 300px scroll strip with 100px tabs so
// the component's real measurement code runs against stable geometry.
function installLayoutShim() {
	HTMLElement.prototype.getBoundingClientRect = function getBoundingClientRect() {
		if (this.matches('[data-carbon-tabs]')) return rect(0, VIEWPORT_WIDTH);
		if (this.matches('[data-tab]')) {
			const container = this.closest<HTMLElement>('[data-carbon-tabs]');
			if (container) {
				const buttons = Array.from(container.querySelectorAll<HTMLElement>('[data-tab]'));
				return rect(buttons.indexOf(this) * TAB_WIDTH - container.scrollLeft, TAB_WIDTH);
			}
		}
		return originalRect.call(this);
	};
	Object.defineProperty(HTMLElement.prototype, 'clientWidth', {
		configurable: true,
		get() {
			if (this instanceof HTMLElement && this.matches('[data-carbon-tabs]')) return VIEWPORT_WIDTH;
			return originalClientWidth?.get?.call(this) ?? 0;
		}
	});
}

function restoreLayoutShim() {
	HTMLElement.prototype.getBoundingClientRect = originalRect;
	if (originalClientWidth) {
		Object.defineProperty(HTMLElement.prototype, 'clientWidth', originalClientWidth);
	} else {
		Reflect.deleteProperty(HTMLElement.prototype, 'clientWidth');
	}
}

const { mount, unmount } = await import('svelte');
const { default: CarbonTabs } = await import('./CarbonTabs.svelte');

const flush = async (ms = 150) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

async function mountTabs(selectedTab: string) {
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(CarbonTabs as never, { target, props: { tabs, selectedTab } });
	await flush();
	const container = target.querySelector<HTMLElement>('[data-carbon-tabs]');
	assert.ok(container, 'the tab strip renders');
	return {
		target,
		container,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

function assertVisible(container: HTMLElement, tabId: string) {
	const tab = container.querySelector<HTMLElement>(`[data-testid="tab-${tabId}"]`);
	assert.ok(tab, `the ${tabId} tab renders`);
	const containerRect = container.getBoundingClientRect();
	const areaLeft = containerRect.left + container.clientLeft;
	const areaRight = areaLeft + container.clientWidth;
	const tabRect = tab.getBoundingClientRect();
	assert.ok(
		tabRect.left >= areaLeft && tabRect.right <= areaRight,
		`${tabId} must sit inside the strip's client area (${tabRect.left}..${tabRect.right} in ${areaLeft}..${areaRight})`
	);
}

test('the initially selected tab is visible on mount', async () => {
	installLayoutShim();
	const ui = await mountTabs('tab-0');
	try {
		assertVisible(ui.container, 'tab-0');
		assert.equal(ui.container.scrollLeft, 0, 'an already visible tab must not scroll the strip');
	} finally {
		ui.unmount();
		restoreLayoutShim();
	}
});

test('selecting a late tab scrolls its whole width into the client area', async () => {
	installLayoutShim();
	const ui = await mountTabs('tab-0');
	try {
		ui.container.querySelector<HTMLButtonElement>('[data-testid="tab-tab-11"]')!.click();
		await flush();
		assert.ok(ui.container.scrollLeft > 0, 'the strip must scroll to reach the late tab');
		assertVisible(ui.container, 'tab-11');
	} finally {
		ui.unmount();
		restoreLayoutShim();
	}
});
