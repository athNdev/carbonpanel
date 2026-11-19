// SPDX-License-Identifier: MPL-2.0
// Regression coverage for the extracted Studio maintenance workflows: the
// update map is keyed by slug with the available count derived from
// `update_available`, missing update arrays read as empty, and the refresh
// request and failure statuses stay unchanged.
import { describe, test } from 'node:test';
import assert from 'node:assert/strict';

import {
	fetchPackUpdates,
	refreshPack,
	type ApiFetch,
	type ModUpdateInfo
} from './pack-maintenance';

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

function update(overrides: Partial<ModUpdateInfo> & { slug: string }): ModUpdateInfo {
	return {
		name: overrides.slug,
		platform: 'modrinth',
		current_version_id: 'v1',
		latest_version_id: 'v2',
		latest_file_name: 'mod.jar',
		latest_download_url: 'https://example.test/mod.jar',
		update_available: false,
		pinned: false,
		...overrides
	};
}

describe('studio pack maintenance', () => {
	test('keys updates by slug and counts only available updates', async () => {
		const stale = update({ slug: 'stale', update_available: true });
		const fresh = update({ slug: 'fresh', update_available: false });
		const { apiFetch, requests } = transport(() => jsonResponse({ updates: [stale, fresh] }));
		const result = await fetchPackUpdates('pack-1', apiFetch);
		assert.deepEqual(result.updates, { stale, fresh });
		assert.equal(result.availableCount, 1);
		assert.deepEqual(requests, [{ url: '/api/v1/packwiz/packs/pack-1/updates', init: undefined }]);
	});

	test('treats a missing updates array as empty', async () => {
		const { apiFetch } = transport(() => jsonResponse({}));
		assert.deepEqual(await fetchPackUpdates('pack-1', apiFetch), {
			updates: {},
			availableCount: 0
		});
	});

	test('surfaces the status when the update check fails', async () => {
		const { apiFetch } = transport(() => jsonResponse({}, 500));
		await assert.rejects(fetchPackUpdates('pack-1', apiFetch), /HTTP 500/);
	});

	test('posts a pack refresh with no body', async () => {
		const { apiFetch, requests } = transport(() => jsonResponse({}));
		await refreshPack('pack-1', apiFetch);
		assert.deepEqual(requests, [
			{ url: '/api/v1/packwiz/packs/pack-1/refresh', init: { method: 'POST' } }
		]);
	});

	test('surfaces the status when refresh fails', async () => {
		const { apiFetch } = transport(() => jsonResponse({}, 503));
		await assert.rejects(refreshPack('pack-1', apiFetch), /HTTP 503/);
	});
});
