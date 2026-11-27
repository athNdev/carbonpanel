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
const toasts: string[] = [];
const successToasts: string[] = [];
mock.module('svelte-sonner', () => ({
	toast: {
		error: (message: unknown) => toasts.push(String(message)),
		success: (message: unknown) => successToasts.push(String(message))
	}
}));

const { mount, unmount } = await import('svelte');
const { rpcClient } = await import('$lib/api/rpc-client');
const { ServerStatus } = await import('../../proto/carbonpanel/v1/common_pb');
const { ServerArchiveFormat } = await import('../../proto/carbonpanel/v1/server_pb');
const { default: ServerArchiveExport } = await import('./ServerArchiveExport.svelte');
const { default: ExportHost } = await import('../../../../test/fixtures/export-host.svelte');

// Capture the real client call and transport fetch before any test replaces
// them, so the interceptor-seam test drives the production RPC path.
const realExportServerArchive = rpcClient.server.exportServerArchive.bind(rpcClient.server);
const realFetch = globalThis.fetch;

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

async function readyArchive(sessionId = 'server-a-session', filename = 'alpha.zip') {
	(rpcClient.server as unknown as Record<string, unknown>).exportServerArchive = () =>
		Promise.resolve({ sessionId, filename, totalSize: 2n });
	await prepare();
	assert.ok(
		document.querySelector('[data-testid="archive-export-ready"]'),
		'the archive is ready before the download is attempted'
	);
}

async function acknowledgeDisclosure() {
	const checkbox = document.querySelector<HTMLInputElement>(
		'[data-testid="archive-disclosure"] input[type="checkbox"]'
	);
	assert.ok(checkbox, 'the disclosure checkbox is rendered');
	checkbox.click();
	await flush();
	assert.equal(checkbox.checked, true, 'the disclosure is acknowledged');
}

function clickDownload() {
	const button = document.querySelector<HTMLButtonElement>('[data-testid="archive-download"]');
	assert.ok(button, 'download is available');
	assert.equal(button.disabled, false, 'an acknowledged download is enabled');
	button.click();
}

function headResponse(status: number) {
	const calls: Array<{ url: string; init?: RequestInit }> = [];
	globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
		calls.push({ url: String(input), init });
		return Promise.resolve(new Response(null, { status }));
	}) as typeof fetch;
	return calls;
}

function deferredHead() {
	const calls: Array<{ url: string; init?: RequestInit }> = [];
	let respond!: (status: number) => void;
	globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
		calls.push({ url: String(input), init });
		return new Promise<Response>((resolve) => {
			respond = (status: number) => resolve(new Response(null, { status }));
		});
	}) as typeof fetch;
	return { calls, respond: (status: number) => respond(status) };
}

