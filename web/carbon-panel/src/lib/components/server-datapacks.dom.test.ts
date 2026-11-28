// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for the Datapacks tab: a delete failure already toasted by
// the RPC interceptor adds no component toast, while an unmarked failure still
// toasts exactly once.
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
		success: () => {}
	}
}));

const { mount, unmount } = await import('svelte');
const { create } = await import('@bufbuild/protobuf');

mock.module('$lib/components/datapack-browser-dialog.svelte', () => ({ default: () => null }));

const { rpcClient } = await import('$lib/api/rpc-client');
const originalDatapackClient = rpcClient.datapack;
afterEach(() => {
	errorToasts.length = 0;
	(rpcClient as unknown as { datapack: typeof originalDatapackClient }).datapack =
		originalDatapackClient;
});
const { InstalledDatapackSchema } = await import('$lib/proto/carbonpanel/v1/datapack_pb');
const { ServerSchema, ServerStatus } = await import('$lib/proto/carbonpanel/v1/common_pb');
const { default: ServerDatapacks } = await import('./server-datapacks.svelte');

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

function taggedError(message: string): Error {
	return Object.assign(new Error(message), { __rpcToasted: true });
}

const datapack = create(InstalledDatapackSchema, {
	fileName: 'example.zip',
	displayName: 'Example',
	size: 2048n,
	packFormat: '48'
});

type DatapackClient = {
	listDatapacks: () => Promise<{ datapacks: unknown[] }>;
	deleteDatapack: () => Promise<unknown>;
	uploadDatapack: () => Promise<unknown>;
	installDatapack: () => Promise<unknown>;
};

function stubDatapackClient(overrides: Partial<DatapackClient> = {}): DatapackClient {
	const stub: DatapackClient = {
		listDatapacks: async () => ({ datapacks: [datapack] }),
		deleteDatapack: async () => ({}),
		uploadDatapack: async () => ({}),
		installDatapack: async () => ({}),
		...overrides
	};
	(rpcClient as unknown as { datapack: DatapackClient }).datapack = stub;
	return stub;
}

const server = create(ServerSchema, {
	id: 'srv-1',
	name: 'survival',
	containerId: 'container-1',
	status: ServerStatus.RUNNING,
	mcVersion: '1.21'
});

function mountUi() {
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(ServerDatapacks as never, { target, props: { server } });
	return {
		target,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

function clickDelete(target: HTMLElement): void {
	const button = target.querySelector('[aria-label="Delete Example"]') as HTMLButtonElement | null;
	assert.ok(button, 'the installed datapack exposes a delete action');
	button.click();
}

test('a tagged datapack-delete failure adds no component toast', async () => {
	stubDatapackClient({
		deleteDatapack: async () => {
			throw taggedError('Delete rejected.');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		clickDelete(ui.target);
		await flush();
		assert.deepEqual(errorToasts, []);
	} finally {
		ui.unmount();
	}
});

test('an unmarked datapack-delete failure produces exactly one component toast', async () => {
	stubDatapackClient({
		deleteDatapack: async () => {
			throw new Error('plain failure');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		clickDelete(ui.target);
		await flush();
		assert.deepEqual(errorToasts, ['plain failure']);
	} finally {
		ui.unmount();
	}
});
