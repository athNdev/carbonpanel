// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Create-from-archive submission state (OSS-428).
 *
 * Owns two things the wizard needs to get right:
 *  - preview responses are fenced by a request sequence, so an older preview
 *    (for example one taken before a node was chosen) cannot overwrite a newer
 *    result;
 *  - the create request id is allocated once per completed upload and kept
 *    across retries. A refused create leaves the upload in place so the
 *    operator can correct the destination and retry with the same id.
 *
 * The upload itself stays in the wizard: this session only coordinates preview
 * and submission identity.
 */

import { beginLifecycleRequest } from '../../lifecycle-request';
import type { PreviewServerArchiveResponse } from '../../proto/carbonpanel/v1/server_pb';
import { createRequestSequence } from '../../utils/request-sequence';

export interface ArchivePreviewCall {
	uploadId: string;
	nodeId: string;
}

interface PreviewRequests {
	previewServerArchive(request: ArchivePreviewCall): Promise<PreviewServerArchiveResponse>;
}

export interface ArchiveImportState {
	uploadId: string;
	fileName: string;
	preview: PreviewServerArchiveResponse | null;
	preparing: boolean;
	error: string | null;
	/** Stable across retries of this submission; empty until an upload completes. */
	requestId: string;
}

export function createArchiveImport(
	requests: PreviewRequests,
	onChange: (state: ArchiveImportState) => void = () => {}
) {
	const sequence = createRequestSequence();
	let uploadId = '';
	let fileName = '';
	let preview: PreviewServerArchiveResponse | null = null;
	let preparing = false;
	let error: string | null = null;
	let attempt: { requestId: string; finish: () => void } | null = null;

	function snapshot(): ArchiveImportState {
		return { uploadId, fileName, preview, preparing, error, requestId: attempt?.requestId ?? '' };
	}

	function emit() {
		onChange(snapshot());
	}

	function setUpload(id: string, name: string): ArchiveImportState {
		if (uploadId === id) return snapshot();
		uploadId = id;
		fileName = name;
		preview = null;
		preparing = false;
		error = null;
		attempt?.finish();
		attempt = id ? beginLifecycleRequest(id, 'create-archive') : null;
		emit();
		return snapshot();
	}

	async function refresh(nodeId: string): Promise<void> {
		if (!uploadId) return;
		const seq = sequence.next();
		const currentUpload = uploadId;
		preparing = true;
		error = null;
		emit();
		try {
			const result = await requests.previewServerArchive({ uploadId: currentUpload, nodeId });
			if (seq !== sequence.current || currentUpload !== uploadId) return;
			preview = result;
		} catch (cause) {
			if (seq !== sequence.current || currentUpload !== uploadId) return;
			preview = null;
			error = cause instanceof Error ? cause.message : 'Archive preview failed';
		} finally {
			if (seq === sequence.current && currentUpload === uploadId) {
				preparing = false;
				emit();
			}
		}
	}

	/** Called only after a committed create. A refusal keeps upload and id. */
	function succeeded() {
		attempt?.finish();
		attempt = null;
		emit();
	}

	function discard() {
		sequence.next();
		uploadId = '';
		fileName = '';
		preview = null;
		preparing = false;
		error = null;
		attempt?.finish();
		attempt = null;
		emit();
	}

	return {
		get state(): ArchiveImportState {
			return snapshot();
		},
		setUpload,
		refresh,
		succeeded,
		discard
	};
}

export type ArchiveImport = ReturnType<typeof createArchiveImport>;
