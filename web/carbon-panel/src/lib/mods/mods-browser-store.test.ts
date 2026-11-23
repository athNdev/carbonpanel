// SPDX-License-Identifier: MPL-2.0

// Behavior tests for the shared mods-browser state machine. Bun does not
// compile Svelte runes, so `$state` is mocked as an identity before the module
// is imported (the same isolation the websocket store test relies on) and the
// machine is driven purely through its events.

import { describe, test } from 'node:test';
import assert from 'node:assert/strict';

(globalThis as unknown as { $state: <T>(v: T) => T }).$state = (v) => v;

const { createModsBrowserStore } = await import('./mods-browser-store.svelte');
type Store = ReturnType<typeof createModsBrowserStore>;

import type { ModsBrowserPorts } from './mods-browser-store.svelte';
import type { PlanVersion } from './dependency-plan';

const result = (id: string, platform = 'modrinth') => ({
	id,
	slug: id,
	title: id,
	description: '',
	iconUrl: '',
	author: '',
	downloads: 1,
	categories: [],
	platform,
	installed: false
});

const version = (id: string, deps: PlanVersion['dependencies'] = []): PlanVersion => ({
	id,
	version_number: id,
	name: id,
	version_type: 'release',
	file_name: `${id}.jar`,
	download_url: `https://cdn/${id}.jar`,
	file_size: 1,
	dependencies: deps
});

function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (error: unknown) => void;
	const promise = new Promise<T>((res, rej) => {
		resolve = res;
		reject = rej;
	});
	return { promise, resolve, reject };
}

function makePorts(overrides: Partial<ModsBrowserPorts> = {}): ModsBrowserPorts {
	return {
		search: async () => ({ results: [result('mod-a')] }),
		versions: async () => ({
			versions: [version('1.0.0', [{ project_id: 'required-dep', dependency_type: 'required' }])]
		}),
		install: async () => ({
			success: true,
			installed: ['1.0.0.jar'],
			errors: [],
			message: 'Installed'
		}),
		...overrides
	};
}

function makeStore(overrides: Partial<ModsBrowserPorts> = {}): Store {
	return createModsBrowserStore(makePorts(overrides));
}

const serverTarget = {
	kind: 'server' as const,
	id: 'srv-1',
	loader: 'fabric',
	mcVersion: '1.21.1'
};

async function openWithResults(): Promise<Store> {
	const store = makeStore();
	store.setTarget(serverTarget);
	await store.submitSearch();
	return store;
}

describe('mods browser store — catalog region', () => {
	test('a search round-trips through the ports and clears the error state', async () => {
		const store = await openWithResults();
		assert.equal(store.searching, false);
		assert.equal(store.results.length, 1);
		assert.equal(store.results[0].id, 'mod-a');
		assert.equal(store.searchError, '');
	});

	test('a slow earlier search response can never overwrite newer results (E15)', async () => {
		const first = deferred<{ results: ReturnType<typeof result>[] }>();
		const second = deferred<{ results: ReturnType<typeof result>[] }>();
		const queue = [first.promise, second.promise];
		const store = makeStore({
			search: () => queue.shift() as Promise<{ results: ReturnType<typeof result>[] }>
		});
		store.setTarget(serverTarget);
		const firstRun = store.submitSearch();
		const secondRun = store.submitSearch();
		second.resolve({ results: [result('newer')] });
		await secondRun;
		assert.deepEqual(
			store.results.map((entry) => entry.id),
			['newer']
		);
		first.resolve({ results: [result('stale')] });
		await firstRun;
		assert.deepEqual(
			store.results.map((entry) => entry.id),
			['newer'],
			'the superseded response must be dropped'
		);
		assert.equal(store.searching, false);
	});

	test('setPlatform switches the catalog and re-searches', async () => {
		const requests: string[] = [];
		const store = makeStore({
			search: async (request) => {
				requests.push(request.platform);
				return { results: [] };
			}
		});
		store.setTarget(serverTarget);
		await store.setPlatform('curseforge');
		assert.equal(store.platform, 'curseforge');
		assert.deepEqual(requests, ['curseforge']);
	});

	test('search failures land in a recoverable error state, not an empty success', async () => {
		const store = makeStore({
			search: async () => {
				throw new Error('provider unreachable');
			}
		});
		store.setTarget(serverTarget);
		await store.submitSearch();
		assert.match(store.searchError, /provider unreachable/);
		assert.equal(store.results.length, 0);
		assert.equal(store.searching, false);
	});
});

