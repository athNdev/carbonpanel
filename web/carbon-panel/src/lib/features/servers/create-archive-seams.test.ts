// SPDX-License-Identifier: MPL-2.0
// Seam-level guards for the operator flows. These assert the wiring in the
// actual route/container source, complementing the pure and rendered tests.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const read = (relative: string) =>
	readFileSync(resolve(import.meta.dirname, '../../..', relative), 'utf8');

const wizard = read('routes/servers/new/+page.svelte');
const detail = read('routes/servers/[id]/+page.svelte');
const container = read('lib/features/servers/ServerArchiveExport.svelte');

test('the wizard submits the completed upload with the stable submission id', () => {
	assert.ok(
		wizard.includes("archiveUploadId: archiveMode ? archiveImportState.uploadId : ''"),
		'create must carry the completed archive upload'
	);
	assert.ok(
		wizard.includes('createRequest.requestId = archiveImportState.requestId'),
		'archive retries must reuse the same request id'
	);
	assert.ok(
		wizard.includes('previewServerArchive(request, ARCHIVE_RPC_OPTIONS)'),
		'preview must use the extended archive deadline'
	);
	assert.ok(
		wizard.includes('const key = `${uploadId}:${nodeKey}`'),
		'preview responses must be fenced by upload and destination'
	);
});

test('the wizard never stops a live server in the archive flow', () => {
	assert.ok(!wizard.includes('stopServer('), 'the create wizard must not stop any server');
	assert.ok(
		wizard.includes('No running server is stopped by this flow.'),
		'the UI must state that no live server is stopped'
	);
});

test('the operator export control is mounted on server detail', () => {
	assert.ok(detail.includes('ServerArchiveExport'), 'server detail must expose the export control');
	assert.ok(
		container.includes('triggerStreamDownload(sessionId, filename)'),
		'download must reuse the authorized stream transport'
	);
	assert.ok(
		container.includes('consistency: STOPPED_CONSISTENCY'),
		'export must request stopped-server consistency'
	);
	assert.ok(
		container.includes('ARCHIVE_CREDENTIAL_DISCLOSURE'),
		'the disclosure must be passed into the export panel'
	);
	assert.ok(
		!container.includes('stopServer('),
		'export must refuse a live server rather than stopping it'
	);
});
