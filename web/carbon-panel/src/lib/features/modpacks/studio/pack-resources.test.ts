// SPDX-License-Identifier: MPL-2.0
// Regression coverage for the extracted Studio override-file and raw-TOML
// workflows: outgoing URLs/methods/bodies stay byte-identical, the legacy
// `files || []` and text-only-on-ok semantics are preserved, and failure
// statuses still surface `HTTP <status>`.
import { describe, test } from 'node:test';
import assert from 'node:assert/strict';

import {
	deletePackFile,
	fetchPackFiles,
	fetchPackToml,
	installDatapack,
	savePackFile,
	type ApiFetch
} from './pack-resources';

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

describe('studio pack resources', () => {
	test('fetches override files from the /packs/ route', async () => {
		const files = [{ path: 'config/a.json', size: 12, mod_time: 't', category: 'config' }];
		const { apiFetch, requests } = transport(() => jsonResponse({ files }));
		assert.deepEqual(await fetchPackFiles('pack-1', apiFetch), files);
		assert.deepEqual(requests, [{ url: '/api/v1/packwiz/packs/pack-1/files', init: undefined }]);
	});

	test('treats a missing files array as empty', async () => {
		const { apiFetch } = transport(() => jsonResponse({}));
		assert.deepEqual(await fetchPackFiles('pack-1', apiFetch), []);
	});

	test('surfaces the status when fetching files fails', async () => {
		const { apiFetch } = transport(() => jsonResponse({}, 502));
		await assert.rejects(fetchPackFiles('pack-1', apiFetch), /HTTP 502/);
	});

	test('posts a trimmed override path and content with the exact body', async () => {
		const { apiFetch, requests } = transport(() => jsonResponse({}));
		await savePackFile('pack-1', 'config/settings.json', '{"a":1}', apiFetch);
		assert.deepEqual(requests, [
			{
				url: '/api/v1/packwiz/packs/pack-1/files',
				init: {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ path: 'config/settings.json', content: '{"a":1}' })
				}
			}
		]);
	});

	test('encodes the file path when deleting an override', async () => {
		const { apiFetch, requests } = transport(() => jsonResponse({}));
		await deletePackFile('pack 1', 'config/a b.json', apiFetch);
		assert.deepEqual(requests, [
			{
				url: '/api/v1/packwiz/packs/pack 1/files?path=config%2Fa%20b.json',
				init: { method: 'DELETE' }
			}
		]);
	});

	test('reads raw pack.toml text from the non-/packs/ route', async () => {
		const { apiFetch, requests } = transport(() => new Response('name="A"'));
		assert.equal(await fetchPackToml('pack-1', apiFetch), 'name="A"');
		assert.deepEqual(requests, [{ url: '/api/v1/packwiz/pack-1/pack.toml', init: undefined }]);
	});

	test('returns null for raw pack.toml on a failed response', async () => {
		const { apiFetch } = transport(() => new Response('', { status: 404 }));
		assert.equal(await fetchPackToml('pack-1', apiFetch), null);
	});

	test('returns null for raw pack.toml when the transport rejects', async () => {
		const original = console.error;
		console.error = () => {};
		try {
			const apiFetch: ApiFetch = () => Promise.reject(new Error('offline'));
			assert.equal(await fetchPackToml('pack-1', apiFetch), null);
		} finally {
			console.error = original;
		}
	});

	test('installs an indexed datapack with the exact payload', async () => {
		const { apiFetch, requests } = transport(() => jsonResponse({}));
		await installDatapack('pack-1', 'dp-1', '1.21.1', 'modrinth', apiFetch);
		assert.deepEqual(requests, [
			{
				url: '/api/v1/packwiz/packs/pack-1/datapacks',
				init: {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({
						datapack_id: 'dp-1',
						game_version: '1.21.1',
						indexer: 'modrinth'
					})
				}
			}
		]);
	});

	test('uses response text as the datapack install error when present', async () => {
		const { apiFetch } = transport(() => new Response('already installed', { status: 409 }));
		await assert.rejects(installDatapack('pack-1', 'dp-1', '1.21.1', 'modrinth', apiFetch), {
			message: 'already installed'
		});
	});
});