describe('mods browser store — detail region', () => {
	test('selectResult loads versions and pre-selects required dependencies', async () => {
		const store = await openWithResults();
		await store.selectResult(store.results[0]);
		assert.equal(store.versions.length, 1);
		assert.equal(store.selectedVersion?.id, '1.0.0');
		assert.equal(store.dependencySelection['required-dep'], true);
	});

	test('a stale versions response is dropped when a newer result is selected (E15)', async () => {
		const first = deferred<{ versions: PlanVersion[] }>();
		const second = deferred<{ versions: PlanVersion[] }>();
		const queue = [first.promise, second.promise];
		const guarded = createModsBrowserStore({
			...makePorts(),
			versions: () => queue.shift() as Promise<{ versions: PlanVersion[] }>
		});
		guarded.setTarget(serverTarget);
		await guarded.submitSearch();
		const runA = guarded.selectResult(result('a'));
		const runB = guarded.selectResult(result('b'));
		second.resolve({ versions: [version('b-1')] });
		await runB;
		assert.deepEqual(
			guarded.versions.map((entry) => entry.id),
			['b-1']
		);
		first.resolve({ versions: [version('a-1')] });
		await runA;
		assert.equal(guarded.selectedResult?.id, 'b', 'the newer selection must win');
		assert.deepEqual(
			guarded.versions.map((entry) => entry.id),
			['b-1'],
			'the superseded versions response must be dropped'
		);
	});

	test('selectResult is refused while an install is running (selection retention)', async () => {
		const install = deferred<{
			success: boolean;
			installed: string[];
			errors: string[];
			message: string;
		}>();
		const store = makeStore({ install: () => install.promise });
		store.setTarget(serverTarget);
		await store.submitSearch();
		const run = store.installServerItems({
			items: [{ name: 'm', file_name: 'm.jar', download_url: 'u' }],
			root: { platform: 'modrinth', id: 'm', title: 'm', version: '1', depCount: 0 }
		});
		assert.equal(store.installing, true);
		await store.selectResult(result('other'));
		assert.equal(store.selectedResult, null, 'selection must not change mid-install');
		install.resolve({ success: true, installed: ['m.jar'], errors: [], message: 'ok' });
		await run;
	});

	test('version lookup failures land in a dedicated error state', async () => {
		const store = makeStore({
			versions: async () => {
				throw new Error('versions outage');
			}
		});
		store.setTarget(serverTarget);
		await store.submitSearch();
		await store.selectResult(store.results[0]);
		assert.match(store.versionsError, /versions outage/);
		assert.equal(store.loadingVersions, false);
	});
});

describe('mods browser store — install region', () => {
	test('per-item outcomes distinguish installed from failed files (E10)', async () => {
		const store = makeStore({
			install: async () => ({
				success: true,
				installed: ['root.jar'],
				errors: [],
				message: 'Installed'
			})
		});
		store.setTarget(serverTarget);
		await store.submitSearch();
		await store.installServerItems({
			items: [
				{ name: 'Root', file_name: 'root.jar', download_url: 'u1' },
				{ name: 'Dep', file_name: 'dep.jar', download_url: 'u2' }
			],
			root: { platform: 'modrinth', id: 'root', title: 'Root', version: '1', depCount: 1 }
		});
		assert.deepEqual(
			store.installOutcomes.map((row) => [row.label, row.tone]),
			[
				['Root', 'green'],
				['Dep', 'red']
			]
		);
		assert.match(store.installOutcomes[1].detail ?? '', /Not installed/);
	});

	test('unresolved dependencies are appended and never silently dropped (E9)', async () => {
		const store = await openWithResults();
		await store.installServerItems({
			items: [{ name: 'Root', file_name: 'root.jar', download_url: 'u' }],
			root: { platform: 'modrinth', id: 'root', title: 'Root', version: '1', depCount: 1 },
			unresolvedCount: 1
		});
		store.appendOutcomes([
			{
				key: 'dep:flaky',
				label: 'Flaky API',
				tone: 'yellow',
				detail: 'Unresolved: provider outage'
			}
		]);
		const skipped = store.installOutcomes.find((row) => row.key === 'dep:flaky');
		assert.ok(skipped, 'the unresolved dependency must be visible');
		assert.equal(skipped.tone, 'yellow');
	});

	test('a successful install appends exactly one session entry', async () => {
		const store = await openWithResults();
		await store.installServerItems({
			items: [{ name: 'Root', file_name: 'root.jar', download_url: 'u' }],
			root: { platform: 'modrinth', id: 'root', title: 'Root', version: '2.0', depCount: 2 }
		});
		assert.equal(store.sessionCount, 1);
		assert.equal(store.sessionInstalled[0].key, 'modrinth:root');
		assert.equal(store.sessionInstalled[0].version, '2.0');
	});

	test('closing the dialog mid-install resets installing so reopen is not stuck (E7)', async () => {
		const install = deferred<{
			success: boolean;
			installed: string[];
			errors: string[];
			message: string;
		}>();
		const store = makeStore({ install: () => install.promise });
		store.setTarget(serverTarget);
		await store.submitSearch();
		await store.selectResult(store.results[0]);
		const run = store.installServerItems({
			items: [{ name: 'm', file_name: 'm.jar', download_url: 'u' }],
			root: { platform: 'modrinth', id: 'm', title: 'm', version: '1', depCount: 0 }
		});
		store.closeDialog();
		assert.equal(store.installing, false, 'CLOSE must reset the install region');
		assert.equal(store.selectedResult, null);
		install.resolve({ success: true, installed: ['m.jar'], errors: [], message: 'ok' });
		await run;
		assert.equal(store.installing, false);
	});

	test('closing keeps the query so a reopen re-runs it intentionally (E12)', async () => {
		const store = await openWithResults();
		store.query = 'sodium';
		store.closeDialog();
		assert.equal(store.query, 'sodium');
		assert.equal(store.results.length, 1, 'results stay until the next search');
	});
});

describe('mods browser store — target identity', () => {
	test('TARGET_CHANGED resets everything and invalidates in-flight responses (E22)', async () => {
		const pending = deferred<{ results: ReturnType<typeof result>[] }>();
		const store = makeStore({ search: () => pending.promise });
		store.setTarget(serverTarget);
		const run = store.submitSearch();
		store.setTarget({ kind: 'wizard', id: '', loader: 'forge', mcVersion: '1.20.1' });
		pending.resolve({ results: [result('old-server')] });
		await run;
		assert.equal(store.results.length, 0, 'the stale target response must be dropped');
		assert.equal(store.target.loader, 'forge');
		assert.equal(store.query, '');
		assert.equal(store.sessionCount, 0);
	});
});
