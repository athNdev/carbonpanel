// SPDX-License-Identifier: MPL-2.0
// Bounded seam replacing the real chunked uploader in the seeded fixture.
// `success` yields an upload session; `fail` rejects through the same normal
// uploadFile seam the wizard calls, so toast/error behavior is real UI behavior.
// `pending` holds the upload open until the test releases it, which lets the
// fixture prove that a replacement upload fences an older completion.

const calls = [];
globalThis.__wizardUploadCalls = calls;
globalThis.__wizardUploadMode = 'success';
globalThis.__wizardPendingUploads = [];

export function uploadFile(file, options) {
	const mode = globalThis.__wizardUploadMode || 'success';
	const callIndex = calls.length + 1;
	calls.push({ name: file.name, size: file.size, mode });
	if (mode === 'fail') {
		return Promise.reject(new Error('seeded upload failure'));
	}
	const sessionId =
		globalThis.__wizardUploadSessionId ||
		(callIndex === 1 ? 'seeded-session' : `seeded-session-${callIndex}`);
	delete globalThis.__wizardUploadSessionId;
	if (mode === 'pending') {
		return new Promise((resolve) => {
			globalThis.__wizardPendingUploads.push({
				fileName: file.name,
				release(overrideSessionId) {
					options?.onProgress?.({
						sessionId: overrideSessionId || sessionId,
						bytesUploaded: file.size,
						totalBytes: file.size,
						chunksUploaded: 1,
						totalChunks: 1,
						percentComplete: 100
					});
					resolve({
						sessionId: overrideSessionId || sessionId,
						tempPath: `/tmp/${overrideSessionId || sessionId}`
					});
				}
			});
		});
	}
	options?.onProgress?.({
		sessionId,
		bytesUploaded: file.size,
		totalBytes: file.size,
		chunksUploaded: 1,
		totalChunks: 1,
		percentComplete: 100
	});
	return Promise.resolve({ sessionId, tempPath: `/tmp/${sessionId}` });
}
