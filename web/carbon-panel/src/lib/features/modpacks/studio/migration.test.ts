// SPDX-License-Identifier: MPL-2.0
// Regression coverage for the extracted Studio version-migration workflow:
// outgoing requests (URL, method, headers and body) must stay byte-identical
// for simulate and apply, the report is returned unchanged, failures surface
// the HTTP status, and the route's error-message fallback is preserved.
import { describe, test } from 'node:test';
import assert from 'node:assert/strict';

import {
	MIGRATION_FAILURE_MESSAGE,
	migrationErrorMessage,
	runMigration,
	type ApiFetch,
	type MigrationReport,
	type MigrationRequest
} from './migration';

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

function report(overrides: Partial<MigrationReport> = {}): MigrationReport {
	return {
		pack_id: 'pack-1',
		current_mc: '1.20.1',
		current_loader: 'fabric',
		target_mc: '1.21.1',
		target_loader: 'fabric',
		total_mods: 2,
		compatible_count: 1,
		incompatible_count: 1,
		mods: [
			{
				slug: 'ok-mod',
				name: 'Compatible Mod',
				platform: 'modrinth',
				current_version_id: 'v1',
				target_version_id: 'v2',
				compatible: true
			},
			{
				slug: 'stale-mod',
				name: 'Stale Mod',
				platform: 'curseforge',
				current_version_id: 'v3',
				compatible: false
			}
		],
		...overrides
	};
}

function request(overrides: Partial<MigrationRequest> = {}): MigrationRequest {
	return {
		packId: 'pack-1',
		targetMc: '1.21.1',
		targetLoader: 'fabric',
		...overrides
	};
}

const MIGRATE_URL = '/api/v1/packwiz/packs/pack-1/migrate';

describe('studio version migration', () => {
	test('simulation sends the exact outgoing request and returns the report', async () => {
		const expected = report();
		const { apiFetch, requests } = transport(
			() => new Response(JSON.stringify(expected), { status: 200 })
		);

		const actual = await runMigration(request(), false, apiFetch);

		assert.deepEqual(actual, expected);
		assert.equal(requests.length, 1);
		assert.equal(requests[0].url, MIGRATE_URL);
		assert.equal(requests[0].init?.method, 'POST');
		assert.deepEqual(requests[0].init?.headers, { 'Content-Type': 'application/json' });
		assert.equal(
			requests[0].init?.body,
			'{"target_mc":"1.21.1","target_loader":"fabric","apply":false}'
		);
	});

	test('apply flips only the apply flag in the body', async () => {
		const { apiFetch, requests } = transport(
			() => new Response(JSON.stringify(report()), { status: 200 })
		);

		await runMigration(request(), true, apiFetch);

		assert.equal(requests.length, 1);
		assert.equal(requests[0].url, MIGRATE_URL);
		assert.equal(
			requests[0].init?.body,
			'{"target_mc":"1.21.1","target_loader":"fabric","apply":true}'
		);
	});

	test('preserves loader and target values verbatim in the payload', async () => {
		const { apiFetch, requests } = transport(
			() => new Response(JSON.stringify(report()), { status: 200 })
		);

		await runMigration(request({ targetMc: '1.21.4', targetLoader: 'NeoForge' }), false, apiFetch);

		assert.equal(
			requests[0].init?.body,
			'{"target_mc":"1.21.4","target_loader":"NeoForge","apply":false}'
		);
	});

	test('uses the pack id in the path without touching other characters', async () => {
		const { apiFetch, requests } = transport(
			() => new Response(JSON.stringify(report()), { status: 200 })
		);

		await runMigration(request({ packId: 'pack/with spaces' }), false, apiFetch);

		assert.equal(requests[0].url, '/api/v1/packwiz/packs/pack/with spaces/migrate');
	});

	test('throws with the HTTP status on a failed response', async () => {
		const { apiFetch } = transport(() => new Response('no', { status: 409 }));

		await assert.rejects(runMigration(request(), false, apiFetch), (err: unknown) => {
			assert.ok(err instanceof Error);
			assert.equal(err.message, 'HTTP 409');
			return true;
		});
	});

	test('error message keeps the thrown HTTP status', () => {
		assert.equal(migrationErrorMessage(new Error('HTTP 409')), 'HTTP 409');
	});

	test('error message falls back for non-errors and empty messages', () => {
		assert.equal(migrationErrorMessage('boom'), MIGRATION_FAILURE_MESSAGE);
		assert.equal(migrationErrorMessage(undefined), MIGRATION_FAILURE_MESSAGE);
		assert.equal(migrationErrorMessage(new Error('')), MIGRATION_FAILURE_MESSAGE);
		assert.equal(migrationErrorMessage(null), MIGRATION_FAILURE_MESSAGE);
	});
});
