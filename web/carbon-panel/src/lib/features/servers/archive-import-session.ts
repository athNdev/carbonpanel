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
import type {
	PreviewServerArchiveResponse,
	ServerArchiveDestination
} from '../../proto/carbonpanel/v1/server_pb';
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
	/** Upload the successful preview belongs to; empty until a preview succeeds. */
	previewUploadId: string;
	/** Destination node the successful preview belongs to; empty for automatic placement. */
	previewNodeId: string;
	/** Enabled destinations from the placement preflight; retained across a failed preview. */
	destinations: ServerArchiveDestination[];
	/**
	 * Upload whose enabled-destination preflight succeeded. It stays set across a
	 * later failed node preview so the current preflight remains authoritative
	 * instead of a stale node inventory snapshot.
	 */
	preflightUploadId: string;
	preparing: boolean;
	error: string | null;
	/** Stable across retries of this submission; empty until an upload completes. */
	requestId: string;
}

/**
 * Submit may only use a preview that proved this exact upload for this exact,
 * concrete destination. A failed or superseded preview leaves `preview` null or
 * tied to an older node, and an inspection-only preview for the automatic
 * (empty) destination never qualifies create.
 */
export function isArchivePreviewCurrent(state: ArchiveImportState, nodeId: string): boolean {
	return Boolean(
		state.uploadId &&
		state.preview &&
		!state.preparing &&
		!state.error &&
		// An inspection preview without a node is useful, but it never qualifies
		// a create: the destination storage check would not have run.
		nodeId &&
		state.previewUploadId === state.uploadId &&
		state.previewNodeId === nodeId
	);
}

export function createArchiveImport(
	requests: PreviewRequests,
	onChange: (state: ArchiveImportState) => void = () => {}
) {
	const sequence = createRequestSequence();
	let uploadId = '';
	let fileName = '';
	let preview: PreviewServerArchiveResponse | null = null;
	let previewUploadId = '';
	let previewNodeId = '';
	let destinations: ServerArchiveDestination[] = [];
	let preflightUploadId = '';
	let preparing = false;
	let error: string | null = null;
	let attempt: { requestId: string; finish: () => void } | null = null;

	function snapshot(): ArchiveImportState {
		return {
			uploadId,
			fileName,
			preview,
			previewUploadId,
			previewNodeId,
			destinations,
			preflightUploadId,
			preparing,
			error,
			requestId: attempt?.requestId ?? ''
		};
	}

	function emit() {
		onChange(snapshot());
	}

	function setUpload(id: string, name: string): ArchiveImportState {
		if (uploadId === id) return snapshot();
		uploadId = id;
		fileName = name;
		preview = null;
		previewUploadId = '';
		previewNodeId = '';
		destinations = [];
		preflightUploadId = '';
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
		// A retry or destination change must never keep serving the previous
		// preview while the new validation is in flight.
		preview = null;
		previewUploadId = '';
		previewNodeId = '';
		error = null;
		emit();
		try {
			const result = await requests.previewServerArchive({ uploadId: currentUpload, nodeId });
			if (seq !== sequence.current || currentUpload !== uploadId) return;
			preview = result;
			previewUploadId = currentUpload;
			previewNodeId = nodeId;
			// The preflight is independent of the concrete-node capacity result, so
			// keep it available while the operator retries a failed preview.
			destinations = result.destinations;
			preflightUploadId = currentUpload;
		} catch (cause) {
			if (seq !== sequence.current || currentUpload !== uploadId) return;
			preview = null;
			previewUploadId = '';
			previewNodeId = '';
			error = cause instanceof Error ? cause.message : 'Archive preview failed';
		} finally {
			if (seq === sequence.current && currentUpload === uploadId) {
				preparing = false;
				emit();
			}
		}
	}

	/** Retry the same upload against the same destination after a refusal. */
	function retry(nodeId: string): Promise<void> {
		return refresh(nodeId);
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
		previewUploadId = '';
		previewNodeId = '';
		destinations = [];
		preflightUploadId = '';
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
		retry,
		succeeded,
		discard
	};
}

export type ArchiveImport = ReturnType<typeof createArchiveImport>;
