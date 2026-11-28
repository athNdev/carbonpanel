// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for the Mods tab: failures already toasted by the RPC
// interceptor must not add a component toast, while unmarked failures still
// toast exactly once.
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
	page: { url: new URL('http://localhost/servers/srv-1'), params: { id: 'srv-1' }, data: {} }
}));

const errorToasts: string[] = [];
mock.module('svelte-sonner', () => ({
	toast: {
		error: (message: unknown) => errorToasts.push(String(message)),
		success: () => {},
		info: () => {}
	}
}));

const { mount, unmount } = await import('svelte');
const { create } = await import('@bufbuild/protobuf');

// The browser dialog has its own seams; this test covers the tab's listing,
// toggle and delete call sites.
mock.module('$lib/components/mod-browser-dialog.svelte', () => ({ default: () => null }));

const { rpcClient } = await import('$lib/api/rpc-client');
const originalModClient = rpcClient.mod;
afterEach(() => {
	errorToasts.length = 0;
	(rpcClient as unknown as { mod: typeof originalModClient }).mod = originalModClient;
});
const { ModSchema } = await import('$lib/proto/carbonpanel/v1/mod_pb');
const { ModLoader, ServerSchema, ServerStatus } =
	await import('$lib/proto/carbonpanel/v1/common_pb');
const { default: ServerMods } = await import('./server-mods.svelte');

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

function taggedError(message: string): Error {
	return Object.assign(new Error(message), { __rpcToasted: true });
}

const installedMod = create(ModSchema, {
	id: 'mod-1',
	serverId: 'srv-1',
	fileName: 'example.jar',
	displayName: 'Example',
	description: '',
	version: '1.0.0',
	enabled: true,
	fileSize: 1024n
});

type ModClient = {
	listMods: () => Promise<{ mods: unknown[] }>;
	updateMod: () => Promise<unknown>;
	deleteMod: () => Promise<unknown>;
	importUploadedMod: () => Promise<unknown>;
};

function stubModClient(overrides: Partial<ModClient> = {}): ModClient {
	const stub: ModClient = {
		listMods: async () => ({ mods: [installedMod] }),
		updateMod: async () => ({}),
		deleteMod: async () => ({}),
		importUploadedMod: async () => ({}),
		...overrides
	};
	(rpcClient as unknown as { mod: ModClient }).mod = stub;
	return stub;
}

const server = create(ServerSchema, {
	id: 'srv-1',
	name: 'survival',
	containerId: 'container-1',
	status: ServerStatus.RUNNING,
	modLoader: ModLoader.FABRIC
});

function mountUi() {
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(ServerMods as never, { target, props: { server, active: true } });
	return {
		target,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

function clickToggle(target: HTMLElement): void {
	const button = target.querySelector(
		'button[title="Click to disable"]'
	) as HTMLButtonElement | null;
	assert.ok(button, 'the installed mod exposes a toggle');
	button.click();
}

test('a tagged mod-listing failure adds no component toast', async () => {
	stubModClient({
		listMods: async () => {
			throw taggedError('Mods rejected.');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		assert.deepEqual(errorToasts, []);
	} finally {
		ui.unmount();
	}
});

test('an unmarked mod-listing failure produces exactly one component toast', async () => {
	stubModClient({
		listMods: async () => {
			throw new Error('plain failure');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		assert.deepEqual(errorToasts, ['Failed to load mods']);
	} finally {
		ui.unmount();
	}
});

test('a tagged mod-toggle failure adds no component toast', async () => {
	stubModClient({
		updateMod: async () => {
			throw taggedError('Toggle rejected.');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		clickToggle(ui.target);
		await flush();
		assert.deepEqual(errorToasts, []);
	} finally {
		ui.unmount();
	}
});

test('an unmarked mod-toggle failure produces exactly one component toast', async () => {
	stubModClient({
		updateMod: async () => {
			throw new Error('plain failure');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		clickToggle(ui.target);
		await flush();
		assert.deepEqual(errorToasts, ['Failed to toggle mod']);
	} finally {
		ui.unmount();
	}
});
