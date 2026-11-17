// SPDX-License-Identifier: MPL-2.0
// Regression coverage for the extracted Studio dependency reconciliation
// workflow: outgoing requests must stay identical, required-only filtering,
// already-installed skips, cycle and depth guards, and best-effort failures.
import { describe, test } from 'node:test';
import assert from 'node:assert/strict';

import {
	installRequiredDependencies,
	type ApiFetch,
	type DependencyInstallInput,
	type InstalledPackMod,
	type VersionDependency
} from './dependencies';

interface RecordedRequest {
	url: string;
	init?: RequestInit;
}

function transport(handler: (url: string, init?: RequestInit) => Response) {
	const requests: RecordedRequest[] = [];
	const apiFetch: ApiFetch = (input, init) => {
		const url = typeof input === 'string' ? input : String(input);
		requests.push({ url, init });
		return handler(url, init);
	};
	return { apiFetch, requests };
}

function versionsPayload(versions: unknown[]) {
	return new Response(JSON.stringify({ versions }), { status: 200 });
}

function fixture(overrides: Partial<DependencyInstallInput> = {}) {
	const input: DependencyInstallInput = {
		deps: [],
		platform: 'modrinth',
		id: 'root-mod',
		side: 'both',
		packId: 'pack-1',
		modLoader: 'Fabric',
		mcVersion: '1.20.1',
		existingMods: [] as InstalledPackMod[],
		...overrides
	};
	return input;
}

function singleVersion(overrides: Record<string, unknown> = {}): Record<string, unknown> {
	return {
		id: 'v1',
		name: 'Fabric API',
		file_name: 'fabric-api-0.1.jar',
		download_url: 'https://dl/fabric-api-0.1.jar',
		file_size: 42,
		dependencies: [],
		...overrides
	};
}

const VERSIONS_URL = '/api/v1/servers/none/mods/fapi/versions?platform=modrinth&loader=fabric&mc_version=1.20.1';

