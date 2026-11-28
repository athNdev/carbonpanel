// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for the server Modules tab: RPC failures the interceptor
// already toasted must not add a second component toast, while unmarked
// failures still produce exactly one. The same guard covers listing loads,
// template loads and module actions.
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

// The edit/log/template dialogs have their own seams; this test exercises the
// tab's list and action call sites without mounting them.
mock.module('./ModuleDialog.svelte', () => ({ default: () => null }));
mock.module('./ModuleLogsDialog.svelte', () => ({ default: () => null }));
mock.module('./ModuleTemplateCreateDialog.svelte', () => ({ default: () => null }));

const { rpcClient } = await import('$lib/api/rpc-client');
const originalModuleClient = rpcClient.module;
afterEach(() => {
	errorToasts.length = 0;
	(rpcClient as unknown as { module: typeof originalModuleClient }).module = originalModuleClient;
});
const { ModuleSchema, ModuleStatus } = await import('$lib/proto/carbonpanel/v1/module_pb');
const { ServerSchema, ServerStatus } = await import('$lib/proto/carbonpanel/v1/common_pb');
const { default: ServerModules } = await import('./ServerModules.svelte');

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

function taggedError(message: string): Error {
	return Object.assign(new Error(message), { __rpcToasted: true });
}

type ModuleClient = {
	listModules: () => Promise<{ modules: unknown[] }>;
	listModuleTemplates: () => Promise<{ templates: unknown[] }>;
	getResolvedAliases: () => Promise<{ aliases: Record<string, string> }>;
	startModule: () => Promise<unknown>;
	stopModule: () => Promise<unknown>;
	restartModule: () => Promise<unknown>;
	deleteModule: () => Promise<unknown>;
};

const runningModule = create(ModuleSchema, {
	id: 'mod-1',
	name: 'Map renderer',
	serverId: 'srv-1',
	serverName: 'survival',
	templateName: 'bluemap',
	status: ModuleStatus.RUNNING
});

function stubModuleClient(overrides: Partial<ModuleClient> = {}): ModuleClient {
	const stub: ModuleClient = {
		listModules: async () => ({ modules: [runningModule] }),
		listModuleTemplates: async () => ({ templates: [] }),
		getResolvedAliases: async () => ({ aliases: {} }),
		startModule: async () => ({}),
		stopModule: async () => ({}),
		restartModule: async () => ({}),
		deleteModule: async () => ({}),
		...overrides
	};
	(rpcClient as unknown as { module: ModuleClient }).module = stub;
	return stub;
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
	const instance = mount(ServerModules as never, { target, props: { server, active: true } });
	return {
		target,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

function clickByLabel(target: HTMLElement, label: string): void {
	const button = target.querySelector(`[aria-label="${label}"]`) as HTMLButtonElement | null;
	assert.ok(button, `the tab exposes the ${label} action`);
	button.click();
}

test('a tagged module action failure adds no component toast', async () => {
	stubModuleClient({
		stopModule: async () => {
			throw taggedError('Stop rejected.');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		clickByLabel(ui.target, 'Stop module');
		await flush();
		assert.deepEqual(errorToasts, [], 'the interceptor toast is the only one');
	} finally {
		ui.unmount();
	}
});

test('an unmarked module action failure produces exactly one component toast', async () => {
	stubModuleClient({
		stopModule: async () => {
			throw new Error('plain failure');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		clickByLabel(ui.target, 'Stop module');
		await flush();
		assert.deepEqual(errorToasts, ['Failed to stop module: plain failure']);
	} finally {
		ui.unmount();
	}
});

test('tagged listing and template failures add no component toast', async () => {
	stubModuleClient({
		listModules: async () => {
			throw taggedError('List rejected.');
		},
		listModuleTemplates: async () => {
			throw taggedError('Templates rejected.');
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

test('unmarked listing and template failures each toast once', async () => {
	stubModuleClient({
		listModules: async () => {
			throw new Error('list failure');
		},
		listModuleTemplates: async () => {
			throw new Error('template failure');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		assert.deepEqual(errorToasts, ['Failed to load modules', 'Failed to load module templates']);
	} finally {
		ui.unmount();
	}
});
