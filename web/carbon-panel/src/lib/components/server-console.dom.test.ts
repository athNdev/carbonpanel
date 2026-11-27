// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for the server console: RPC failures already toasted by
// the interceptor must not add a component toast, while the role=alert
// announcement still updates exactly once.
import test, { afterEach } from 'node:test';
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
	page: { url: new URL('http://localhost/'), params: {}, data: {} }
}));

const errorToasts: string[] = [];
mock.module('svelte-sonner', () => ({
	toast: {
		error: (message: unknown) => errorToasts.push(String(message)),
		success: () => {}
	}
}));

const { mount, unmount } = await import('svelte');
const { create } = await import('@bufbuild/protobuf');

// paneforge only exports the `svelte` condition, which the Bun test runner
// does not request; the pane wrappers are irrelevant to the toast seam.
const { default: ResizableStub } = await import('../../../test/fixtures/resizable-stub.svelte');
mock.module('$lib/components/ui/resizable', () => ({
	ResizablePaneGroup: ResizableStub,
	ResizablePane: ResizableStub,
	ResizableHandle: ResizableStub
}));

// The live socket has its own harness; the console toast seams do not need it.
const noop = () => {};
const unsubscribe = () => noop;
mock.module('$lib/stores/websocket.svelte', () => ({
	wsClient: {
		state: { connectionState: 'disconnected' },
		isReady: false,
		connect: noop,
		reconnect: noop,
		subscribe: noop,
		unsubscribe: noop,
		sendCommand: noop,
		onLogs: unsubscribe,
		onLogEntry: unsubscribe,
		onCommandResult: unsubscribe
	}
}));

const { rpcClient } = await import('$lib/api/rpc-client');
const originalServerClient = rpcClient.server;
afterEach(() => {
	(rpcClient as unknown as { server: typeof originalServerClient }).server = originalServerClient;
});
const { ServerSchema, ServerStatus } = await import('$lib/proto/carbonpanel/v1/common_pb');
const { LogEntrySchema } = await import('$lib/proto/carbonpanel/v1/server_pb');
const { default: ServerConsole } = await import('./server-console.svelte');

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

type ServerClient = {
	getServerLogs: () => Promise<unknown>;
	clearServerLogs: () => Promise<unknown>;
	sendCommand: () => Promise<unknown>;
	uploadToMCLogs: () => Promise<unknown>;
};

function stubServerClient(overrides: Partial<ServerClient>): ServerClient {
	const stub: ServerClient = {
		getServerLogs: async () => ({ logs: [] }),
		clearServerLogs: async () => ({}),
		sendCommand: async () => ({ success: true }),
		uploadToMCLogs: async () => ({ url: '' }),
		...overrides
	};
	(rpcClient as unknown as { server: ServerClient }).server = stub;
	return stub;
}

function taggedError(message: string): Error {
	return Object.assign(new Error(message), { __rpcToasted: true });
}

const server = create(ServerSchema, {
	id: 'srv-1',
	name: 'survival',
	containerId: 'container-1',
	status: ServerStatus.RUNNING
});

function mountUi() {
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(ServerConsole as never, { target, props: { server, active: false } });
	return {
		target,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

function alertText(target: HTMLElement): string {
	return target.querySelector('[role="alert"]')?.textContent?.trim() ?? '';
}

test('a log-listing failure already toasted by the interceptor adds no component toast', async () => {
	errorToasts.length = 0;
	let calls = 0;
	stubServerClient({
		getServerLogs: async () => {
			calls += 1;
			throw taggedError('Carbon Panel could not reach the server.');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		(ui.target.querySelector('[aria-label="Refresh logs"]') as HTMLElement).click();
		await flush();
		assert.equal(calls, 1, 'Refresh must issue the RPC');
		assert.deepEqual(errorToasts, [], 'the component must not toast a tagged failure');
		assert.match(alertText(ui.target), /Failed to fetch logs: Carbon Panel could not reach/);
	} finally {
		ui.unmount();
	}
});

test('an unmarked log-listing failure produces exactly one component toast', async () => {
	errorToasts.length = 0;
	stubServerClient({
		getServerLogs: async () => {
			throw new Error('plain failure');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		(ui.target.querySelector('[aria-label="Refresh logs"]') as HTMLElement).click();
		await flush();
		assert.deepEqual(errorToasts, ['Failed to fetch logs: plain failure']);
		assert.match(alertText(ui.target), /Failed to fetch logs: plain failure/);
	} finally {
		ui.unmount();
	}
});

test('a tagged log-clear failure adds no component toast but announces once', async () => {
	errorToasts.length = 0;
	stubServerClient({
		getServerLogs: async () => ({ logs: [create(LogEntrySchema, { message: 'hello' })] }),
		clearServerLogs: async () => {
			throw taggedError('Clear rejected.');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		(ui.target.querySelector('[aria-label="Refresh logs"]') as HTMLElement).click();
		await flush();
		(ui.target.querySelector('[aria-label="Clear log buffer"]') as HTMLButtonElement).click();
		await flush();
		const confirm = Array.from(document.querySelectorAll('button')).find((button) =>
			button.textContent?.includes('Clear viewed buffer')
		) as HTMLButtonElement | undefined;
		assert.ok(confirm, 'the clear confirm must be reachable');
		confirm.click();
		await flush();
		assert.deepEqual(errorToasts, []);
		assert.match(alertText(ui.target), /Failed to clear logs: Clear rejected\./);
	} finally {
		ui.unmount();
	}
});

test('a tagged mclo.gs upload failure adds no component toast but announces once', async () => {
	errorToasts.length = 0;
	stubServerClient({
		uploadToMCLogs: async () => {
			throw taggedError('Upload rejected.');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		(ui.target.querySelector('[aria-label="Upload logs to mclo.gs"]') as HTMLElement).click();
		await flush();
		assert.deepEqual(errorToasts, []);
		assert.match(alertText(ui.target), /Failed to upload to mclo\.gs: Upload rejected\./);
	} finally {
		ui.unmount();
	}
});

test('a tagged command failure adds no component toast but announces once', async () => {
	errorToasts.length = 0;
	stubServerClient({
		sendCommand: async () => {
			throw taggedError('Command rejected.');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		const input = ui.target.querySelector(
			'input[placeholder^="Enter Minecraft"]'
		) as HTMLInputElement;
		assert.ok(input, 'the console exposes a command input');
		input.value = 'say hi';
		input.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true, data: 'say hi' }));
		await flush(10);
		const sendButton = ui.target.querySelector('[data-testid="console-send"]') as HTMLButtonElement;
		assert.equal(sendButton.disabled, false, 'Send must enable once a command is typed');
		sendButton.click();
		await flush();
		assert.deepEqual(errorToasts, []);
		assert.match(alertText(ui.target), /Failed to execute command: Command rejected\./);
	} finally {
		ui.unmount();
	}
});
