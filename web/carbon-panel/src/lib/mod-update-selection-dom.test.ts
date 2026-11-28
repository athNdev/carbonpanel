// SPDX-License-Identifier: MPL-2.0
// Mounted coverage for the selection-cleared notice: the live region is stable
// and present before its text changes, and a fresh successful check replaces or
// clears the previous notice.

import test, { after } from 'node:test';
import assert from 'node:assert/strict';
import '../../test/dom-setup.mjs';
import { create } from '@bufbuild/protobuf';
import { ModLoader, ServerSchema } from './proto/carbonpanel/v1/common_pb';
import { InstalledModUpdateSchema, ModSchema, type Mod } from './proto/carbonpanel/v1/mod_pb';

const { mock } = await import('bun:test' as string);
mock.module('$app/navigation', () => ({ goto: async () => undefined }));
mock.module('$app/paths', () => ({
	resolve: (path: string) => path,
	asset: (path: string) => path
}));
mock.module('$app/state', () => ({ page: {} }));
mock.module('$app/env', () => ({ browser: false, dev: false }));

const { mount, unmount, tick, flushSync } = await import('svelte');
const { fromStore, writable } = await import('svelte/store');
const { rpcClient } = await import('./api/rpc-client');
const originalTaskClient = { ...rpcClient.task };
const originalModClient = { ...rpcClient.mod };

Object.assign(rpcClient.task, {
	listTasks: async () => ({ tasks: [] }),
	listTaskExecutions: async () => ({ executions: [] }),
	updateTask: async () => ({}),
	createTask: async () => ({}),
	toggleTask: async () => ({})
});

type ListModsRequest = { serverId: string; checkUpdates: boolean };
type UpdateModResponse = { stagedUpdates: { outcome: string }[] };
let listMods = async (_request: ListModsRequest) => ({ mods: [] as Mod[] });
let updateMod = async (_request: unknown): Promise<UpdateModResponse> => ({ stagedUpdates: [] });
Object.assign(rpcClient.mod, {
	listMods: (request: ListModsRequest) => listMods(request),
	updateMod: (request: unknown) => updateMod(request)
});
after(() => {
	Object.assign(rpcClient.task, originalTaskClient);
	Object.assign(rpcClient.mod, originalModClient);
});

const { default: ServerMods } = await import('./components/server-mods.svelte');

function installed(state: string, candidateVersion: string): Mod {
	return create(ModSchema, {
		id: 'mod-1',
		serverId: 'srv',
		fileName: 'mod.jar',
		displayName: 'Example mod',
		enabled: true,
		fileSize: 1024n,
		updatePreview: create(InstalledModUpdateSchema, {
			state,
			installedSha512: 'installed-hash',
			installedVersionId: 'version-1',
			installedVersion: '1.0.0',
			candidateVersionId: candidateVersion,
			candidateVersion: candidateVersion,
			candidateSha512: `candidate-${candidateVersion}`,
			candidateFilename: `mod-${candidateVersion}.jar`
		})
	});
}

function renderMods() {
	const server = create(ServerSchema, { id: 'srv', modLoader: ModLoader.FABRIC });
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(ServerMods, { target, props: { server, active: true } });
	return {
		target,
		async destroy() {
			await unmount(instance);
			target.remove();
		}
	};
}

function deferred<T>() {
	let resolve!: (value: T) => void;
	const promise = new Promise<T>((settle) => {
		resolve = settle;
	});
	return { promise, resolve };
}

function renderSwitchableMods(initialServerId: string) {
	const select = (id: string) => create(ServerSchema, { id, modLoader: ModLoader.FABRIC });
	const selection = writable(select(initialServerId));
	const selected = fromStore(selection);
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(ServerMods, {
		target,
		props: {
			get server() {
				return selected.current;
			},
			active: true
		}
	});
	return {
		target,
		navigate(id: string) {
			selection.set(select(id));
		},
		async destroy() {
			await unmount(instance);
			target.remove();
		}
	};
}

async function settle() {
	await new Promise((resolve) => setTimeout(resolve, 0));
	await tick();
}

function statusRegions(target: HTMLElement): HTMLParagraphElement[] {
	return Array.from(target.querySelectorAll<HTMLParagraphElement>('p[role="status"]'));
}

function checkSummary(target: HTMLElement): HTMLParagraphElement {
	const regions = statusRegions(target);
	assert.equal(regions.length, 2, 'the pane has one check summary and one selection notice region');
	return regions[0];
}

function noticeRegion(target: HTMLElement): HTMLParagraphElement {
	const regions = statusRegions(target);
	assert.equal(regions.length, 2, 'the pane has one check summary and one selection notice region');
	return regions[1];
}

function selectFirstUpdate(target: HTMLElement) {
	const checkbox = target.querySelector<HTMLInputElement>('input[type="checkbox"]');
	assert.ok(checkbox, 'the available row has no selection checkbox');
	flushSync(() => checkbox.click());
	return checkbox;
}

