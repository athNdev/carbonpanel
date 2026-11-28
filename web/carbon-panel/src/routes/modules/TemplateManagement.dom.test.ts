// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for the Modules template route: failures already toasted
// by the RPC interceptor must not add a component toast, while unmarked
// listing and delete failures still toast exactly once.
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
mock.module('svelte-sonner', () => ({
	toast: {
		error: (message: unknown) => errorToasts.push(String(message)),
		success: () => {}
	}
}));

const { mount, unmount } = await import('svelte');
const { create } = await import('@bufbuild/protobuf');

mock.module('$lib/components/server-ui/ModuleTemplateCreateDialog.svelte', () => ({
	default: () => null
}));

const { rpcClient } = await import('$lib/api/rpc-client');
const originalModuleClient = rpcClient.module;
afterEach(() => {
	errorToasts.length = 0;
	(rpcClient as unknown as { module: typeof originalModuleClient }).module = originalModuleClient;
});
const { ModuleTemplateSchema, ModuleTemplateType } =
	await import('$lib/proto/carbonpanel/v1/module_pb');
const { default: TemplateManagement } = await import('./TemplateManagement.svelte');

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

function taggedError(message: string): Error {
	return Object.assign(new Error(message), { __rpcToasted: true });
}

const template = create(ModuleTemplateSchema, {
	name: 'custom-sidecar',
	description: 'Custom template',
	type: ModuleTemplateType.CUSTOM,
	icon: 'Package',
	dockerImage: 'example/sidecar:latest',
	category: 'monitoring'
});

type ModuleClient = {
	listModuleTemplates: () => Promise<{ templates: unknown[] }>;
	deleteModuleTemplate: () => Promise<unknown>;
};

function stubModuleClient(overrides: Partial<ModuleClient> = {}): ModuleClient {
	const stub: ModuleClient = {
		listModuleTemplates: async () => ({ templates: [template] }),
		deleteModuleTemplate: async () => ({}),
		...overrides
	};
	(rpcClient as unknown as { module: ModuleClient }).module = stub;
	return stub;
}

function mountUi() {
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(TemplateManagement as never, { target, props: {} });
	return {
		target,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

async function confirmDelete(target: HTMLElement): Promise<void> {
	const button = target.querySelector('[aria-label="Delete template"]') as HTMLButtonElement | null;
	assert.ok(button, 'the custom template exposes a delete action');
	button.click();
	await flush(10);
	const confirm = Array.from(document.querySelectorAll('button')).find((candidate) =>
		candidate.textContent?.includes('Delete Template')
	) as HTMLButtonElement | undefined;
	assert.ok(confirm, 'the delete confirmation must be reachable');
	confirm.click();
	await flush();
}

test('a tagged template-listing failure adds no component toast', async () => {
	stubModuleClient({
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

test('an unmarked template-listing failure produces exactly one component toast', async () => {
	stubModuleClient({
		listModuleTemplates: async () => {
			throw new Error('plain failure');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		assert.deepEqual(errorToasts, ['Failed to load module templates']);
	} finally {
		ui.unmount();
	}
});

test('a tagged template-delete failure adds no component toast', async () => {
	stubModuleClient({
		deleteModuleTemplate: async () => {
			throw taggedError('Template still in use.');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		await confirmDelete(ui.target);
		assert.deepEqual(errorToasts, []);
	} finally {
		ui.unmount();
	}
});

test('an unmarked template-delete failure produces exactly one component toast', async () => {
	stubModuleClient({
		deleteModuleTemplate: async () => {
			throw new Error('plain failure');
		}
	});
	const ui = mountUi();
	try {
		await flush();
		await confirmDelete(ui.target);
		assert.deepEqual(errorToasts, ['Failed to delete template: plain failure']);
	} finally {
		ui.unmount();
	}
});