function spyAnchorClicks() {
	const clicks: Array<{ href: string; download: string }> = [];
	const original = HTMLAnchorElement.prototype.click;
	HTMLAnchorElement.prototype.click = function () {
		clicks.push({
			href: this.getAttribute('href') ?? '',
			download: this.getAttribute('download') ?? ''
		});
	};
	return {
		clicks,
		restore() {
			HTMLAnchorElement.prototype.click = original;
		}
	};
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

test('a stale rejection cannot toast through the real silent RPC interceptor', async () => {
	toasts.length = 0;
	type PendingCall = {
		headers: Headers;
		resolve: (response: Response) => void;
		reject: (error: unknown) => void;
	};
	const pending: PendingCall[] = [];
	const errorResponse = (message: string) =>
		new Response(JSON.stringify({ code: 'failed_precondition', message }), {
			status: 500,
			headers: { 'content-type': 'application/json' }
		});

	globalThis.fetch = ((_input: RequestInfo | URL, init?: RequestInit) => {
		let resolve!: PendingCall['resolve'];
		let reject!: PendingCall['reject'];
		const response = new Promise<Response>((ok, fail) => {
			resolve = ok;
			reject = fail;
		});
		pending.push({ headers: new Headers(init?.headers), resolve, reject });
		return response;
	}) as typeof fetch;

	const ui = mountExport();
	try {
		(rpcClient.server as unknown as Record<string, unknown>).exportServerArchive =
			realExportServerArchive;

		await openExport();
		await prepare();
		assert.equal(pending.length, 1, 'the first preparation reached the transport');

		window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
		await flush();
		await openExport();
		await chooseFormat('TAR.GZ (.tar.gz)');
		await prepare();
		assert.equal(pending.length, 2, 'the reopened attempt reached the transport');

		pending[0].resolve(errorResponse('stale ZIP refusal'));
		await flush();
		assert.deepEqual(
			toasts,
			[],
			'the interceptor and the fenced component stay quiet for a stale rejection'
		);
		assert.ok(
			document
				.querySelector('[data-testid="archive-export-panel"]')
				?.textContent?.includes('Preparing archive'),
			'the reopened attempt stays authoritative'
		);

		pending[1].resolve(errorResponse('current tar.gz refusal'));
		await flush();
		assert.equal(toasts.length, 1, 'only the current attempt reports a failure');
		assert.match(toasts[0] ?? '', /current tar\.gz refusal/);
		assert.ok(
			document
				.querySelector('[data-testid="archive-export-panel"]')
				?.textContent?.includes('current tar.gz refusal'),
			'the current attempt shows its own failure inline'
		);
		assert.ok(
			pending.every((call) => call.headers.get('x-silent-request') === 'true'),
			'the export request is silent so the shared interceptor cannot toast'
		);
	} finally {
		ui.unmount();
		globalThis.fetch = realFetch;
		(rpcClient.server as unknown as Record<string, unknown>).exportServerArchive =
			realExportServerArchive;
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

test('a refused download preflight reports the status and keeps the modal open', async () => {
	toasts.length = 0;
	successToasts.length = 0;
	const anchor = spyAnchorClicks();
	const fetchCalls = headResponse(403);
	const ui = mountExport();
	try {
		await openExport();
		await readyArchive();
		await acknowledgeDisclosure();
		clickDownload();
		await flush();

		assert.equal(fetchCalls.length, 1, 'the download is preflighted once');
		assert.equal(fetchCalls[0].url, '/api/v1/download/server-a-session');
		assert.equal(fetchCalls[0].init?.method, 'HEAD');
		assert.equal(fetchCalls[0].init?.credentials, 'same-origin');
		assert.deepEqual(toasts, ['Archive download was refused (HTTP 403).']);
		assert.deepEqual(successToasts, [], 'a refused download never claims it started');
		assert.equal(anchor.clicks.length, 0, 'a refused download must not click the anchor');
		assert.ok(
			document.querySelector('[data-testid="archive-export-panel"]'),
			'the modal stays open for a refusal'
		);
		assert.ok(
			document.querySelector('[data-testid="archive-export-ready"]'),
			'the ready archive stays available to retry'
		);
		const checkbox = document.querySelector<HTMLInputElement>(
			'[data-testid="archive-disclosure"] input[type="checkbox"]'
		);
		assert.equal(checkbox?.checked, true, 'the acknowledgement survives the refusal');
		const button = document.querySelector<HTMLButtonElement>('[data-testid="archive-download"]');
		assert.equal(button?.disabled, false, 'the operator can retry after a refusal');
	} finally {
		ui.unmount();
		anchor.restore();
		globalThis.fetch = realFetch;
	}
});

test('an accepted download preflight starts the download once and closes the modal', async () => {
	toasts.length = 0;
	successToasts.length = 0;
	const anchor = spyAnchorClicks();
	const fetchCalls = headResponse(200);
	const ui = mountExport();
	try {
		await openExport();
		await readyArchive();
		await acknowledgeDisclosure();
		clickDownload();
		await flush();

		assert.equal(fetchCalls.length, 1, 'the download is preflighted once');
		assert.equal(fetchCalls[0].init?.method, 'HEAD');
		assert.deepEqual(toasts, []);
		assert.deepEqual(successToasts, ['Archive download started.']);
		assert.deepEqual(anchor.clicks, [
			{ href: '/api/v1/download/server-a-session', download: 'alpha.zip' }
		]);
		assert.equal(
			document.querySelector('[data-testid="archive-export-panel"]'),
			null,
			'an accepted download closes the modal'
		);
	} finally {
		ui.unmount();
		anchor.restore();
		globalThis.fetch = realFetch;
	}
});

test('a stale refused preflight cannot toast or close a reopened dialog', async () => {
	toasts.length = 0;
	successToasts.length = 0;
	const anchor = spyAnchorClicks();
	const pending = deferredHead();
	const ui = mountExport();
	try {
		await openExport();
		await readyArchive();
		await acknowledgeDisclosure();
		clickDownload();
		await flush();
		assert.equal(pending.calls.length, 1, 'the download preflight is in flight');

		window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
		await flush();
		assert.equal(document.querySelector('[data-testid="archive-export-panel"]'), null);

		await openExport();
		await readyArchive();
		assert.ok(
			document.querySelector('[data-testid="archive-export-ready"]'),
			'the reopened dialog is ready'
		);

		pending.respond(403);
		await flush();
		assert.deepEqual(toasts, [], 'a stale refusal must not toast');
		assert.deepEqual(successToasts, [], 'a stale refusal must not claim success');
		assert.equal(anchor.clicks.length, 0);
		assert.ok(
			document.querySelector('[data-testid="archive-export-panel"]'),
			'a stale refusal must not close the new dialog'
		);
		assert.ok(
			document.querySelector('[data-testid="archive-export-ready"]'),
			'the new dialog keeps its own ready archive'
		);
	} finally {
		ui.unmount();
		anchor.restore();
		globalThis.fetch = realFetch;
	}
});