function checkForUpdates(target: HTMLElement) {
	const button = target.querySelector<HTMLButtonElement>('button[aria-label="Check for updates"]');
	assert.ok(button, 'the check trigger is missing');
	flushSync(() => button.click());
}

test('the selection notice region exists before its text changes', async () => {
	const ui = renderMods();
	try {
		listMods = async () => ({ mods: [installed('available', '2.0.0')] });
		await settle();
		// The region is mounted while empty, so a screen reader can observe the
		// node before the prune effect writes a notice into it.
		const region = noticeRegion(ui.target);
		assert.equal(region.textContent, '');
		assert.equal(region.isConnected, true);

		selectFirstUpdate(ui.target);
		listMods = async () => ({ mods: [installed('available', '3.0.0')] });
		checkForUpdates(ui.target);
		await settle();

		assert.match(
			noticeRegion(ui.target).textContent ?? '',
			/^Selection cleared for mod\.jar: the installed file or offered release changed; review and select it again\.$/
		);
		// Same element as before the text changed: the region was never
		// conditionally unmounted and remounted.
		assert.equal(noticeRegion(ui.target), region);
		// The notice does not reuse or duplicate the check summary region.
		const summary = checkSummary(ui.target);
		assert.notEqual(summary, region);
		assert.ok((summary.textContent ?? '').length > 0, 'the check summary is empty');
		assert.equal((summary.textContent ?? '').includes('Selection cleared'), false);
	} finally {
		await ui.destroy();
	}
});

test('a later successful check clears the earlier selection notice', async () => {
	const ui = renderMods();
	try {
		listMods = async () => ({ mods: [installed('available', '2.0.0')] });
		await settle();
		selectFirstUpdate(ui.target);
		listMods = async () => ({ mods: [installed('available', '3.0.0')] });
		checkForUpdates(ui.target);
		await settle();
		assert.match(noticeRegion(ui.target).textContent ?? '', /^Selection cleared for mod\.jar:/);

		// The fresh check offers the same rows again; nothing else is stale, so
		// the previous notice must not survive it.
		checkForUpdates(ui.target);
		await settle();
		assert.equal(noticeRegion(ui.target).textContent, '');
	} finally {
		await ui.destroy();
	}
});

test('a plain reload keeps the selection notice', async () => {
	const ui = renderMods();
	try {
		listMods = async () => ({ mods: [installed('available', '2.0.0')] });
		await settle();
		selectFirstUpdate(ui.target);
		listMods = async () => ({ mods: [installed('available', '3.0.0')] });
		checkForUpdates(ui.target);
		await settle();
		assert.match(noticeRegion(ui.target).textContent ?? '', /^Selection cleared for mod\.jar:/);

		// Toggling the mod reloads the listing without a check. Its rows carry no
		// previews, which must not be read as "the notice is resolved".
		listMods = async () => ({
			mods: [
				create(ModSchema, {
					id: 'mod-1',
					serverId: 'srv',
					fileName: 'mod.jar',
					displayName: 'Example mod',
					enabled: false,
					fileSize: 1024n
				})
			]
		});
		const toggle = Array.from(ui.target.querySelectorAll('button')).find(
			(button) => button.textContent?.trim() === 'Active'
		);
		assert.ok(toggle, 'the enable/disable control is missing');
		flushSync(() => toggle.click());
		await settle();
		assert.match(noticeRegion(ui.target).textContent ?? '', /^Selection cleared for mod\.jar:/);
	} finally {
		await ui.destroy();
	}
});

test('a late stage response cannot mutate the newly selected server', async () => {
	const ui = renderSwitchableMods('srv-a');
	const calls: ListModsRequest[] = [];
	const gate = deferred<UpdateModResponse>();
	try {
		listMods = async (request) => {
			calls.push(request);
			return { mods: request.serverId === 'srv-a' ? [installed('available', '2.0.0')] : [] };
		};
		updateMod = () => gate.promise;
		await settle();
		selectFirstUpdate(ui.target);

		const stage = Array.from(ui.target.querySelectorAll<HTMLButtonElement>('button')).find(
			(button) => button.textContent?.trim().startsWith('Stage ')
		);
		assert.ok(stage, 'the stage action is missing');
		flushSync(() => stage.click());
		await settle();

		ui.navigate('srv-b');
		await settle();
		calls.length = 0;
		assert.equal(checkSummary(ui.target).textContent, '');

		gate.resolve({ stagedUpdates: [{ outcome: 'staged' }] });
		await settle();
		await settle();

		assert.equal(
			checkSummary(ui.target).textContent,
			'',
			'the old response must not write the new server status'
		);
		assert.equal(
			calls.some((call) => call.serverId === 'srv-b' && call.checkUpdates),
			false,
			'the old response must not start a refresh for the new server'
		);
	} finally {
		await ui.destroy();
	}
});
