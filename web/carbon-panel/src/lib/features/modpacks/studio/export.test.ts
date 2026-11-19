// SPDX-License-Identifier: MPL-2.0
// Request-level regression coverage for the extracted Studio export-selection
// workflow: exact export URL/query, blob handoff and filename, CurseForge 422
// dropped-item interpretation, error text fallback and the option helpers.
import { describe, test } from 'node:test';
import assert from 'node:assert/strict';

import {
	DEFAULT_EXPORT_OPTIONS,
	EXPORT_FORMATS,
	buildExportQuery,
	downloadPackExport,
	findCurseForgeDroppedMods,
	formatExportLabel,
	getExportFilename,
	requestPackExport,
	type ApiFetch,
	type PackExportOptions
} from './export';

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

const opts: PackExportOptions = {
	includeClient: false,
	optionalMode: 'optional',
	overrides: 'configs-only',
	summary: 'Hello'
};

describe('studio export selection', () => {
	test('every launcher format is offered with a helper hint', () => {
		const ids = EXPORT_FORMATS.map((f) => f.id);
		for (const expected of ['mrpack', 'curseforge', 'mmc', 'packwiz']) {
			assert.ok(ids.includes(expected as never), `Export menu must offer ${expected}`);
		}
	});

	test('default export keeps client mods and all overrides', () => {
		assert.equal(DEFAULT_EXPORT_OPTIONS.includeClient, true);
		assert.equal(DEFAULT_EXPORT_OPTIONS.optionalMode, 'keep');
		assert.equal(DEFAULT_EXPORT_OPTIONS.overrides, 'all');
	});

	test('export query encodes per-launcher options in the historical order', () => {
		assert.equal(
			buildExportQuery(opts),
			'include_client=false&optional_mode=optional&overrides=configs-only&summary=Hello'
		);
		assert.equal(
			buildExportQuery(DEFAULT_EXPORT_OPTIONS),
			'include_client=true&optional_mode=keep&overrides=all'
		);
		assert.equal(
			buildExportQuery({ ...DEFAULT_EXPORT_OPTIONS, summary: '   ' }),
			'include_client=true&optional_mode=keep&overrides=all'
		);
	});

	test('export filenames and labels match launcher expectations', () => {
		assert.equal(getExportFilename('Odyssey', 'mrpack'), 'Odyssey.mrpack');
		assert.equal(getExportFilename('Odyssey', 'packwiz'), 'Odyssey.packwiz.zip');
		assert.equal(getExportFilename('Odyssey', 'mmc'), 'Odyssey-mmc.zip');
		assert.equal(getExportFilename('Odyssey', 'curseforge'), 'Odyssey.zip');
		assert.equal(getExportFilename('  ', 'mrpack'), 'modpack.mrpack');
		assert.equal(formatExportLabel('mmc'), 'instance.zip');
	});

	test('CurseForge data-loss preview flags non-CF mods', () => {
		const dropped = findCurseForgeDroppedMods([
			{ name: 'Sodium', platform: 'modrinth', project_id: 'AANobbMI', version_id: 'v1' },
			{ name: 'JEI', platform: 'curseforge', project_id: '238222', version_id: '4635832' },
			{ name: 'Direct', platform: 'url', project_id: 'x', version_id: 'y' }
		]);
		assert.deepEqual(dropped, ['Sodium', 'Direct']);
	});

	test('requests the exact export URL and hands the blob to the download seam', async () => {
		const blob = new Blob(['archive'], { type: 'application/zip' });
		const { apiFetch, requests } = transport(() => new Response(blob));
		const saved: { name: string; size: number }[] = [];
		const result = await downloadPackExport(
			'pack-1',
			'Odyssey',
			'mrpack',
			opts,
			apiFetch,
			(b, name) => {
				saved.push({ name, size: b.size });
			}
		);
		assert.deepEqual(result, { ok: true });
		assert.deepEqual(requests, [
			{
				url: '/api/v1/packwiz/packs/pack-1/export/mrpack?include_client=false&optional_mode=optional&overrides=configs-only&summary=Hello',
				init: undefined
			}
		]);
		assert.deepEqual(saved, [{ name: 'Odyssey.mrpack', size: blob.size }]);
	});

	test('interprets a CurseForge 422 dropped-item response', async () => {
		const { apiFetch } = transport(() =>
			jsonResponse({ error: 'blocked', dropped: ['Sodium', 'Direct'] }, 422)
		);
		const result = await requestPackExport(
			'pack-1',
			'curseforge',
			DEFAULT_EXPORT_OPTIONS,
			apiFetch
		);
		assert.deepEqual(result, { ok: false, dropped: ['Sodium', 'Direct'], message: 'blocked' });
	});

	test('builds the default CurseForge dropped message when the body omits an error', async () => {
		const { apiFetch } = transport(() => jsonResponse({ dropped: ['Sodium'] }, 422));
		const result = await requestPackExport(
			'pack-1',
			'curseforge',
			DEFAULT_EXPORT_OPTIONS,
			apiFetch
		);
		assert.deepEqual(result, {
			ok: false,
			dropped: ['Sodium'],
			message: 'CurseForge export blocked: 1 item(s) have no CurseForge project/file ID.'
		});
	});

	test('falls back to a generic message when a 422 body is not JSON', async () => {
		const { apiFetch } = transport(() => new Response('not json', { status: 422 }));
		const result = await requestPackExport(
			'pack-1',
			'curseforge',
			DEFAULT_EXPORT_OPTIONS,
			apiFetch
		);
		assert.deepEqual(result, { ok: false, message: 'Export failed: HTTP 422' });
	});

	test('uses response text as a generic export error when present', async () => {
		const { apiFetch } = transport(() => new Response('unauthorized', { status: 401 }));
		const result = await requestPackExport('pack-1', 'mrpack', DEFAULT_EXPORT_OPTIONS, apiFetch);
		assert.deepEqual(result, { ok: false, message: 'unauthorized' });
	});

	test('falls back to the status text when a generic error body is empty', async () => {
		const { apiFetch } = transport(() => new Response('', { status: 500 }));
		const result = await requestPackExport('pack-1', 'mrpack', DEFAULT_EXPORT_OPTIONS, apiFetch);
		assert.deepEqual(result, { ok: false, message: 'Export failed: HTTP 500' });
	});
});
