// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import { ModLoader } from '../../proto/carbonpanel/v1/common_pb';
import {
	PreviewServerArchiveResponseSchema,
	ServerArchiveFindingCode,
	ServerArchiveFindingSchema,
	ServerArchiveFindingSeverity,
	ServerArchiveFormat
} from '../../proto/carbonpanel/v1/server_pb';
import {
	ARCHIVE_CREDENTIAL_DISCLOSURE,
	ARCHIVE_FORMATS,
	STOPPED_CONSISTENCY,
	archiveCapacityState,
	archiveConsistencyLabel,
	archiveFormatLabel,
	describeFinding,
	describePreview,
	isSelectableArchiveFormat,
	isSupportedArchiveFilename
} from './archive-presentation';
import { ServerArchiveConsistency } from '../../proto/carbonpanel/v1/server_pb';

test('the required third-party credential disclosure is reproduced exactly', () => {
	assert.equal(
		ARCHIVE_CREDENTIAL_DISCLOSURE,
		'Third-party plugin and mod files are included and can contain credentials. Review the archive before sharing it.'
	);
});

test('only the two supported export formats are selectable', () => {
	assert.deepEqual(
		ARCHIVE_FORMATS.map((f) => f.value),
		[ServerArchiveFormat.ZIP, ServerArchiveFormat.TAR_GZ]
	);
	assert.equal(isSelectableArchiveFormat(ServerArchiveFormat.ZIP), true);
	assert.equal(isSelectableArchiveFormat(ServerArchiveFormat.TAR_GZ), true);
	assert.equal(isSelectableArchiveFormat(ServerArchiveFormat.UNSPECIFIED), false);
	assert.match(archiveFormatLabel(ServerArchiveFormat.ZIP), /ZIP/);
	assert.match(archiveFormatLabel(ServerArchiveFormat.TAR_GZ), /TAR/);
});

test('export consistency is stopped only', () => {
	assert.equal(STOPPED_CONSISTENCY, ServerArchiveConsistency.STOPPED);
	assert.match(
		archiveConsistencyLabel(ServerArchiveConsistency.STOPPED),
		/must already be stopped/
	);
});

test('upload accepts both archive encodings and rejects unrelated files', () => {
	for (const name of ['world.zip', 'world.tar.gz', 'WORLD.TGZ', 'backup.TAR.GZ']) {
		assert.equal(isSupportedArchiveFilename(name), true, name);
	}
	for (const name of ['world.rar', 'world.gz', 'world.json', 'world.zip.exe']) {
		assert.equal(isSupportedArchiveFilename(name), false, name);
	}
});

test('capacity is reported honestly before a node is selected', () => {
	const base = {
		requiredBytes: 2048n,
		expandedBytes: 1024n
	};
	assert.equal(archiveCapacityState(create(PreviewServerArchiveResponseSchema, base)), 'unknown');
	assert.equal(
		archiveCapacityState(
			create(PreviewServerArchiveResponseSchema, { ...base, availableBytes: 4096n })
		),
		'sufficient'
	);
	assert.equal(
		archiveCapacityState(
			create(PreviewServerArchiveResponseSchema, { ...base, availableBytes: 100n })
		),
		'insufficient'
	);
});

test('preview presentation preserves typed findings and blocking status', () => {
	const preview = create(PreviewServerArchiveResponseSchema, {
		format: ServerArchiveFormat.TAR_GZ,
		schemaVersion: 1,
		exporterVersion: '1.2.3',
		mcVersion: '1.21.1',
		modLoader: ModLoader.FABRIC,
		loaderVersion: '0.16.0',
		memory: 4096,
		maxPlayers: 12,
		fileCount: 42,
		expandedBytes: 1048576n,
		requiredBytes: 2097152n,
		availableBytes: 1048576n,
		findings: [
			create(ServerArchiveFindingSchema, {
				severity: ServerArchiveFindingSeverity.WARNING,
				code: ServerArchiveFindingCode.UNSUPPORTED_LOADER,
				message: 'Loader build unavailable'
			}),
			create(ServerArchiveFindingSchema, {
				severity: ServerArchiveFindingSeverity.BLOCKER,
				code: ServerArchiveFindingCode.INSUFFICIENT_SPACE,
				message: 'Not enough space'
			})
		]
	});

	const view = describePreview(preview);
	assert.match(view.format, /TAR/);
	assert.equal(view.mcVersion, '1.21.1');
	assert.equal(view.modLoader, 'fabric');
	assert.equal(view.memoryMb, 4096);
	assert.equal(view.fileCount, 42);
	assert.equal(view.availableBytes, '1 MB');
	assert.equal(view.capacity, 'insufficient');
	assert.equal(view.isBlocked, true);
	assert.deepEqual(
		view.findings.map((f) => f.isBlocker),
		[false, true]
	);
	assert.deepEqual(
		view.findings.map((f) => f.code),
		['Unsupported loader', 'Insufficient space']
	);
});

test('a preview with warnings only is not blocked', () => {
	const finding = describeFinding(
		create(ServerArchiveFindingSchema, {
			severity: ServerArchiveFindingSeverity.WARNING,
			code: ServerArchiveFindingCode.PROVIDER_SETUP,
			message: 'Supply provider credentials'
		})
	);
	assert.equal(finding.severity, 'Warning');
	assert.equal(finding.isBlocker, false);
	assert.equal(describePreview(create(PreviewServerArchiveResponseSchema, {})).isBlocked, false);
});
