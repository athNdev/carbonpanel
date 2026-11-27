// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for the server console: RPC failures already toasted by
// the interceptor must not add a component toast, while the role=alert
// announcement still updates exactly once. Failure paths must also terminate
// every loading spinner, and unmounting must clear fallback polling.
import test, { afterEach } from 'node:test';
import assert from 'node:assert/strict';
// @ts-expect-error -- Bun supplies this test-only module; the app tsconfig omits Bun types.
import { jest, mock } from 'bun:test';
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

// The live socket has its own harness; the console seams do not need it. The
// fixture keeps reactive connection state so connect() can start fallback
// polling, matching an unauthenticated app before the socket handshakes.
const noop = () => {};
const unsubscribe = () => noop;
const { wsState } = await import('../../../test/fixtures/websocket-state.svelte.js');
mock.module('$lib/stores/websocket.svelte', () => ({
	wsClient: {
		state: wsState,
		isReady: false,
		connect: () => {
			wsState.connectionState = 'connecting';
		},
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
	jest.useRealTimers();
	wsState.connectionState = 'disconnected';
	(rpcClient as unknown as { server: typeof originalServerClient }).server = originalServerClient;
});
const { ServerSchema, ServerStatus } = await import('$lib/proto/carbonpanel/v1/common_pb');
const { LogEntrySchema } = await import('$lib/proto/carbonpanel/v1/server_pb');
const { default: ServerConsole } = await import('./server-console.svelte');

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

// Let pending promises and Svelte's effect queue drain without a wall-clock
// wait, so fake-timer tests do not need real intervals to progress.
const settle = async () => {
	for (let i = 0; i < 10; i += 1) await Promise.resolve();
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

function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (error: unknown) => void;
	const promise = new Promise<T>((res, rej) => {
		resolve = res;
		reject = rej;
	});
	return { promise, resolve, reject };
}

const server = create(ServerSchema, {
	id: 'srv-1',
	name: 'survival',
	containerId: 'container-1',
	status: ServerStatus.RUNNING
});

function mountUi(active = false) {
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(ServerConsole as never, { target, props: { server, active } });
	let destroyed = false;
	return {
		target,
		unmount() {
			if (destroyed) return;
			destroyed = true;
			unmount(instance);
			target.remove();
		}
	};
}

function alertText(target: HTMLElement): string {
	return target.querySelector('[role="alert"]')?.textContent?.trim() ?? '';
}

function spinnerCount(target: HTMLElement): number {
	return target.querySelectorAll('.animate-spin').length;
}

function watchAlert(target: HTMLElement) {
	const alert = target.querySelector('[role="alert"]');
	assert.ok(alert, 'the console exposes an assertive alert region');
	const updates: string[] = [];
	const observer = new MutationObserver(() => updates.push(alert.textContent?.trim() ?? ''));
	observer.observe(alert, { childList: true, characterData: true, subtree: true });
	return {
		updates,
		stop: () => observer.disconnect()
	};
}

function refreshButton(target: HTMLElement): HTMLButtonElement {
	return target.querySelector('[aria-label="Refresh logs"]') as HTMLButtonElement;
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

test('a tagged log-listing failure terminates loading, re-enables Refresh and announces once', async () => {
	errorToasts.length = 0;
	const pending = deferred<{ logs: unknown[] }>();
	let calls = 0;
	stubServerClient({
		getServerLogs: () => {
			calls += 1;
			return pending.promise;
		}
	});
	const ui = mountUi();
	const watcher = watchAlert(ui.target);
	try {
		const refresh = refreshButton(ui.target);
		refresh.click();
		await flush(10);
		assert.equal(calls, 1, 'Refresh must issue the RPC');
		assert.equal(refresh.disabled, true, 'Refresh stays disabled while the listing is in flight');
		assert.equal(spinnerCount(ui.target), 1, 'the in-flight listing shows one spinner');

		pending.reject(taggedError('Carbon Panel could not reach the server.'));
		await flush();

		assert.equal(refresh.disabled, false, 'Refresh must re-enable after the failure');
		assert.equal(spinnerCount(ui.target), 0, 'no loading spinner may survive the failure');
		assert.deepEqual(errorToasts, [], 'the component must not toast a tagged failure');
		assert.equal(
			alertText(ui.target),
			'Failed to fetch logs: Carbon Panel could not reach the server.'
		);
		assert.deepEqual(
			watcher.updates,
			['Failed to fetch logs: Carbon Panel could not reach the server.'],
			'the role=alert region must update exactly once'
		);
	} finally {
		watcher.stop();
		ui.unmount();
	}
});

test('a plain log-listing failure also terminates loading and announces once', async () => {
	errorToasts.length = 0;
	stubServerClient({
		getServerLogs: async () => {
			throw new Error('plain failure');
		}
	});
	const ui = mountUi();
	const watcher = watchAlert(ui.target);
	try {
		refreshButton(ui.target).click();
		await flush();
		assert.equal(refreshButton(ui.target).disabled, false);
		assert.equal(spinnerCount(ui.target), 0);
		assert.deepEqual(errorToasts, ['Failed to fetch logs: plain failure']);
		assert.equal(alertText(ui.target), 'Failed to fetch logs: plain failure');
		assert.deepEqual(watcher.updates, ['Failed to fetch logs: plain failure']);
	} finally {
		watcher.stop();
		ui.unmount();
	}
});

test('a rejected RPC command resets loading and re-enables the command controls', async () => {
	errorToasts.length = 0;
	const pending = deferred<{ success: boolean }>();
	stubServerClient({
		sendCommand: () => pending.promise
	});
	const ui = mountUi();
	const watcher = watchAlert(ui.target);
	try {
		const input = ui.target.querySelector(
			'input[placeholder^="Enter Minecraft"]'
		) as HTMLInputElement;
		assert.ok(input, 'the console exposes a command input');
		input.value = 'say hi';
		input.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true, data: 'say hi' }));
		await flush(10);
		const send = ui.target.querySelector('[data-testid="console-send"]') as HTMLButtonElement;
		send.click();
		await flush(10);

		assert.equal(send.disabled, true, 'Send stays disabled while the command is in flight');
		assert.equal(refreshButton(ui.target).disabled, true, 'shared loading state is visible');
		assert.equal(input.disabled, false, 'the command input remains available');

		pending.reject(taggedError('Command rejected.'));
		await flush();

		assert.equal(
			refreshButton(ui.target).disabled,
			false,
			'loading must reset after the rejection'
		);
		assert.equal(input.disabled, false, 'the command input stays enabled');
		const nextSend = ui.target.querySelector('[data-testid="console-send"]') as HTMLButtonElement;
		assert.equal(nextSend.disabled, true, 'Send stays disabled only because the command cleared');
		input.value = 'say again';
		input.dispatchEvent(
			new InputEvent('input', { bubbles: true, composed: true, data: 'say again' })
		);
		await flush(10);
		assert.equal(nextSend.disabled, false, 'Send re-enables once a new command is typed');
		assert.equal(spinnerCount(ui.target), 0, 'no loading spinner may survive the rejection');
		assert.deepEqual(errorToasts, []);
		assert.equal(alertText(ui.target), 'Failed to execute command: Command rejected.');
		assert.deepEqual(watcher.updates, ['Failed to execute command: Command rejected.']);
	} finally {
		watcher.stop();
		ui.unmount();
	}
});

