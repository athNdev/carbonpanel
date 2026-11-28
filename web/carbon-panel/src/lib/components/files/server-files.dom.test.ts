// SPDX-License-Identifier: MPL-2.0
// Real-DOM regression coverage for the file list's stale-response fence. A
// listing started for server A can resolve after the operator switched to
// server B; it must not overwrite B's tree, terminate B's loading state, or
// toast a failure for the discarded view.
import test, { afterEach } from 'node:test';
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

const errorToasts: string[] = [];
mock.module('svelte-sonner', () => ({
	toast: {
		error: (message: unknown) => errorToasts.push(String(message)),
		success: () => {}
	}
}));

const { createClassComponent } = await import('svelte/legacy');
const { create } = await import('@bufbuild/protobuf');
const { rpcClient } = await import('$lib/api/rpc-client');
const { FileInfoSchema } = await import('$lib/proto/carbonpanel/v1/file_pb');
const { ServerSchema, ServerStatus } = await import('$lib/proto/carbonpanel/v1/common_pb');
const { default: ServerFiles } = await import('./server-files.svelte');

type FileClient = {
	listFiles: (request: { serverId: string; path: string; tree: boolean }) => Promise<{
		files: unknown[];
	}>;
};
type FileResponse = Awaited<ReturnType<FileClient['listFiles']>>;

const originalFileClient = rpcClient.file;
afterEach(() => {
	errorToasts.length = 0;
	(rpcClient as unknown as { file: FileClient }).file = originalFileClient as unknown as FileClient;
});

function stubFileClient(listFiles: FileClient['listFiles']) {
	(rpcClient as unknown as { file: FileClient }).file = { listFiles };
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

const flush = async (ms = 50) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

function server(id: string, name: string) {
	return create(ServerSchema, {
		id,
		name,
		containerId: `container-${id}`,
		status: ServerStatus.RUNNING
	});
}

function file(name: string): unknown {
	return create(FileInfoSchema, {
		name,
		path: `/${name}`,
		isDir: false,
		size: 12n,
		modified: 0n,
		isEditable: true,
		children: []
	});
}

function listResponse(name: string): FileResponse {
	return { files: [file(name)] };
}

function mountFiles(initialServer: ReturnType<typeof server>) {
	const target = document.createElement('div');
	document.body.appendChild(target);
	// The legacy host is a public Svelte API for imperative mounting with
	// reactive `$set` props, which is exactly what the server-detail parent does
	// when the operator selects another server.
	const instance = createClassComponent({
		component: ServerFiles as never,
		target,
		props: { server: initialServer, active: true }
	}) as unknown as {
		$set: (props: Record<string, unknown>) => void;
		$destroy: () => void;
	};
	let destroyed = false;
	return {
		target,
		setServer(next: ReturnType<typeof server>) {
			instance.$set({ server: next });
		},
		unmount() {
			if (destroyed) return;
			destroyed = true;
			instance.$destroy();
			target.remove();
		}
	};
}

function rowPaths(target: HTMLElement): string[] {
	return Array.from(target.querySelectorAll('[role="treeitem"]')).map(
		(row) => row.getAttribute('data-path') ?? ''
	);
}

function spinnerCount(target: HTMLElement): number {
	return target.querySelectorAll('.animate-spin').length;
}

test('a late listing for the previous server cannot replace the current tree', async () => {
	errorToasts.length = 0;
	const pending = new Map<string, ReturnType<typeof deferred<FileResponse>>>();
	const calls: string[] = [];
	stubFileClient((request) => {
		calls.push(request.serverId);
		const next = deferred<FileResponse>();
		pending.set(request.serverId, next);
		return next.promise;
	});

	const ui = mountFiles(server('server-a', 'Alpha'));
	try {
		await flush(10);
		assert.deepEqual(calls, ['server-a'], 'the active tab lists the mounted server');
		assert.deepEqual(rowPaths(ui.target), [], 'the tree waits for the listing');

		ui.setServer(server('server-b', 'Beta'));
		await flush();
		assert.deepEqual(calls, ['server-a', 'server-b'], 'switching servers lists the new one');

		// B settles first with its own tree.
		pending.get('server-b')?.resolve(listResponse('beta.txt'));
		await flush();
		assert.deepEqual(rowPaths(ui.target), ['/beta.txt'], "the current server's tree renders");
		assert.equal(spinnerCount(ui.target), 0, 'the current listing finished loading');

		// A settles last; it belongs to the discarded view.
		pending.get('server-a')?.resolve(listResponse('alpha.txt'));
		await flush();

		assert.deepEqual(rowPaths(ui.target), ['/beta.txt'], "B's tree must survive A's late response");
		assert.equal(
			ui.target.textContent?.includes('alpha.txt'),
			false,
			"A's files must never appear on B's tab"
		);
		assert.equal(spinnerCount(ui.target), 0, 'B stays loaded after the obsolete response');
		assert.deepEqual(errorToasts, []);
	} finally {
		ui.unmount();
	}
});

test('an obsolete failure neither toasts nor clears the current loading state', async () => {
	errorToasts.length = 0;
	const pending = new Map<string, ReturnType<typeof deferred<FileResponse>>>();
	stubFileClient((request) => {
		const next = deferred<FileResponse>();
		pending.set(request.serverId, next);
		return next.promise;
	});

	const ui = mountFiles(server('server-a', 'Alpha'));
	try {
		await flush(10);
		assert.equal(pending.has('server-a'), true, "A's listing is in flight before the switch");

		ui.setServer(server('server-b', 'Beta'));
		await flush();
		assert.equal(spinnerCount(ui.target), 1, "B's listing is still in flight");

		pending.get('server-a')?.reject(new Error('server A listing failed'));
		await flush();

		assert.deepEqual(errorToasts, [], 'an obsolete failure must not toast');
		assert.equal(
			spinnerCount(ui.target),
			1,
			"an obsolete failure must not clear B's loading state"
		);

		pending.get('server-b')?.resolve(listResponse('beta.txt'));
		await flush();
		assert.deepEqual(rowPaths(ui.target), ['/beta.txt'], "B's tree renders after its own response");
		assert.equal(spinnerCount(ui.target), 0, "B's loading state terminates on its response");
		assert.deepEqual(errorToasts, []);
	} finally {
		ui.unmount();
	}
});

test('a current listing failure still toasts and terminates loading', async () => {
	errorToasts.length = 0;
	stubFileClient(() => Promise.reject(new Error('listing failed')));
	const ui = mountFiles(server('server-a', 'Alpha'));
	try {
		await flush();
		assert.deepEqual(errorToasts, ['Failed to load files']);
		assert.equal(spinnerCount(ui.target), 0, 'a current failure must not leave a spinner');
	} finally {
		ui.unmount();
	}
});
