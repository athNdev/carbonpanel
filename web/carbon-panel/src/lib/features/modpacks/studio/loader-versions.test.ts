// SPDX-License-Identifier: MPL-2.0
// Regression coverage for the extracted Studio loader-version fetch: the
// request URL (lowercased loader, game_version query including the empty
// fallback), the current-version precedence rule and the ['latest'] fallback
// on HTTP failure, malformed data or a rejected request.
import { describe, test } from 'node:test';
import assert from 'node:assert/strict';

import { fetchLoaderVersions, type ApiFetch } from './loader-versions';

function transport(handler: (url: string) => Response) {
	const urls: string[] = [];
	const apiFetch: ApiFetch = (input) => {
		const url = typeof input === 'string' ? input : String(input);
		urls.push(url);
		return handler(url);
	};
	return { apiFetch, urls };
}

function jsonResponse(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}

describe('studio loader versions', () => {
	test('skips the request when no loader was supplied', async () => {
		const { apiFetch, urls } = transport(() => jsonResponse({ versions: ['x'] }));
		assert.equal(await fetchLoaderVersions('', '1.21.1', undefined, apiFetch), null);
		assert.deepEqual(urls, []);
	});

	test('builds the lowercased loader URL with the game version', async () => {
		const { apiFetch, urls } = transport(() => jsonResponse({ versions: ['0.16.0'] }));
		assert.deepEqual(await fetchLoaderVersions('Forge', '1.20.1', undefined, apiFetch), ['0.16.0']);
		assert.deepEqual(urls, ['/api/v1/packwiz/loaders/forge/versions?game_version=1.20.1']);
	});

	test('uses an empty game_version query when the Minecraft version is absent', async () => {
		const { apiFetch, urls } = transport(() => jsonResponse({ versions: ['latest'] }));
		await fetchLoaderVersions('fabric', '', undefined, apiFetch);
		assert.deepEqual(urls, ['/api/v1/packwiz/loaders/fabric/versions?game_version=']);
	});

	test('prepends the current version when it is not in the returned list', async () => {
		const { apiFetch } = transport(() => jsonResponse({ versions: ['0.16.0', '0.15.0'] }));
		assert.deepEqual(await fetchLoaderVersions('fabric', '1.21.1', '0.14.0', apiFetch), [
			'0.14.0',
			'0.16.0',
			'0.15.0'
		]);
	});

	test('does not duplicate a current version already present', async () => {
		const { apiFetch } = transport(() => jsonResponse({ versions: ['0.16.0', '0.15.0'] }));
		assert.deepEqual(await fetchLoaderVersions('fabric', '1.21.1', '0.15.0', apiFetch), [
			'0.16.0',
			'0.15.0'
		]);
	});

	test('falls back to latest on an empty version list', async () => {
		const { apiFetch } = transport(() => jsonResponse({ versions: [] }));
		assert.deepEqual(await fetchLoaderVersions('fabric', '1.21.1', undefined, apiFetch), [
			'latest'
		]);
	});

	test('falls back to latest on a non-ok response', async () => {
		const { apiFetch } = transport(() => jsonResponse({ error: 'nope' }, 503));
		assert.deepEqual(await fetchLoaderVersions('fabric', '1.21.1', undefined, apiFetch), [
			'latest'
		]);
	});

	test('falls back to latest when the transport rejects', async () => {
		const original = console.error;
		console.error = () => {};
		try {
			const apiFetch: ApiFetch = () => Promise.reject(new Error('offline'));
			assert.deepEqual(await fetchLoaderVersions('fabric', '1.21.1', undefined, apiFetch), [
				'latest'
			]);
		} finally {
			console.error = original;
		}
	});
});