test('a rejected log clear resets clearing and re-enables the Clear control', async () => {
	errorToasts.length = 0;
	const pending = deferred<Record<string, never>>();
	stubServerClient({
		getServerLogs: async () => ({ logs: [create(LogEntrySchema, { message: 'hello' })] }),
		clearServerLogs: () => pending.promise
	});
	const ui = mountUi();
	const watcher = watchAlert(ui.target);
	try {
		refreshButton(ui.target).click();
		await flush();
		const clearButton = () =>
			ui.target.querySelector('[aria-label="Clear log buffer"]') as HTMLButtonElement;
		assert.equal(clearButton().disabled, false, 'Clear enables once logs are loaded');

		clearButton().click();
		await flush(10);
		const confirm = Array.from(document.querySelectorAll('button')).find((button) =>
			button.textContent?.includes('Clear viewed buffer')
		) as HTMLButtonElement | undefined;
		assert.ok(confirm, 'the clear confirm must be reachable');
		confirm.click();
		await flush(10);

		const working = Array.from(document.querySelectorAll('button')).find((button) =>
			button.textContent?.includes('Working')
		) as HTMLButtonElement | undefined;
		assert.ok(working, 'the confirm control shows clearing state');
		assert.equal(working.disabled, true, 'the confirm control is disabled while clearing');
		assert.ok(spinnerCount(document.body) >= 1, 'the clearing state shows a spinner');

		pending.reject(taggedError('Clear rejected.'));
		await flush();

		assert.equal(clearButton().disabled, false, 'Clear re-enables after the failure');
		assert.equal(
			Array.from(document.querySelectorAll('button')).some((button) =>
				button.textContent?.includes('Working')
			),
			false,
			'the clearing state must terminate'
		);
		assert.deepEqual(errorToasts, []);
		assert.equal(alertText(ui.target), 'Failed to clear logs: Clear rejected.');
		assert.deepEqual(watcher.updates, ['Failed to clear logs: Clear rejected.']);
	} finally {
		watcher.stop();
		ui.unmount();
	}
});