describe('studio dependency reconciliation', () => {
	test('installs required dependencies with exact outgoing requests', async () => {
		const deps: VersionDependency[] = [
			{ project_id: 'fapi', dependency_type: 'required' },
			{ project_id: 'cloth-config', dependency_type: 'optional' },
			{ dependency_type: 'required' }
		];
		const { apiFetch, requests } = transport((url) => {
			if (url.includes('/versions?')) return versionsPayload([singleVersion()]);
			return new Response('{}', { status: 200 });
		});

		const added = await installRequiredDependencies(fixture({ deps }), apiFetch);

		assert.equal(added, 1);
		assert.equal(requests.length, 2);
		assert.equal(requests[0].url, VERSIONS_URL);
		assert.equal(requests[1].url, '/api/v1/packwiz/packs/pack-1/mods');
		assert.equal(requests[1].init?.method, 'POST');
		assert.deepEqual(requests[1].init?.headers, { 'Content-Type': 'application/json' });
		assert.equal(
			requests[1].init?.body,
			'{"slug":"fapi","name":"Fabric API","file_name":"fabric-api-0.1.jar","side":"both","platform":"modrinth","project_id":"fapi","version_id":"v1","download_url":"https://dl/fabric-api-0.1.jar","file_size":42,"pinned":false}'
		);
	});

	test('missing dependency_type defaults to required', async () => {
		const deps: VersionDependency[] = [{ project_id: 'fapi' }];
		const { apiFetch, requests } = transport((url) => {
			if (url.includes('/versions?')) return versionsPayload([singleVersion()]);
			return new Response('{}', { status: 200 });
		});

		const added = await installRequiredDependencies(fixture({ deps }), apiFetch);

		assert.equal(added, 1);
		assert.equal(requests.length, 2);
		assert.equal(requests[0].url, VERSIONS_URL);
	});

	test('does not reinstall mods already in the pack', async () => {
		const deps: VersionDependency[] = [
			{ project_id: 'fapi', dependency_type: 'required' },
			{ project_id: 'jei', dependency_type: 'required' }
		];
		const existing: InstalledPackMod[] = [
			{ slug: 'fabric-api', project_id: 'fapi' },
			{ slug: 'jei' }
		];
		const { apiFetch, requests } = transport(() => {
			assert.fail('no request may run for installed mods');
			return new Response('{}', { status: 500 });
		});

		const added = await installRequiredDependencies(
			fixture({ deps, existingMods: existing }),
			apiFetch
		);

		assert.equal(added, 0);
		assert.equal(requests.length, 0);
	});

	test('case-insensitive slug collisions skip after the version lookup', async () => {
		const deps: VersionDependency[] = [{ project_id: 'fapi', dependency_type: 'required' }];
		const existing: InstalledPackMod[] = [{ slug: 'Fapi', project_id: 'other' }];
		const { apiFetch, requests } = transport(() => versionsPayload([singleVersion()]));

		const added = await installRequiredDependencies(
			fixture({ deps, existingMods: existing }),
			apiFetch
		);

		assert.equal(added, 0);
		assert.equal(requests.length, 1);
		assert.match(requests[0].url, /\/versions\?/);
	});

	test('seeds visited keys with the added mod platform as fallback', async () => {
		const deps: VersionDependency[] = [{ project_id: 'shared', dependency_type: 'required' }];
		const existing: InstalledPackMod[] = [{ slug: 'shared-mod', project_id: 'shared' }];
		const { apiFetch, requests } = transport(() => {
			assert.fail('an existing mod without platform must still be seeded');
			return new Response('{}', { status: 500 });
		});

		const added = await installRequiredDependencies(
			fixture({ deps, existingMods: existing }),
			apiFetch
		);

		assert.equal(added, 0);
		assert.equal(requests.length, 0);
	});

	test('never reinstalls the added mod itself', async () => {
		const deps: VersionDependency[] = [{ project_id: 'root-mod', dependency_type: 'required' }];
		const { apiFetch, requests } = transport(() => {
			assert.fail('the added mod is visited before the walk');
			return new Response('{}', { status: 500 });
		});

		const added = await installRequiredDependencies(fixture({ deps }), apiFetch);

		assert.equal(added, 0);
		assert.equal(requests.length, 0);
	});

	test('guards cycles through the visited set', async () => {
		const deps: VersionDependency[] = [{ project_id: 'a', dependency_type: 'required' }];
		const { apiFetch, requests } = transport((url) => {
			if (url.includes('/mods/a/')) {
				return versionsPayload([singleVersion({ dependencies: [{ project_id: 'b' }] })]);
			}
			return versionsPayload([singleVersion({ dependencies: [{ project_id: 'a' }] })]);
		});

		const added = await installRequiredDependencies(fixture({ deps }), apiFetch);

		assert.equal(added, 2);
		assert.equal(requests.length, 4);
		assert.deepEqual(
			requests.filter((r) => r.url.includes('/versions?')).map((r) => r.url),
			[
				'/api/v1/servers/none/mods/a/versions?platform=modrinth&loader=fabric&mc_version=1.20.1',
				'/api/v1/servers/none/mods/b/versions?platform=modrinth&loader=fabric&mc_version=1.20.1'
			]
		);
	});

	test('bounds the walk at three dependency levels', async () => {
		const deps: VersionDependency[] = [{ project_id: 'd1', dependency_type: 'required' }];
		const { apiFetch, requests } = transport((url) => {
			const id = url.split('/mods/')[1]?.split('/')[0] ?? '';
			const level = Number(id.replace('d', '')) || 0;
			return versionsPayload([
				singleVersion({
					dependencies: [{ project_id: `d${level + 1}`, dependency_type: 'required' }]
				})
			]);
		});

		const added = await installRequiredDependencies(fixture({ deps }), apiFetch);

		assert.equal(added, 4);
		const installed = requests.filter((r) => r.url === '/api/v1/packwiz/packs/pack-1/mods');
		assert.equal(installed.length, 4);
		assert.ok(!requests.some((r) => r.url.includes('/mods/d5/')));
	});

	test('skips dependencies whose version lookup fails and keeps siblings', async () => {
		const deps: VersionDependency[] = [
			{ project_id: 'broken', dependency_type: 'required' },
			{ project_id: 'jei', dependency_type: 'required' }
		];
		const { apiFetch, requests } = transport((url) => {
			if (url.includes('/mods/broken/')) return new Response('boom', { status: 404 });
			if (url.includes('/versions?')) return versionsPayload([singleVersion()]);
			return new Response('{}', { status: 200 });
		});

		const added = await installRequiredDependencies(fixture({ deps }), apiFetch);

		assert.equal(added, 1);
		assert.equal(requests.length, 3);
		assert.match(requests[2].init?.body as string, /"slug":"jei"/);
	});

	test('skips dependencies with no compatible versions', async () => {
		const deps: VersionDependency[] = [{ project_id: 'fapi', dependency_type: 'required' }];
		const { apiFetch, requests } = transport(() => versionsPayload([]));

		const added = await installRequiredDependencies(fixture({ deps }), apiFetch);

		assert.equal(added, 0);
		assert.equal(requests.length, 1);
	});

	test('continues past failed install calls', async () => {
		const deps: VersionDependency[] = [
			{ project_id: 'rejected', dependency_type: 'required' },
			{ project_id: 'jei', dependency_type: 'required' }
		];
		const { apiFetch, requests } = transport((url, init) => {
			if (url.includes('/versions?')) return versionsPayload([singleVersion()]);
			if (url.includes('/packwiz/packs/') && String(init?.body || '').includes('rejected'))
				return new Response('nope', { status: 500 });
			return new Response('{}', { status: 200 });
		});

		const added = await installRequiredDependencies(fixture({ deps }), apiFetch);

		assert.equal(added, 1);
		assert.equal(requests.length, 4);
	});
});
