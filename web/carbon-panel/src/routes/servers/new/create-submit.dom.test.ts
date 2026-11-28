// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for the create-server wizard's submission fence. Activating
// the real Create Server control twice in one tick must still issue exactly one
// createServer RPC, one navigation and at most one failure report.
import test, { afterEach } from 'node:test';
import assert from 'node:assert/strict';
// @ts-expect-error -- Bun supplies this test-only module; the app tsconfig omits Bun types.
import { mock } from 'bun:test';
import '../../../../test/dom-setup.mjs';

const gotoCalls: string[] = [];
const errorToasts: string[] = [];
const successToasts: string[] = [];
const infoToasts: string[] = [];

mock.module('$app/navigation', () => ({
	goto: (path: string) => {
		gotoCalls.push(path);
		return Promise.resolve();
	},
	invalidateAll: () => Promise.resolve(),
	onNavigate: () => {},
	beforeNavigate: () => {},
	afterNavigate: () => {},
	pushState: () => {},
	replaceState: () => {},
	preloadData: () => Promise.resolve()
}));
mock.module('$app/paths', () => ({
	resolve: (path: string, params?: Record<string, string>) =>
		params?.id ? `/servers/${params.id}` : path,
	asset: (path: string) => path
}));
mock.module('$app/env', () => ({ browser: true }));
mock.module('$app/state', () => ({
	page: { url: new URL('http://localhost/servers/new'), params: {}, data: {} }
}));
mock.module('svelte-sonner', () => ({
	toast: {
		error: (message: unknown) => errorToasts.push(String(message)),
		success: (message: unknown) => successToasts.push(String(message)),
		info: (message: unknown) => infoToasts.push(String(message))
	}
}));

const { mount, unmount } = await import('svelte');
const { rpcClient } = await import('$lib/api/rpc-client');
const { NodeStatus } = await import('$lib/proto/carbonpanel/v1/node_pb');
const { default: CreateServerPage } = await import('./+page.svelte');

const originalClients = {
	minecraft: rpcClient.minecraft,
	modpack: rpcClient.modpack,
	proxy: rpcClient.proxy,
	server: rpcClient.server,
	node: rpcClient.node,
	blueprint: rpcClient.blueprint
};
afterEach(() => {
	Object.assign(rpcClient, originalClients);
});

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

type Deferred<T> = {
	promise: Promise<T>;
	resolve: (value: T) => void;
	reject: (error: unknown) => void;
};
function deferred<T>(): Deferred<T> {
	let resolve!: (value: T) => void;
	let reject!: (error: unknown) => void;
	const promise = new Promise<T>((yes, no) => {
		resolve = yes;
		reject = no;
	});
	return { promise, resolve, reject };
}

type CreateResponse = { server: { id: string; name: string } };

const node = {
	id: 'node-a',
	name: 'Node A',
	enabled: true,
	status: NodeStatus.ONLINE,
	host: 'docker-a',
	availableMemoryMb: 8192,
	allocatedMemoryMb: 0
};

function installBaseClients() {
	Object.assign(rpcClient, {
		minecraft: {
			getMinecraftVersions: async () => ({
				versions: [{ id: '1.21.1' }],
				latest: '1.21.1'
			}),
			getModLoaders: async () => ({ modloaders: [] }),
			getDockerImages: async () => ({ images: [] }),
			getLoaderVersions: async () => ({ versions: [] })
		},
		modpack: {
			listFavorites: async () => ({ modpacks: [] })
		},
		proxy: {
			getProxyStatus: async () => ({ enabled: false, baseUrl: '' }),
			getProxyListeners: async () => ({ listeners: [] })
		},
		server: {
			getNextAvailablePort: async () => ({ port: 25565, usedPorts: [] }),
			previewServerArchive: async () => ({})
		},
		node: { listNodes: async () => ({ nodes: [node] }) },
		blueprint: { listBlueprints: async () => ({ blueprints: [] }) }
	});
}

function stubCreateServer() {
	const calls: unknown[] = [];
	const attempt = deferred<CreateResponse>();
	(rpcClient.server as unknown as Record<string, unknown>).createServer = (request: unknown) => {
		calls.push(request);
		return attempt.promise;
	};
	return { calls, attempt };
}

