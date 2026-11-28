// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for listener renames: an empty name must be rejected before
// the RPC, and a valid name must be saved trimmed.

import test, { after } from 'node:test';
import assert from 'node:assert/strict';
// @ts-expect-error -- Bun supplies this test-only module; the app tsconfig omits Bun types.
import { mock } from 'bun:test';
import '../../../test/dom-setup.mjs';

mock.module('$app/navigation', () => ({
	goto: () => Promise.resolve(),
	invalidateAll: () => Promise.resolve(),
	onNavigate: () => {},
	beforeNavigate: () => {},
	afterNavigate: () => {},
	pushState: () => {},
	replaceState: () => {},
	preloadData: () => Promise.resolve()
}));
mock.module('$app/paths', () => ({
	resolve: (path: string) => path,
	asset: (path: string) => path
}));
mock.module('$app/env', () => ({ browser: true }));
mock.module('$app/state', () => ({
	page: { url: new URL('http://localhost/'), params: {}, data: {}, state: {} }
}));

mock.module('svelte-sonner', () => ({
	toast: { error: () => {}, success: () => {}, info: () => {} }
}));

const { create } = await import('@bufbuild/protobuf');
const { ProxyListenerSchema } = await import('$lib/proto/carbonpanel/v1/common_pb');
const listener = create(ProxyListenerSchema, {
	id: 'listener-1',
	name: 'Primary',
	description: '',
	port: 25565,
	enabled: true,
	isDefault: false
});

const updateRequests: Array<{ name?: string }> = [];

// Keep the real RPC singleton and replace only the methods this test drives,
// so no process-global module mock leaks into later test files.
const { rpcClient } = await import('$lib/api/rpc-client');
const proxyClient = rpcClient.proxy as unknown as Record<string, unknown>;
const realProxyMethods = {
	getProxyStatus: rpcClient.proxy.getProxyStatus.bind(rpcClient.proxy),
	getProxyListeners: rpcClient.proxy.getProxyListeners.bind(rpcClient.proxy),
	getProxyRoutes: rpcClient.proxy.getProxyRoutes.bind(rpcClient.proxy),
	updateProxyListener: rpcClient.proxy.updateProxyListener.bind(rpcClient.proxy)
};
Object.assign(proxyClient, {
	getProxyStatus: async () => ({ enabled: true, baseUrl: '' }),
	getProxyListeners: async () => ({ listeners: [{ listener, serverCount: 0 }] }),
	getProxyRoutes: async () => ({ routes: [] }),
	updateProxyListener: async (request: { name?: string }) => {
		updateRequests.push({ name: request.name });
		return {};
	}
});
after(() => {
	Object.assign(proxyClient, realProxyMethods);
});

const { mount, unmount } = await import('svelte');
const { default: RoutingSettings } = await import('./routing-settings.svelte');

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

function mountUi() {
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(RoutingSettings as never, { target });
	return {
		target,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

function editNameInput(target: HTMLElement): HTMLInputElement {
	const input = target.querySelector<HTMLInputElement>('input[placeholder="Listener name"]');
	assert.ok(input, 'the listener edit form exposes its name field');
	return input;
}

function saveButton(target: HTMLElement): HTMLButtonElement {
	const button = Array.from(target.querySelectorAll('button')).find(
		(candidate) => candidate.textContent?.trim() === 'Save'
	);
	assert.ok(button, 'the listener edit form exposes a Save button');
	return button;
}

function type(input: HTMLInputElement, value: string) {
	input.value = value;
	input.dispatchEvent(new Event('input', { bubbles: true }));
}

test('an empty listener name is rejected before the RPC and saved names are trimmed', async () => {
	updateRequests.length = 0;
	const ui = mountUi();
	try {
		await flush();
		ui.target.querySelector<HTMLButtonElement>('[aria-label="Edit listener"]')!.click();
		await flush();
		const input = editNameInput(ui.target);

		type(input, '   ');
		saveButton(ui.target).click();
		await flush();

		assert.equal(updateRequests.length, 0, 'an empty name must not call updateProxyListener');
		assert.equal(
			ui.target.querySelector('[role="alert"]')?.textContent?.trim(),
			'Listener name is required',
			'the edit form shows an inline name error'
		);
		assert.ok(editNameInput(ui.target), 'the edit form stays open after the rejection');

		type(input, '  Renamed  ');
		saveButton(ui.target).click();
		await flush();

		assert.equal(updateRequests.length, 1, 'a valid rename sends exactly one update');
		assert.equal(updateRequests[0].name, 'Renamed', 'the saved name is trimmed before the RPC');
	} finally {
		ui.unmount();
	}
});
