// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for node dialog focus return. Closing a dialog must put
// keyboard focus back on the control that opened it.

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
const { NodeSchema, NodeStatus } = await import('$lib/proto/carbonpanel/v1/node_pb');
const node = create(NodeSchema, {
	id: 'node-1',
	name: 'node-1',
	host: 'unix:///var/run/docker.sock',
	status: NodeStatus.ONLINE,
	enabled: true
});

let resolveScan: ((value: unknown) => void) | undefined;
const scanResult = new Promise<unknown>((resolve) => {
	resolveScan = resolve;
});
const scanCandidates = [
	{ host: 'tcp://10.0.0.5:2376', port: 2376, reachable: true, latencyMs: 5n, source: 'scan' }
];

// Keep the real RPC singleton and replace only the methods this test drives,
// so no process-global module mock leaks into later test files.
const { rpcClient } = await import('$lib/api/rpc-client');
const nodeClient = rpcClient.node as unknown as Record<string, unknown>;
const realNodeMethods = {
	listNodes: rpcClient.node.listNodes.bind(rpcClient.node),
	scanNodes: rpcClient.node.scanNodes.bind(rpcClient.node),
	pingNode: rpcClient.node.pingNode.bind(rpcClient.node),
	createNode: rpcClient.node.createNode.bind(rpcClient.node),
	updateNode: rpcClient.node.updateNode.bind(rpcClient.node)
};
Object.assign(nodeClient, {
	listNodes: async () => ({ nodes: [node] }),
	scanNodes: () => scanResult,
	pingNode: async () => ({}),
	createNode: async () => ({}),
	updateNode: async () => ({})
});
after(() => {
	Object.assign(nodeClient, realNodeMethods);
});

const { mount, unmount } = await import('svelte');
const { default: NodeSettings } = await import('./node-settings.svelte');

const flush = async (ms = 120) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

function mountUi() {
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(NodeSettings as never, { target });
	return {
		target,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

function buttonByText(target: HTMLElement, text: string): HTMLButtonElement {
	const button = Array.from(target.querySelectorAll('button')).find((candidate) =>
		candidate.textContent?.includes(text)
	);
	assert.ok(button, `expected a "${text}" button`);
	return button;
}

test('closing the auto-detect results dialog returns focus to the trigger', async () => {
	const ui = mountUi();
	try {
		await flush();
		const trigger = buttonByText(ui.target, 'Auto-detect');
		trigger.focus();
		assert.equal(document.activeElement, trigger, 'the trigger starts focused');

		trigger.click();
		await flush(20);
		assert.equal(trigger.disabled, true, 'scanning disables the trigger');
		// Browsers move focus off a control when it becomes disabled; Happy DOM
		// keeps focus on disabled controls, so model that focus move explicitly
		// before the async scan resolves.
		document.body.focus();
		assert.equal(document.activeElement, document.body);

		resolveScan?.({ candidates: scanCandidates });
		await flush(250);
		assert.ok(
			Array.from(document.querySelectorAll('[role="dialog"]')).some((dialog) =>
				dialog.textContent?.includes('Docker daemons detected')
			),
			'the detected-daemons dialog opens after the scan'
		);

		buttonByText(document.body, 'Skip for now').click();
		await flush();
		assert.equal(
			document.activeElement,
			trigger,
			'focus must return to the Auto-detect trigger after close'
		);
	} finally {
		ui.unmount();
	}
});

test('closing the edit dialog returns focus to the edit trigger', async () => {
	const ui = mountUi();
	try {
		await flush();
		const trigger = ui.target.querySelector<HTMLButtonElement>('[aria-label="Edit node-1"]');
		assert.ok(trigger, 'the node exposes an edit control');
		trigger.focus();
		trigger.click();
		await flush();
		assert.ok(
			Array.from(document.querySelectorAll('[role="dialog"]')).some((dialog) =>
				dialog.textContent?.includes('Edit Docker Daemon Node')
			),
			'the edit dialog opens'
		);

		buttonByText(document.body, 'Cancel').click();
		await flush();
		assert.equal(
			document.activeElement,
			trigger,
			'focus must return to the Edit trigger after close'
		);
	} finally {
		ui.unmount();
	}
});
