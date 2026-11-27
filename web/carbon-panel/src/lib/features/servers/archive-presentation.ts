// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Presentation policy for server portability archives (OSS-428).
 *
 * Pure helpers only: no RPC, store or DOM access. The export dialog and the
 * create-from-archive wizard render through this module so labels, format
 * selection, disclosure text and finding interpretation cannot drift apart.
 */

import { ModLoader } from '../../proto/carbonpanel/v1/common_pb';
import {
	ServerArchiveConsistency,
	ServerArchiveFindingCode,
	ServerArchiveFindingSeverity,
	ServerArchiveFormat,
	type PreviewServerArchiveResponse,
	type ServerArchiveFinding
} from '../../proto/carbonpanel/v1/server_pb';
import { formatFileSize, parseFileSize } from '../../utils/format-file-size';
import { enumToString } from '../../utils';

/**
 * Required disclosure. The exporter filters known managed settings and paths;
 * it cannot inspect arbitrary plugin/mod files. Never present this as secret
 * detection.
 */
export const ARCHIVE_CREDENTIAL_DISCLOSURE =
	'Third-party plugin and mod files are included and can contain credentials. Review the archive before sharing it.';

export const ARCHIVE_CREDENTIAL_DISCLOSURE_DETAIL =
	'Only recognized managed settings and paths are removed. The contents of third-party plugin and mod files are not inspected for secrets.';

/** Export preparation, preview and import validation may take up to an hour. */
export const ARCHIVE_RPC_OPTIONS = { timeoutMs: 60 * 60 * 1000 } as const;

export const ARCHIVE_FORMATS = [
	{ value: ServerArchiveFormat.ZIP, label: 'ZIP (.zip)', extension: '.zip' },
	{ value: ServerArchiveFormat.TAR_GZ, label: 'TAR.GZ (.tar.gz)', extension: '.tar.gz' }
] as const;

export const STOPPED_CONSISTENCY = ServerArchiveConsistency.STOPPED;

export function isSelectableArchiveFormat(format: ServerArchiveFormat): boolean {
	return ARCHIVE_FORMATS.some((entry) => entry.value === format);
}

export function archiveFormatLabel(format: ServerArchiveFormat): string {
	return ARCHIVE_FORMATS.find((entry) => entry.value === format)?.label ?? 'Unknown format';
}

export function archiveConsistencyLabel(consistency: ServerArchiveConsistency): string {
	return consistency === ServerArchiveConsistency.STOPPED
		? 'Stopped — the server must already be stopped'
		: 'Unsupported consistency';
}

/** Both encodings are accepted by the upload/preview path. */
export function isSupportedArchiveFilename(name: string): boolean {
	const lower = name.toLowerCase();
	return lower.endsWith('.zip') || lower.endsWith('.tar.gz') || lower.endsWith('.tgz');
}

export function findingSeverityLabel(severity: ServerArchiveFindingSeverity): string {
	switch (severity) {
		case ServerArchiveFindingSeverity.BLOCKER:
			return 'Blocker';
		case ServerArchiveFindingSeverity.WARNING:
			return 'Warning';
		default:
			return 'Unknown severity';
	}
}

export function findingCodeLabel(code: ServerArchiveFindingCode): string {
	switch (code) {
		case ServerArchiveFindingCode.CUSTOM_RUNTIME:
			return 'Custom runtime';
		case ServerArchiveFindingCode.PROVIDER_SETUP:
			return 'Provider setup';
		case ServerArchiveFindingCode.INSUFFICIENT_SPACE:
			return 'Insufficient space';
		case ServerArchiveFindingCode.STORAGE_UNAVAILABLE:
			return 'Storage unavailable';
		case ServerArchiveFindingCode.UNSUPPORTED_LOADER:
			return 'Unsupported loader';
		default:
			return 'Other';
	}
}

export interface ArchiveFindingView {
	severity: string;
	code: string;
	message: string;
	isBlocker: boolean;
}

export function describeFinding(finding: ServerArchiveFinding): ArchiveFindingView {
	return {
		severity: findingSeverityLabel(finding.severity),
		code: findingCodeLabel(finding.code),
		message: finding.message,
		isBlocker: finding.severity === ServerArchiveFindingSeverity.BLOCKER
	};
}

export type ArchiveCapacityState = 'unknown' | 'sufficient' | 'insufficient';

/**
 * Space reporting is node-dependent. Before a node is selected `availableBytes`
 * is absent: report that honestly rather than implying capacity was checked.
 */
export function archiveCapacityState(preview: PreviewServerArchiveResponse): ArchiveCapacityState {
	if (preview.availableBytes === undefined) return 'unknown';
	return parseFileSize(preview.availableBytes) >= parseFileSize(preview.requiredBytes)
		? 'sufficient'
		: 'insufficient';
}

export function capacityLabel(state: ArchiveCapacityState): string {
	switch (state) {
		case 'sufficient':
			return 'Destination has enough free space';
		case 'insufficient':
			return 'Destination does not have enough free space';
		default:
			return 'Select a destination node to check free space';
	}
}

export interface ArchivePreviewView {
	format: string;
	schemaVersion: number;
	exporterVersion: string;
	mcVersion: string;
	modLoader: string;
	loaderVersion: string;
	memoryMb: number;
	maxPlayers: number;
	fileCount: number;
	expandedBytes: string;
	requiredBytes: string;
	availableBytes: string | null;
	capacity: ArchiveCapacityState;
	capacityText: string;
	findings: ArchiveFindingView[];
	isBlocked: boolean;
}

export function describePreview(preview: PreviewServerArchiveResponse): ArchivePreviewView {
	const findings = preview.findings.map(describeFinding);
	const capacity = archiveCapacityState(preview);
	return {
		format: archiveFormatLabel(preview.format),
		schemaVersion: preview.schemaVersion,
		exporterVersion: preview.exporterVersion,
		mcVersion: preview.mcVersion,
		modLoader: enumToString(ModLoader, preview.modLoader) || 'unspecified',
		loaderVersion: preview.loaderVersion,
		memoryMb: preview.memory,
		maxPlayers: preview.maxPlayers,
		fileCount: preview.fileCount,
		expandedBytes: formatFileSize(parseFileSize(preview.expandedBytes)),
		requiredBytes: formatFileSize(parseFileSize(preview.requiredBytes)),
		availableBytes:
			preview.availableBytes === undefined
				? null
				: formatFileSize(parseFileSize(preview.availableBytes)),
		capacity,
		capacityText: capacityLabel(capacity),
		findings,
		isBlocked: findings.some((finding) => finding.isBlocker)
	};
}