test('a rejected mclo.gs upload resets uploading and re-enables the Upload control', async () => {
	errorToasts.length = 0;
	const pending = deferred<{ url: string }>();
	stubServerClient({
		uploadToMCLogs: () => pending.promise
	});
	const ui = mountUi();
	const watcher = watchAlert(ui.target);
	try {
		const uploadButton = () =>
			ui.target.querySelector('[aria-label="Upload logs to mclo.gs"]') as HTMLButtonElement;
		uploadButton().click();
		await flush(10);
		assert.equal(uploadButton().disabled, true, 'Upload stays disabled while in flight');
		assert.equal(spinnerCount(ui.target), 1, 'the upload state shows one spinner');

		pending.reject(taggedError('Upload rejected.'));
		await flush();

		assert.equal(uploadButton().disabled, false, 'Upload re-enables after the failure');
		assert.equal(spinnerCount(ui.target), 0, 'no loading spinner may survive the failure');
		assert.deepEqual(errorToasts, []);
		assert.equal(alertText(ui.target), 'Failed to upload to mclo.gs: Upload rejected.');
		assert.deepEqual(watcher.updates, ['Failed to upload to mclo.gs: Upload rejected.']);
	} finally {
		watcher.stop();
		ui.unmount();
	}
});

test('an empty log listing renders the empty state without a loading spinner', async () => {
	stubServerClient({
		getServerLogs: async () => ({ logs: [] })
	});
	const ui = mountUi();
	try {
		assert.equal(spinnerCount(ui.target), 0, 'the initial empty state has no spinner');
		refreshButton(ui.target).click();
		await flush();
		assert.match(ui.target.querySelector('[role="log"]')?.textContent ?? '', /No logs available/);
		assert.equal(refreshButton(ui.target).disabled, false);
		assert.equal(spinnerCount(ui.target), 0, 'the settled empty state has no spinner');
	} finally {
		ui.unmount();
	}
});

test('unmounting clears the fallback polling interval and stops log fetches', async () => {
	jest.useFakeTimers();
	let calls = 0;
	stubServerClient({
		getServerLogs: async () => {
			calls += 1;
			return { logs: [] };
		}
	});
	const ui = mountUi(true);
	try {
		await settle();
		assert.equal(calls, 1, 'mount fetches the initial log listing');
		assert.equal(wsState.connectionState, 'connecting');
		assert.equal(jest.getTimerCount(), 1, 'the fallback interval is scheduled');

		jest.advanceTimersByTime(4999);
		await settle();
		assert.equal(calls, 1, 'fallback polling waits the full 5s interval');
		jest.advanceTimersByTime(1);
		await settle();
		assert.equal(calls, 2, 'the fallback interval is live before unmount');

		ui.unmount();
		assert.equal(jest.getTimerCount(), 0, 'unmount clears the fallback interval');

		jest.advanceTimersByTime(15000);
		await settle();
		assert.equal(calls, 2, 'no log fetch may run after unmount');
	} finally {
		ui.unmount();
	}
});
