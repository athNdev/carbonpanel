// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for the export modal's attempt fence. A preparation that
// settles after the operator closed the dialog, reopened it for another format
// or switched servers must not write its session, filename, phase or error.
import test from 'node:test';
import assert from 'node:assert/strict';
// @ts-expect-error -- Bun supplies this test-only module; the app tsconfig omits Bun types.
import { mock } from 'bun:test';
import '../../../../test/dom-setup.mjs';

// The component imports the real RPC singleton, which reaches the auth store.
// Keep that real path while stubbing only SvelteKit's virtual app modules.
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
mock.module('svelte-sonner', () => ({
	toast: { error: () => {}, success: () => {} }
}));

const { mount, unmount } = await import('svelte');
const { rpcClient } = await import('$lib/api/rpc-client');
const { ServerStatus } = await import('../../proto/carbonpanel/v1/common_pb');
const { ServerArchiveFormat } = await import('../../proto/carbonpanel/v1/server_pb');
const { default: ServerArchiveExport } = await import('./ServerArchiveExport.svelte');
const { default: ExportHost } = await import('../../../../test/fixtures/export-host.svelte');

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

type Deferred = {
	promise: Promise<{ sessionId: string; filename: string; totalSize: bigint }>;
	resolve: (value: { sessionId: string; filename: string; totalSize: bigint }) => void;
	reject: (error: Error) => void;
	format: number;
};

function deferred(format: number): Deferred {
	let resolve!: Deferred['resolve'];
	let reject!: Deferred['reject'];
	const promise = new Promise<{ sessionId: string; filename: string; totalSize: bigint }>(
		(ok, fail) => {
			resolve = ok;
			reject = fail;
		}
	);
	return { promise, resolve, reject, format };
}

