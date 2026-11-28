// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for the create wizard's modpack picker. A version list
// fetched for pack A must not replace the releases of pack B selected later.
import test, { afterEach } from 'node:test';
import assert from 'node:assert/strict';
// @ts-expect-error -- Bun supplies this test-only module; the app tsconfig omits Bun types.
import { mock } from 'bun:test';
import '../../../../test/dom-setup.mjs';

mock.module('$app/navigation', () => ({
	goto: () => Promise.resolve(),
	invalidateAll: () => Promise.resolve(),
	onNavigate: () => {},
	beforeNavigate: () => {},
	afterNavigate: () => {}
}));
mock.module('$app/paths', () => ({
	resolve: (path: string) => path,
	asset: (path: string) => path
}));
mock.module('$app/env', () => ({ browser: true }));
mock.module('$app/state', () => ({
	page: { url: new URL('http://localhost/servers/new'), params: {}, data: {} }
}));
mock.module('svelte-sonner', () => ({
	toast: {
		error: () => {},
		success: () => {},
		info: () => {}
	}
}));

const { mount, unmount } = await import('svelte');
const { create } = await import('@bufbuild/protobuf');
const { rpcClient } = await import('$lib/api/rpc-client');
const { IndexedModpackSchema, VersionSchema } =
	await import('$lib/proto/carbonpanel/v1/modpack_pb');
const { default: CreateServerPage } = await import('./+page.svelte');

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

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

const pack = (id: string) =>
	create(IndexedModpackSchema, {
		id,
		indexer: 'modrinth',
		name: id,
		summary: `${id} summary`,
		gameVersions: '["1.21.1"]',
		modLoaders: '["fabric"]',
		mcVersion: '1.21.1',
		recommendedRam: 4096,
		latestFileId: `${id}-latest`
	});
const release = (id: string) =>
	create(VersionSchema, {
		id,
		displayName: id,
		releaseType: 'release',
		gameVersions: ['1.21.1']
	});

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

test('a late version list for the previous pack cannot replace the selected pack releases', async () => {
	const configA = deferred<{ config: undefined }>();
	const versionsA = deferred<{ versions: unknown[] }>();
	const configB = deferred<{ config: undefined }>();
	const versionsB = deferred<{ versions: unknown[] }>();
	const packA = pack('pack-a');
	const packB = pack('pack-b');

	const minecraft = {
		getMinecraftVersions: async () => ({ versions: [{ id: '1.21.1' }], latest: '1.21.1' }),
		getModLoaders: async () => ({ modloaders: [] }),
		getDockerImages: async () => ({ images: [] }),
		getLoaderVersions: async () => ({ versions: [] })
	};
	const modpack = {
		listFavorites: async () => ({ modpacks: [] }),
		searchModpacks: async () => ({ modpacks: [packA, packB], total: 2 }),
		getModpack: async ({ id }: { id: string }) => ({
			modpack: id === packA.id ? packA : packB
		}),
		getModpackConfig: ({ id }: { id: string }) =>
			id === packA.id ? configA.promise : configB.promise,
		getModpackVersions: ({ id }: { id: string }) =>
			id === packA.id ? versionsA.promise : versionsB.promise
	};
	Object.assign(rpcClient, {
		minecraft,
		modpack,
		proxy: {
			getProxyStatus: async () => ({ enabled: false, baseUrl: '' }),
			getProxyListeners: async () => ({ listeners: [] })
		},
		server: { getNextAvailablePort: async () => ({ port: 25565, usedPorts: [] }) },
		node: { listNodes: async () => ({ nodes: [] }) },
		blueprint: { listBlueprints: async () => ({ blueprints: [] }) }
	});

	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(CreateServerPage as never, { target });
	const cleanup = () => {
		unmount(instance);
		target.remove();
	};

	const openPicker = async () => {
		const tiles = target.querySelectorAll('[role="radio"]');
		assert.ok(tiles.length >= 2, 'the configuration-method tiles render');
		(tiles[1] as HTMLButtonElement).click();
		await flush();
	};
	const choose = (name: string) => {
		const button = Array.from(document.querySelectorAll('button')).find((candidate) =>
			candidate.textContent?.includes(name)
		) as HTMLButtonElement | undefined;
		assert.ok(button, `the ${name} result is selectable`);
		button.click();
	};

	try {
		await flush(120);
		await openPicker();
		choose('pack-a');
		await flush(20);

		// Pack A is still loading its config/versions.
		assert.equal(configA.promise instanceof Promise, true);
		await openPicker();
		choose('pack-b');
		await flush(20);
		configB.resolve({ config: undefined });
		versionsB.resolve({ versions: [release('release-b')] });
		await flush();

		const options = () =>
			Array.from(target.querySelectorAll('#preset-version option')).map((option) =>
				option.textContent?.trim()
			);
		assert.ok(
			options().some((label) => label?.startsWith('release-b')),
			'pack B releases render after its request settles'
		);

		configA.resolve({ config: undefined });
		versionsA.resolve({ versions: [release('release-a')] });
		await flush();

		assert.ok(
			options().some((label) => label?.startsWith('release-b')),
			'pack B releases stay selected'
		);
		assert.equal(
			options().some((label) => label?.startsWith('release-a')),
			false,
			'a late pack A version list must not reach pack B'
		);
	} finally {
		cleanup();
	}
});