function mountCreate() {
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(CreateServerPage as never, { target });
	return {
		target,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

function findButton(target: HTMLElement, label: string): HTMLButtonElement {
	const button = Array.from(target.querySelectorAll('button')).find((candidate) =>
		candidate.textContent?.includes(label)
	);
	assert.ok(button, `the ${label} control is rendered`);
	return button as HTMLButtonElement;
}

async function setServerName(target: HTMLElement, name: string) {
	const input = target.querySelector<HTMLInputElement>('#name');
	assert.ok(input, 'the server name control is rendered');
	input.value = name;
	input.dispatchEvent(new Event('input', { bubbles: true }));
	await flush(20);
}

async function reachAddonStep(target: HTMLElement) {
	// Step 1 -> 2 -> 3 -> 4 -> 5 through the real wizard controls. The mocked
	// catalog supplies the latest Minecraft version and a free port, so each
	// step's own validation is exercised rather than bypassed.
	for (let step = 0; step < 4; step++) {
		findButton(target, 'Next Step').click();
		await flush(30);
	}
	const heading = Array.from(target.querySelectorAll('h2')).find((element) =>
		element.textContent?.includes('Step 5: Add-ons')
	);
	assert.ok(heading, 'the final add-ons step is reached');
}

function createControl(target: HTMLElement): HTMLButtonElement {
	const button = findButton(target, 'Create Server');
	assert.equal(button.type, 'submit', 'the final control submits the real form');
	assert.ok(button.closest('form'), 'the submit control belongs to the wizard form');
	return button;
}

test('a double activation issues one createServer call and one navigation', async () => {
	errorToasts.length = 0;
	successToasts.length = 0;
	infoToasts.length = 0;
	gotoCalls.length = 0;
	installBaseClients();
	const { calls, attempt } = stubCreateServer();

	const ui = mountCreate();
	try {
		await flush(120);
		await setServerName(ui.target, 'double-click-guard');
		await reachAddonStep(ui.target);

		const submit = createControl(ui.target);
		submit.click();
		submit.click();
		await flush(30);

		assert.equal(calls.length, 1, 'one user action reaches the RPC once');
		assert.equal(gotoCalls.length, 0, 'navigation waits for the create response');
		assert.deepEqual(errorToasts, [], 'no failure is reported while the create is pending');
		assert.deepEqual(successToasts, [], 'no success is claimed while the create is pending');

		attempt.resolve({ server: { id: 'server-1', name: 'double-click-guard' } });
		await flush();

		assert.equal(calls.length, 1, 'the pending create is never duplicated');
		assert.deepEqual(gotoCalls, ['/servers/server-1'], 'the created server is opened once');
		assert.deepEqual(errorToasts, [], 'a successful create reports no failure');
		assert.deepEqual(successToasts, [], 'a successful create does not double-report success');
	} finally {
		ui.unmount();
	}
});

test('a single activation still issues one createServer call and one navigation', async () => {
	errorToasts.length = 0;
	successToasts.length = 0;
	infoToasts.length = 0;
	gotoCalls.length = 0;
	installBaseClients();
	const { calls, attempt } = stubCreateServer();

	const ui = mountCreate();
	try {
		await flush(120);
		await setServerName(ui.target, 'single-click-control');
		await reachAddonStep(ui.target);

		createControl(ui.target).click();
		await flush(30);

		assert.equal(calls.length, 1, 'the normal single submit reaches the RPC');
		assert.equal(gotoCalls.length, 0, 'navigation waits for the create response');

		attempt.resolve({ server: { id: 'server-2', name: 'single-click-control' } });
		await flush();

		assert.equal(calls.length, 1, 'the normal single submit is not repeated');
		assert.deepEqual(gotoCalls, ['/servers/server-2'], 'the created server is opened once');
		assert.deepEqual(errorToasts, []);
		assert.deepEqual(successToasts, []);
	} finally {
		ui.unmount();
	}
});

test('a double activation reports one refusal instead of two', async () => {
	errorToasts.length = 0;
	successToasts.length = 0;
	infoToasts.length = 0;
	gotoCalls.length = 0;
	installBaseClients();
	const { calls, attempt } = stubCreateServer();

	const ui = mountCreate();
	try {
		await flush(120);
		await setServerName(ui.target, 'refused-create');
		await reachAddonStep(ui.target);

		const submit = createControl(ui.target);
		submit.click();
		submit.click();
		await flush(30);

		assert.equal(calls.length, 1, 'the refusal path is also fenced to one RPC call');

		attempt.reject(new Error('node offline'));
		await flush();

		assert.equal(calls.length, 1);
		assert.equal(errorToasts.length, 1, 'the operator sees exactly one failure report');
		assert.match(errorToasts[0] ?? '', /Failed to create server: node offline/);
		assert.deepEqual(successToasts, [], 'a refused create never claims success');
		assert.deepEqual(gotoCalls, [], 'a refused create does not navigate');
	} finally {
		ui.unmount();
	}
});
