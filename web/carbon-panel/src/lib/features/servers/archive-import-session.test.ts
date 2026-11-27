// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import {
	PreviewServerArchiveResponseSchema,
	ServerArchiveDestinationSchema,
	type PreviewServerArchiveResponse,
	type ServerArchiveDestination
} from '../../proto/carbonpanel/v1/server_pb';
import { createArchiveImport, isArchivePreviewCurrent } from './archive-import-session';

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
	assert.equal(session.state.previewNodeId, 'node-2');
	assert.equal(session.state.preparing, false);
	assert.equal(session.state.previewUploadId, 'upload-1');
});

test('only a successful preview of the current upload and node may create', async () => {
	const session = createArchiveImport({
		previewServerArchive: ({ nodeId }) => Promise.resolve(preview(`preview-${nodeId}`))
	});
	session.setUpload('upload-1', 'world.zip');

	assert.equal(isArchivePreviewCurrent(session.state, ''), false);
	await session.refresh('node-2');

	assert.equal(isArchivePreviewCurrent(session.state, 'node-2'), true);
	assert.equal(isArchivePreviewCurrent(session.state, ''), false);
	assert.equal(isArchivePreviewCurrent(session.state, 'node-3'), false);
	assert.equal(isArchivePreviewCurrent({ ...session.state, preview: null }, 'node-2'), false);
	assert.equal(isArchivePreviewCurrent({ ...session.state, error: 'refused' }, 'node-2'), false);
	assert.equal(isArchivePreviewCurrent({ ...session.state, preparing: true }, 'node-2'), false);
});

test('an inspection preview without a destination never qualifies create', async () => {
	const session = createArchiveImport({
		previewServerArchive: () => Promise.resolve(preview('inspection-only'))
	});
	session.setUpload('upload-1', 'world.zip');

	await session.refresh('');

	assert.equal(session.state.preview?.mcVersion, 'inspection-only');
	assert.equal(session.state.previewNodeId, '');
	assert.equal(
		isArchivePreviewCurrent(session.state, ''),
		false,
		'a generic inspection result must not be treated as node qualification'
	);
	assert.equal(isArchivePreviewCurrent(session.state, 'node-2'), false);
});

test('a same-node retry succeeds after a refused preview without a new upload', async () => {
	let call = 0;
	const session = createArchiveImport({
		previewServerArchive: () => {
			call += 1;
			return call === 1
				? Promise.reject(new Error('temporary storage refusal'))
				: Promise.resolve(preview('recovered'));
		}
	});
	session.setUpload('upload-1', 'world.zip');
	const requestId = session.state.requestId;

	await session.refresh('node-2');
	const failed = session.state;
	assert.equal(isArchivePreviewCurrent(failed, 'node-2'), false);
	assert.equal(failed.preview, null);

	await session.retry('node-2');
	const recovered = session.state;
	assert.equal(recovered.error, null);
	assert.equal(recovered.preview?.mcVersion, 'recovered');
	assert.equal(recovered.previewNodeId, 'node-2');
	assert.equal(isArchivePreviewCurrent(recovered, 'node-2'), true);
	assert.equal(recovered.uploadId, 'upload-1');
	assert.equal(recovered.requestId, requestId);
});

test('a late success from a superseded same-node retry is ignored', async () => {
	const older = deferred<PreviewServerArchiveResponse>();
	const newer = deferred<PreviewServerArchiveResponse>();
	let call = 0;
	const session = createArchiveImport({
		previewServerArchive: () => (++call === 1 ? older.promise : newer.promise)
	});
	session.setUpload('upload-1', 'world.zip');

	const first = session.retry('node-2');
	const second = session.retry('node-2');
	newer.reject(new Error('newer refusal'));
	await second;
	older.resolve(preview('late-success'));
	await first;

	assert.equal(session.state.error, 'newer refusal');
	assert.equal(session.state.preview, null);
	assert.equal(isArchivePreviewCurrent(session.state, 'node-2'), false);
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
		previewUploadId: '',
		previewNodeId: '',
		destinations: [],
		preflightUploadId: '',
		preparing: false,
		error: null,
		requestId: ''
	});
});

test('destination preflight survives a failed concrete preview and resets with a new upload', async () => {
	const destinations: ServerArchiveDestination[] = [
		create(ServerArchiveDestinationSchema, { id: 'node-2', name: 'Second Node' }),
		create(ServerArchiveDestinationSchema, { id: 'node-3', name: 'Third Node' })
	];
	let fail = false;
	const session = createArchiveImport({
		previewServerArchive: () =>
			fail
				? Promise.reject(new Error('storage refusal'))
				: Promise.resolve(
						create(PreviewServerArchiveResponseSchema, { mcVersion: '1.21.1', destinations })
					)
	});
	session.setUpload('upload-1', 'world.zip');
	await session.refresh('');
	assert.deepEqual(session.state.destinations, destinations);
	assert.equal(session.state.preflightUploadId, 'upload-1');

	fail = true;
	await session.refresh('node-2');
	assert.equal(session.state.error, 'storage refusal');
	assert.deepEqual(
		session.state.destinations,
		destinations,
		'the enabled destinations stay available for a same-node retry'
	);
	assert.equal(
		session.state.preflightUploadId,
		'upload-1',
		'a failed node preview must not revoke the current destination preflight'
	);

	session.setUpload('upload-2', 'replacement.zip');
	assert.deepEqual(
		session.state.destinations,
		[],
		'a replacement upload clears stale destinations'
	);
	assert.equal(
		session.state.preflightUploadId,
		'',
		'a replacement upload must wait for its own preflight'
	);
});

test('a settled preflight with no enabled destination stays settled and empty', async () => {
	const session = createArchiveImport({
		previewServerArchive: () =>
			Promise.resolve(create(PreviewServerArchiveResponseSchema, { mcVersion: '1.21.1' }))
	});
	session.setUpload('upload-1', 'world.zip');
	await session.refresh('');

	assert.deepEqual(session.state.destinations, []);
	assert.equal(session.state.preflightUploadId, 'upload-1');
	assert.equal(session.state.preparing, false);
	assert.equal(session.state.error, null);
	assert.equal(isArchivePreviewCurrent(session.state, ''), false);
});

test('a failed preflight never records a settled destination list', async () => {
	const session = createArchiveImport({
		previewServerArchive: () => Promise.reject(new Error('inventory unavailable'))
	});
	session.setUpload('upload-1', 'world.zip');

	await session.refresh('node-2');

	assert.equal(session.state.error, 'inventory unavailable');
	assert.equal(session.state.preflightUploadId, '');
	assert.deepEqual(session.state.destinations, []);
});
