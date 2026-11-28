// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for the server configuration staging window. An older
// getUpdateBatch read may resolve after the operator saves; it must not
// overwrite the window they just stored.
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
	afterNavigate: () => {}
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
	toast: {
		error: () => {},
		success: () => {},
		info: () => {}
	}
}));

const { mount, unmount } = await import('svelte');
const { create } = await import('@bufbuild/protobuf');
const { rpcClient } = await import('$lib/api/rpc-client');
const { ServerSchema, ServerStatus } = await import('$lib/proto/carbonpanel/v1/common_pb');
const { ConfigApplyWindow, ConfigCategorySchema, ConfigPropertySchema } =
	await import('$lib/proto/carbonpanel/v1/config_pb');
const { default: ServerConfiguration } = await import('./server-configuration.svelte');

const originalConfigClient = rpcClient.config;
afterEach(() => {
	(rpcClient as unknown as { config: typeof originalConfigClient }).config = originalConfigClient;
});

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

const flush = async (ms = 60) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

const server = create(ServerSchema, {
	id: 'srv-1',
	name: 'survival',
	status: ServerStatus.STOPPED
});

function config(value: string) {
	return [
		create(ConfigCategorySchema, {
			name: 'Gameplay',
			properties: [
				create(ConfigPropertySchema, {
					key: 'motd',
					label: 'MOTD',
					value,
					type: 'text',
					required: false,
					system: false,
					ephemeral: false,
					envVar: 'MOTD',
					options: []
				})
			]
		})
	];
}

function batch(window: number, applyAtUnix: bigint) {
	return { open: true, window, applyAtUnix, idleMinutes: 0, items: [] };
}

// The component renders the UTC value as a local datetime input; mirror its
// conversion instead of hard-coding a workstation timezone.
function localValue(unix: number): string {
	const when = new Date(unix * 1000);
	const pad = (n: number) => String(n).padStart(2, '0');
	return `${when.getFullYear()}-${pad(when.getMonth() + 1)}-${pad(when.getDate())}T${pad(
		when.getHours()
	)}:${pad(when.getMinutes())}`;
}

test('a late staging-window read cannot overwrite the window the operator just saved', async () => {
	const batches: Deferred<ReturnType<typeof batch>>[] = [];
	const savedUnix = Math.floor(new Date(2026, 11, 1, 10, 0, 0).getTime() / 1000);
	const staleUnix = savedUnix + 7 * 24 * 60 * 60;
	const configClient = {
		getServerConfig: async () => ({ categories: config('old') }),
		getUpdateBatch: () => {
			const pending = deferred<ReturnType<typeof batch>>();
			batches.push(pending);
			return pending.promise;
		},
		updateServerConfig: async () => ({ categories: config('new') })
	};
	(rpcClient as unknown as { config: typeof configClient }).config = configClient;

	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(ServerConfiguration, { target, props: { server } });
	const cleanup = () => {
		unmount(instance);
		target.remove();
	};

	try {
		await flush();
		assert.ok(batches.length >= 2, 'mount reads the open batch');
		const motd = target.querySelector('input[id$="-motd"]') as HTMLInputElement | null;
		assert.ok(motd, 'the configuration field renders');
		motd.value = 'new';
		motd.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true, data: 'new' }));
		await flush(10);

		const pickTime = target.querySelector(
			'input[type="radio"][value="time"]'
		) as HTMLInputElement | null;
		assert.ok(pickTime, 'the staging window offers a timestamp');
		pickTime.click();
		await flush(10);

		const applyAt = target.querySelector('input[type="datetime-local"]') as HTMLInputElement;
		assert.ok(applyAt, 'the timestamp control renders');
		applyAt.value = localValue(savedUnix);
		applyAt.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }));
		await flush(10);

		const stage = Array.from(target.querySelectorAll('button')).find(
			(button) => button.textContent?.trim() === 'Stage'
		) as HTMLButtonElement | undefined;
		assert.ok(stage, 'the staging action is available');
		stage.click();
		await flush();

		const savedBatch = batches[batches.length - 1];
		assert.ok(savedBatch, 'saving reads the stored window back');
		savedBatch.resolve(batch(ConfigApplyWindow.AT_TIME, BigInt(savedUnix)));
		await flush();
		assert.equal(
			(target.querySelector('input[type="datetime-local"]') as HTMLInputElement).value,
			localValue(savedUnix),
			'the saved window is shown after the save settles'
		);

		// Resolve the reads that started before the save, newest-first.
		for (let i = batches.length - 2; i >= 0; i -= 1) {
			batches[i].resolve(batch(ConfigApplyWindow.AT_TIME, BigInt(staleUnix)));
			await flush(10);
		}
		assert.equal(
			(target.querySelector('input[type="datetime-local"]') as HTMLInputElement).value,
			localValue(savedUnix),
			'a pre-save batch read must not overwrite the saved window'
		);
	} finally {
		cleanup();
	}
});