function mountExport() {
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(ServerArchiveExport as never, {
		target,
		props: {
			server: { id: 'server-a', name: 'Alpha', status: ServerStatus.STOPPED }
		}
	});
	return {
		target,
		instance,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

async function openExport() {
	const trigger = document.querySelector<HTMLElement>('[data-testid="server-export-archive"]');
	assert.ok(trigger, 'export trigger is mounted');
	trigger.click();
	await flush();
}

async function chooseFormat(label: string) {
	const radio = Array.from(document.querySelectorAll<HTMLElement>('[role="radio"]')).find(
		(element) => element.textContent?.trim() === label
	);
	assert.ok(radio, `${label} is offered`);
	radio.click();
	await flush();
}

async function prepare() {
	const button = document.querySelector<HTMLElement>('[data-testid="archive-prepare"]');
	assert.ok(button, 'prepare is available');
	button.click();
	await flush();
}

test('close/reopen fences a stale success and keeps the current tar.gz session', async () => {
	const calls: Deferred[] = [];
	(rpcClient.server as unknown as Record<string, unknown>).exportServerArchive = (request: {
		format: number;
	}) => {
		const entry = deferred(request.format);
		calls.push(entry);
		return entry.promise;
	};

	const ui = mountExport();
	try {
		await openExport();
		await prepare();
		assert.equal(calls.length, 1);
		assert.equal(calls[0].format, ServerArchiveFormat.ZIP);

		window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
		await flush();
		assert.equal(document.querySelector('[data-testid="archive-export-panel"]'), null);

		await openExport();
		await chooseFormat('TAR.GZ (.tar.gz)');
		await prepare();
		assert.equal(calls.length, 2);
		assert.equal(calls[1].format, ServerArchiveFormat.TAR_GZ);

		calls[0].resolve({
			sessionId: 'stale-zip-session',
			filename: 'stale.zip',
			totalSize: 1n
		});
		await flush();
		assert.equal(
			document.querySelector('[data-testid="archive-export-ready"]'),
			null,
			'a closed attempt must not make the reopened dialog ready'
		);
		assert.equal(
			document
				.querySelector('[data-testid="archive-export-panel"]')
				?.textContent?.includes('stale.zip'),
			false,
			'a closed attempt must not replace the active filename'
		);
		assert.ok(
			document
				.querySelector('[data-testid="archive-export-panel"]')
				?.textContent?.includes('Preparing archive'),
			'the current attempt still owns the preparing state'
		);

		calls[1].resolve({
			sessionId: 'current-tar-session',
			filename: 'current.tar.gz',
			totalSize: 2n
		});
		await flush();
		assert.ok(document.querySelector('[data-testid="archive-export-ready"]'));
		assert.ok(
			document
				.querySelector('[data-testid="archive-export-panel"]')
				?.textContent?.includes('current.tar.gz'),
			'the current tar.gz result is the only one rendered'
		);
	} finally {
		ui.unmount();
	}
});

test('close/reopen fences a stale rejection while the current attempt can still fail', async () => {
	const calls: Deferred[] = [];
	(rpcClient.server as unknown as Record<string, unknown>).exportServerArchive = (request: {
		format: number;
	}) => {
		const entry = deferred(request.format);
		calls.push(entry);
		return entry.promise;
	};

	const ui = mountExport();
	try {
		await openExport();
		await prepare();
		window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
		await flush();

		await openExport();
		await chooseFormat('TAR.GZ (.tar.gz)');
		await prepare();
		assert.equal(calls.length, 2);

		calls[0].reject(new Error('stale ZIP refusal'));
		await flush();
		assert.equal(
			document
				.querySelector('[data-testid="archive-export-panel"]')
				?.textContent?.includes('stale ZIP refusal'),
			false,
			'a closed attempt must not surface its rejection'
		);
		assert.ok(
			document
				.querySelector('[data-testid="archive-export-panel"]')
				?.textContent?.includes('Preparing archive'),
			'the reopened attempt stays authoritative'
		);

		calls[1].reject(new Error('current tar.gz refusal'));
		await flush();
		assert.ok(
			document
				.querySelector('[data-testid="archive-export-panel"]')
				?.textContent?.includes('current tar.gz refusal'),
			'the current attempt still reports its own failure'
		);
		assert.equal(document.querySelector('[data-testid="archive-export-ready"]'), null);
	} finally {
		ui.unmount();
	}
});

test('a server change fences the in-flight preparation', async () => {
	const calls: Deferred[] = [];
	(rpcClient.server as unknown as Record<string, unknown>).exportServerArchive = (request: {
		format: number;
	}) => {
		const entry = deferred(request.format);
		calls.push(entry);
		return entry.promise;
	};

	const target = document.createElement('div');
	document.body.appendChild(target);
	const host = mount(ExportHost as never, { target }) as unknown as {
		switchServer: () => void;
	};
	try {
		await openExport();
		await prepare();
		assert.equal(calls.length, 1);

		host.switchServer();
		await flush();
		assert.equal(
			document
				.querySelector('[data-testid="archive-export-panel"]')
				?.textContent?.includes('Preparing archive'),
			false,
			'switching servers releases the old attempt'
		);

		await prepare();
		assert.equal(calls.length, 2);
		calls[0].resolve({ sessionId: 'server-a-session', filename: 'alpha.zip', totalSize: 1n });
		await flush();
		assert.equal(
			document.querySelector('[data-testid="archive-export-ready"]'),
			null,
			'the former server result must not make the new server ready'
		);
		assert.equal(
			document
				.querySelector('[data-testid="archive-export-panel"]')
				?.textContent?.includes('alpha.zip'),
			false
		);

		calls[1].resolve({ sessionId: 'server-b-session', filename: 'beta.zip', totalSize: 2n });
		await flush();
		assert.ok(
			document
				.querySelector('[data-testid="archive-export-panel"]')
				?.textContent?.includes('beta.zip'),
			'the new server owns the ready archive'
		);
	} finally {
		unmount(host as never);
		target.remove();
	}
});

test('unmounting fences a preparation that settles after teardown', async () => {
	const calls: Deferred[] = [];
	(rpcClient.server as unknown as Record<string, unknown>).exportServerArchive = (request: {
		format: number;
	}) => {
		const entry = deferred(request.format);
		calls.push(entry);
		return entry.promise;
	};

	const ui = mountExport();
	await openExport();
	await prepare();
	assert.equal(calls.length, 1);
	ui.unmount();
	calls[0].resolve({ sessionId: 'late-session', filename: 'late.zip', totalSize: 1n });
	await flush();
	assert.equal(document.querySelector('[data-testid="archive-export-ready"]'), null);
});
