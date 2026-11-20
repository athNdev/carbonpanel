// SPDX-License-Identifier: MPL-2.0
// Bounded seam replacing the real chunked uploader in the seeded fixture.
// `success` yields an upload session; `fail` rejects through the same normal
// uploadFile seam the wizard calls, so toast/error behavior is real UI behavior.

const calls = [];
globalThis.__wizardUploadCalls = calls;
globalThis.__wizardUploadMode = 'success';

export function uploadFile(file, options) {
	const mode = globalThis.__wizardUploadMode || 'success';
	calls.push({ name: file.name, size: file.size, mode });
	options?.onProgress?.({
		sessionId: 'seeded-session',
		bytesUploaded: file.size,
		totalBytes: file.size,
		chunksUploaded: 1,
		totalChunks: 1,
		percentComplete: 100
	});
	if (mode === 'fail') {
		return Promise.reject(new Error('seeded upload failure'));
	}
	return Promise.resolve({ sessionId: 'seeded-session', tempPath: '/tmp/seeded-session' });
}
