// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import {
	PreviewServerArchiveResponseSchema,
	type PreviewServerArchiveResponse
} from '../../proto/carbonpanel/v1/server_pb';
import { createArchiveImport } from './archive-import-session';

function preview(label: string): PreviewServerArchiveResponse {
	return create(PreviewServerArchiveResponseSchema, { mcVersion: label });
}

function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (reason: unknown) => void;
	const promise = new Promise<T>((res, rej) => {
		resolve = res;
		reject = rej;
	});
	return { promise, resolve, reject };
}

test('a completed upload keeps one create request id across refused retries', async () => {
	const session = createArchiveImport({
		previewServerArchive: () => Promise.resolve(preview('1.21.1'))
	});
	session.setUpload('upload-1', 'world.zip');
	const requestId = session.state.requestId;
	assert.ok(requestId, 'an uploaded archive must have a submission id');

	await session.refresh('');
	assert.equal(session.state.requestId, requestId);
	await session.refresh('node-2');
	assert.equal(session.state.requestId, requestId);

	// No succeeded() between attempts: a refusal retained the upload and id.
	assert.equal(session.state.uploadId, 'upload-1');
	assert.equal(session.state.requestId, requestId);

	session.succeeded();
	assert.equal(session.state.requestId, '');
});

test('changing the uploaded archive rotates the submission id', () => {
	const session = createArchiveImport({
		previewServerArchive: () => Promise.resolve(preview('1.21.1'))
	});
	session.setUpload('upload-1', 'a.zip');
	const first = session.state.requestId;
	session.setUpload('upload-2', 'b.tar.gz');
	assert.notEqual(session.state.requestId, first);
});

test('a stale preview cannot overwrite a newer node preview', async () => {
	const older = deferred<PreviewServerArchiveResponse>();
	const newer = deferred<PreviewServerArchiveResponse>();
	let call = 0;
	const session = createArchiveImport({
		previewServerArchive: () => (++call === 1 ? older.promise : newer.promise)
	});
	session.setUpload('upload-1', 'world.zip');

	const first = session.refresh('');
	const second = session.refresh('node-2');
	newer.resolve(preview('newer-node'));
	await second;
	older.resolve(preview('older-node'));
	await first;

	assert.equal(session.state.preview?.mcVersion, 'newer-node');
	assert.equal(session.state.preparing, false);
});

test('a refused preview keeps the upload and submission id for retry', async () => {
	const session = createArchiveImport({
		previewServerArchive: () => Promise.reject(new Error('checksum mismatch'))
	});
	session.setUpload('upload-1', 'world.zip');
	const requestId = session.state.requestId;

	await session.refresh('node-2');

	assert.equal(session.state.error, 'checksum mismatch');
	assert.equal(session.state.uploadId, 'upload-1');
	assert.equal(session.state.requestId, requestId);
	assert.equal(session.state.preview, null);
	assert.equal(session.state.preparing, false);
});

test('discard clears upload, preview and submission identity', async () => {
	const session = createArchiveImport({
		previewServerArchive: () => Promise.resolve(preview('1.21.1'))
	});
	session.setUpload('upload-1', 'world.zip');
	await session.refresh('');
	session.discard();

	assert.deepEqual(session.state, {
		uploadId: '',
		fileName: '',
		preview: null,
		preparing: false,
		error: null,
		requestId: ''
	});
});
