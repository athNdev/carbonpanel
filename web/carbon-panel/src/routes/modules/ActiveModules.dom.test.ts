// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for the modules route: a failed RPC that the interceptor
// already toasted must not produce a second component toast, while the
// loadError state and retry affordance still update.
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
	page: { url: new URL('http://localhost/modules'), params: {}, data: {} }
}));

const errorToasts: string[] = [];
const successToasts: string[] = [];
mock.module('svelte-sonner', () => ({
	toast: {
		error: (message: unknown) => errorToasts.push(String(message)),
		success: (message: unknown) => successToasts.push(String(message))
	}
}));

const { mount, unmount } = await import('svelte');
const { create } = await import('@bufbuild/protobuf');

// The edit/log dialogs are unrelated to this slice and pull in UI primitives
// that are exercised by their own tests; keep the route's action seams under
// test without mounting them.
mock.module('$lib/components/server-ui/ModuleDialog.svelte', () => ({ default: () => null }));
mock.module('$lib/components/server-ui/ModuleLogsDialog.svelte', () => ({ default: () => null }));

const { rpcClient } = await import('$lib/api/rpc-client');
const originalModuleClient = rpcClient.module;
afterEach(() => {
	(rpcClient as unknown as { module: typeof originalModuleClient }).module = originalModuleClient;
});
const { ModuleSchema, ModuleStatus } = await import('$lib/proto/carbonpanel/v1/module_pb');
const { default: ActiveModules } = await import('./ActiveModules.svelte');

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

type ModuleRecord = ReturnType<typeof create<typeof ModuleSchema>>;

function moduleRecord(status: number): ModuleRecord {
	return create(ModuleSchema, {
		id: 'mod-1',
		name: 'Map renderer',
		serverId: 'srv-1',
		serverName: 'survival',
		templateName: 'bluemap',
		status
	});
}

function taggedError(message: string): Error {
	return Object.assign(new Error(message), { __rpcToasted: true });
}

function mountUi() {
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(ActiveModules as never, { target, props: {} });
	return {
		target,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

type ModuleClient = {
	listModules: () => Promise<{ modules: ModuleRecord[] }>;
	startModule: () => Promise<unknown>;
	stopModule: () => Promise<unknown>;
	restartModule: () => Promise<unknown>;
	deleteModule: () => Promise<unknown>;
};

function stubModuleClient(overrides: Partial<ModuleClient>): ModuleClient {
	const stub: ModuleClient = {
		listModules: async () => ({ modules: [] }),
		startModule: async () => ({}),
		stopModule: async () => ({}),
		restartModule: async () => ({}),
		deleteModule: async () => ({}),
		...overrides
	};
	(rpcClient as unknown as { module: ModuleClient }).module = stub;
	return stub;
}

test('a load error already toasted by the RPC interceptor adds no component toast', async () => {
	errorToasts.length = 0;
	stubModuleClient({
		listModules: async () => {
			throw taggedError('Carbon Panel could not reach the server.');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		assert.deepEqual(errorToasts, [], 'the component must not toast a tagged load failure');
		const alert = ui.target.querySelector('[role="alert"]');
		assert.ok(alert, 'the load failure must still render CarbonErrorState');
		assert.match(alert.textContent ?? '', /could not reach the server/);
		assert.match(alert.textContent ?? '', /Retry/, 'retry must remain available');
	} finally {
		ui.unmount();
	}
});

test('an unmarked load error produces exactly one component toast', async () => {
	errorToasts.length = 0;
	stubModuleClient({
		listModules: async () => {
			throw new Error('plain failure');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		assert.deepEqual(errorToasts, ['Failed to load modules']);
		assert.match(ui.target.querySelector('[role="alert"]')?.textContent ?? '', /plain failure/);
	} finally {
		ui.unmount();
	}
});

test('a failed operator action already toasted by the interceptor adds no second toast', async () => {
	errorToasts.length = 0;
	successToasts.length = 0;
	stubModuleClient({
		listModules: async () => ({ modules: [moduleRecord(ModuleStatus.STOPPED)] }),
		startModule: async () => {
			throw taggedError('Module image is unavailable.');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		const start = ui.target.querySelector('[aria-label="Start module"]');
		assert.ok(start, 'a stopped module exposes Start');
		(start as HTMLElement).click();
		await flush();
		assert.deepEqual(errorToasts, [], 'the interceptor toast must stand alone');
		assert.deepEqual(successToasts, [], 'a failed start must not report success');
	} finally {
		ui.unmount();
	}
});

test('an unmarked operator-action error still produces exactly one toast', async () => {
	errorToasts.length = 0;
	stubModuleClient({
		listModules: async () => ({ modules: [moduleRecord(ModuleStatus.STOPPED)] }),
		startModule: async () => {
			throw new Error('plain failure');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		const start = ui.target.querySelector('[aria-label="Start module"]');
		assert.ok(start, 'a stopped module exposes Start');
		(start as HTMLElement).click();
		await flush();
		assert.deepEqual(errorToasts, ['Failed to start module: plain failure']);
	} finally {
		ui.unmount();
	}
});

test('tagged stop, restart and delete failures each add no component toast', async () => {
	errorToasts.length = 0;
	stubModuleClient({
		listModules: async () => ({ modules: [moduleRecord(ModuleStatus.RUNNING)] }),
		stopModule: async () => {
			throw taggedError('Stop rejected.');
		},
		restartModule: async () => {
			throw taggedError('Restart rejected.');
		},
		deleteModule: async () => {
			throw taggedError('Delete rejected.');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		(ui.target.querySelector('[aria-label="Stop module"]') as HTMLElement).click();
		await flush();
		(ui.target.querySelector('[aria-label="Restart module"]') as HTMLElement).click();
		await flush();
		(ui.target.querySelector('[aria-label="Delete module"]') as HTMLElement).click();
		await flush();
		const confirm = Array.from(document.querySelectorAll('button')).find((button) =>
			button.textContent?.includes('Delete Module')
		) as HTMLButtonElement | undefined;
		assert.ok(confirm, 'the delete confirm must be reachable');
		confirm.click();
		await flush();
		assert.deepEqual(errorToasts, [], 'the interceptor toast must stand alone for every action');
	} finally {
		ui.unmount();
	}
});
