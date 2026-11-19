// SPDX-License-Identifier: MPL-2.0
// Regression coverage for the extracted Studio mod management/catalog workflow:
// side rotation, exact PATCH/DELETE/POST/URL payloads, the CurseForge id-vs-slug
// lookup, the json-before-ok search error mapping and the search/mod mapping.
import { describe, test } from 'node:test';
import assert from 'node:assert/strict';

import {
	addModToPack,
	addUrlMod,
	batchModAction,
	findModVersions,
	modFromSearch,
	nextModSide,
	patchMod,
	removeModFromPack,
	searchOnlineMods,
	type ApiFetch,
	type ModItem,
	type SearchModResult
} from './mod-actions';

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

function item(overrides: Partial<SearchModResult> = {}): SearchModResult {
	return {
		id: 'proj-1',
		slug: 'qa-mod',
		title: 'QA Mod',
		description: '',
		icon_url: '',
		author: '',
		downloads: 0,
		categories: [],
		platform: 'modrinth',
		...overrides
	};
}

describe('studio mod management', () => {
	test('rotates sides in the historical order with a safe fallback', () => {
		assert.equal(nextModSide('both'), 'server');
		assert.equal(nextModSide('server'), 'client');
		assert.equal(nextModSide('client'), 'both');
		assert.equal(nextModSide('unknown'), 'both');
	});

	test('patches side and pinned with the exact body', async () => {
		const { apiFetch, requests } = transport(() => jsonResponse({}));
		await patchMod('pack-1', 'qa-mod', 'server', true, apiFetch);
		assert.deepEqual(requests, [
			{
				url: '/api/v1/packwiz/packs/pack-1/mods/qa-mod',
				init: {
					method: 'PATCH',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ side: 'server', pinned: true })
				}
			}
		]);
	});

	test('surfaces the status when patching fails', async () => {
		const { apiFetch } = transport(() => jsonResponse({}, 409));
		await assert.rejects(patchMod('pack-1', 'm', 'both', false, apiFetch), /HTTP 409/);
	});

	test('deletes a mod with no body', async () => {
		const { apiFetch, requests } = transport(() => jsonResponse({}));
		await removeModFromPack('pack-1', 'qa-mod', apiFetch);
		assert.deepEqual(requests, [
			{ url: '/api/v1/packwiz/packs/pack-1/mods/qa-mod', init: { method: 'DELETE' } }
		]);
	});

	test('posts a batch action with slugs and optional side', async () => {
		const { apiFetch, requests } = transport(() => jsonResponse({}));
		await batchModAction('pack-1', 'set_side', ['a', 'b'], 'client', apiFetch);
		assert.deepEqual(requests, [
			{
				url: '/api/v1/packwiz/packs/pack-1/mods/batch',
				init: {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ action: 'set_side', slugs: ['a', 'b'], side: 'client' })
				}
			}
		]);
	});

	test('searches with trimmed query, lowercased loader and url-encoded params', async () => {
		const { apiFetch, requests } = transport(() => jsonResponse({ results: [item()] }));
		const result = await searchOnlineMods('  qa  ', 'modrinth', 'FABRIC', '1.20.1', apiFetch);
		assert.deepEqual(result, { results: [item()], error: '' });
		assert.deepEqual(requests, [
			{
				url: '/api/v1/servers/none/mods/search?query=qa&platform=modrinth&loader=fabric&mc_version=1.20.1',
				init: undefined
			}
		]);
	});

	test('maps a search error response to its error message', async () => {
		const { apiFetch } = transport(() => jsonResponse({ error: 'rate limited' }, 429));
		assert.deepEqual(await searchOnlineMods('qa', 'modrinth', 'fabric', '1.20.1', apiFetch), {
			results: [],
			error: 'rate limited'
		});
	});

	test('uses a default search error when the body has none', async () => {
		const { apiFetch } = transport(() => jsonResponse({}, 500));
		assert.deepEqual(await searchOnlineMods('qa', 'modrinth', 'fabric', '1.20.1', apiFetch), {
			results: [],
			error: 'Search request failed'
		});
	});

	test('looks up CurseForge versions by id, other platforms by slug', async () => {
		const { apiFetch, requests } = transport(() => jsonResponse({ versions: [{ id: 'v1' }] }));
		await findModVersions(item({ platform: 'curseforge' }), 'FABRIC', '1.20.1', apiFetch);
		await findModVersions(item({ slug: 'qa-mod' }), 'fabric', '1.20.1', apiFetch);
		assert.deepEqual(
			requests.map((r) => r.url),
			[
				'/api/v1/servers/none/mods/proj-1/versions?platform=curseforge&loader=fabric&mc_version=1.20.1',
				'/api/v1/servers/none/mods/qa-mod/versions?platform=modrinth&loader=fabric&mc_version=1.20.1'
			]
		);
	});

	test('throws when the version lookup fails', async () => {
		const { apiFetch } = transport(() => jsonResponse({}, 404));
		await assert.rejects(findModVersions(item(), 'fabric', '1.20.1', apiFetch), {
			message: 'No compatible versions found'
		});
	});

	test('maps a chosen version into a pack mod with legacy field fallbacks', () => {
		const mapped = modFromSearch(
			item(),
			{
				id: 'v1',
				fileName: 'legacy.jar',
				downloadUrl: 'https://example.test/legacy.jar',
				fileSize: 9
			},
			'server'
		);
		assert.deepEqual(mapped, {
			slug: 'qa-mod',
			name: 'QA Mod',
			file_name: 'legacy.jar',
			side: 'server',
			platform: 'modrinth',
			project_id: 'proj-1',
			version_id: 'v1',
			download_url: 'https://example.test/legacy.jar',
			file_size: 9,
			pinned: false
		} satisfies ModItem);
	});

	test('posts a mapped mod with the exact body', async () => {
		const { apiFetch, requests } = transport(() => jsonResponse({}));
		const mod = modFromSearch(item(), { id: 'v1' }, 'both');
		await addModToPack('pack-1', mod, apiFetch);
		assert.deepEqual(requests, [
			{
				url: '/api/v1/packwiz/packs/pack-1/mods',
				init: {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({
						slug: 'qa-mod',
						name: 'QA Mod',
						file_name: 'mod.jar',
						side: 'both',
						platform: 'modrinth',
						project_id: 'proj-1',
						version_id: 'v1',
						download_url: '',
						pinned: false
					})
				}
			}
		]);
	});

	test('adds a URL mod with trimmed fields and returns the created name', async () => {
		const { apiFetch, requests } = transport(() => jsonResponse({ name: 'Custom' }));
		const result = await addUrlMod(
			'pack-1',
			{
				name: '  Custom  ',
				fileName: '  custom.jar  ',
				downloadUrl: '  https://example.test/custom.jar  ',
				side: 'client',
				pinned: true
			},
			apiFetch
		);
		assert.deepEqual(result, { name: 'Custom' });
		assert.deepEqual(requests, [
			{
				url: '/api/v1/packwiz/packs/pack-1/mods/url',
				init: {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({
						name: 'Custom',
						file_name: 'custom.jar',
						download_url: 'https://example.test/custom.jar',
						side: 'client',
						pinned: true
					})
				}
			}
		]);
	});

	test('uses response text as the URL-mod error when present', async () => {
		const { apiFetch } = transport(() => new Response('bad url', { status: 400 }));
		await assert.rejects(
			addUrlMod(
				'pack-1',
				{ name: '', fileName: '', downloadUrl: 'x', side: 'both', pinned: false },
				apiFetch
			),
			{ message: 'bad url' }
		);
	});
});
