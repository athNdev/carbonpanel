// SPDX-License-Identifier: MPL-2.0
// Request-level regression coverage for the extracted Studio metadata/content
// workflow: exact GET/PUT urls, methods and bodies, the legacy mods
// normalisation, MC-version change rule and the export content breakdown.
import { describe, test } from 'node:test';
import assert from 'node:assert/strict';

import {
	allPackContentMods,
	fetchStudioPack,
	isMcVersionChanged,
	saveStudioPack,
	summarizePackContent,
	type ApiFetch,
	type StudioPack,
	type StudioPackMod
} from './pack-metadata';

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

function jsonResponse(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}

function pack(overrides: Partial<StudioPack> = {}): StudioPack {
	return {
		id: 'pack-1',
		name: 'Odyssey',
		author: 'Admin',
		version: '1.0.0',
		mc_version: '1.20.1',
		mod_loader: 'fabric',
		loader_version: 'latest',
		updated_at: '2026-01-01T00:00:00Z',
		mods: [],
		...overrides
	};
}

function mod(overrides: Partial<StudioPackMod> = {}): StudioPackMod {
	return {
		slug: 'a-mod',
		name: 'A Mod',
		file_name: 'a.jar',
		side: 'both',
		platform: 'modrinth',
		project_id: 'AANobbMI',
		version_id: 'v1',
		download_url: 'https://example.test/a.jar',
		...overrides
	};
}

describe('studio metadata/content workflow', () => {
	test('GETs the project with no init and normalises a missing mods array', async () => {
		const { apiFetch, requests } = transport(() =>
			jsonResponse({ id: 'pack-1', name: 'Odyssey', mc_version: '1.20.1' })
		);
		const loaded = await fetchStudioPack('pack-1', apiFetch);
		assert.deepEqual(requests, [{ url: '/api/v1/packwiz/packs/pack-1', init: undefined }]);
		assert.deepEqual(loaded, { id: 'pack-1', name: 'Odyssey', mc_version: '1.20.1', mods: [] });
	});

	test('keeps a falsy response body unchanged', async () => {
		const { apiFetch } = transport(() => jsonResponse(null));
		assert.equal(await fetchStudioPack('pack-1', apiFetch), null);
	});

	test('surfaces the status when loading fails', async () => {
		const { apiFetch } = transport(() => jsonResponse({}, 404));
		await assert.rejects(fetchStudioPack('missing', apiFetch), /HTTP 404/);
	});

	test('PUTs the whole pack with the exact body, preserving metadata and content', async () => {
		const { apiFetch, requests } = transport(() => jsonResponse({}));
		const record = pack({ mods: [mod()], datapacks: [mod({ slug: 'a-datapack' })] });
		await saveStudioPack('pack-1', record, apiFetch);
		assert.deepEqual(requests, [
			{
				url: '/api/v1/packwiz/packs/pack-1',
				init: {
					method: 'PUT',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify(record)
				}
			}
		]);
	});

	test('surfaces the status when saving fails', async () => {
		const { apiFetch } = transport(() => jsonResponse({}, 500));
		await assert.rejects(saveStudioPack('pack-1', pack(), apiFetch), /HTTP 500/);
	});

	test('only reports an MC-version change when a baseline exists and differs', () => {
		assert.equal(isMcVersionChanged('', '1.20.1'), false);
		assert.equal(isMcVersionChanged('1.20.1', '1.20.1'), false);
		assert.equal(isMcVersionChanged('1.20.1', '1.21.1'), true);
	});

	test('collects all exported content in the historical order', () => {
		assert.deepEqual(
			allPackContentMods(
				pack({
					mods: [mod({ slug: 'mod' })],
					datapacks: [mod({ slug: 'dp' })],
					resourcepacks: [mod({ slug: 'rp' })],
					shaderpacks: [mod({ slug: 'sp' })]
				})
			).map((m) => m.slug),
			['mod', 'dp', 'rp', 'sp']
		);
		assert.deepEqual(allPackContentMods(pack()), []);
	});

	test('computes the historical content breakdown including numeric CF ids', () => {
		const mods = [
			mod({ slug: 'both', side: 'both', pinned: true }),
			mod({ slug: 'client', side: 'client' }),
			mod({ slug: 'server', side: 'server' }),
			mod({
				slug: 'cf-ok',
				side: 'server',
				platform: 'curseforge',
				project_id: '238222',
				version_id: '4635832'
			}),
			mod({ slug: 'cf-slug', side: 'server', platform: 'curseforge', project_id: 'not-numeric' })
		];
		assert.deepEqual(summarizePackContent(mods, 3), {
			total: 5,
			both: 1,
			client: 1,
			server: 3,
			pinned: 1,
			clientOnly: 1,
			curseforgeReady: 1,
			overrides: 3
		});
	});

	test('tolerates an absent mods list in the breakdown', () => {
		assert.deepEqual(summarizePackContent(undefined as unknown as StudioPackMod[], 0), {
			total: 0,
			both: 0,
			client: 0,
			server: 0,
			pinned: 0,
			clientOnly: 0,
			curseforgeReady: 0,
			overrides: 0
		});
	});
});
