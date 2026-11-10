// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {
	MEMORY_LIMIT_UNSUPPORTED,
	STORAGE_QUOTA_IS_NOT_A_WORLD_QUOTA,
	WRITABLE_LAYER_CAP_UNSUPPORTED,
	ROOTLESS_CGROUP_UNQUALIFIED,
	exitFailureCopy,
	exitFailureKind,
	ignoresResourceWarnings,
	memoryLimitCopy,
	worldDiskCopy
} from './resource-enforcement';

test('memory_limit_unsupported is advisory even if memoryEnforced is true', () => {
	const copy = memoryLimitCopy({
		memoryEnforced: true,
		warnings: [MEMORY_LIMIT_UNSUPPORTED],
		requestedMemoryBytes: 2048 * 1024 * 1024,
		effectiveMemoryBytes: 2048 * 1024 * 1024
	});
	assert.equal(copy.kind, 'advisory');
	assert.equal(
		ignoresResourceWarnings({ memoryEnforced: true, warnings: [MEMORY_LIMIT_UNSUPPORTED] }),
		false
	);
});

test('rootless_cgroup_unqualified is advisory, not enforced', () => {
	assert.equal(
		memoryLimitCopy({ memoryEnforced: true, warnings: [ROOTLESS_CGROUP_UNQUALIFIED] }).kind,
		'advisory'
	);
});

test('storage-opt warning never claims a world quota', () => {
	const copy = worldDiskCopy({
		worldDiskEnforced: true,
		warnings: [STORAGE_QUOTA_IS_NOT_A_WORLD_QUOTA]
	});
	assert.equal(copy.kind, 'advisory');
	assert.match(copy.label, /not a world quota/i);
	assert.equal(
		ignoresResourceWarnings({
			worldDiskEnforced: true,
			warnings: [STORAGE_QUOTA_IS_NOT_A_WORLD_QUOTA]
		}),
		false
	);
});

test('writable_layer_cap_unsupported is not a world quota', () => {
	assert.equal(
		worldDiskCopy({ worldDiskEnforced: true, warnings: [WRITABLE_LAYER_CAP_UNSUPPORTED] }).kind,
		'advisory'
	);
});

test('filling the memory budget does not imply world disk is bounded', () => {
	const memory = memoryLimitCopy({
		memoryEnforced: true,
		warnings: [],
		requestedMemoryBytes: 2 << 30,
		effectiveMemoryBytes: 2 << 30
	});
	const disk = worldDiskCopy({
		memoryEnforced: true,
		worldDiskEnforced: false,
		warnings: []
	});
	assert.equal(memory.kind, 'enforced');
	assert.notEqual(disk.kind, 'enforced');
});

test('world_quota_mb is named as admission, not kernel enforcement', () => {
	const copy = worldDiskCopy({ worldQuotaAdmission: true, worldDiskEnforced: false, warnings: [] });
	assert.equal(copy.kind, 'admission');
	assert.match(copy.label, /admission/i);
	assert.doesNotMatch(copy.label, /enforced/i);
});

test('OOM stays distinct from forced-stop exit 137', () => {
	assert.equal(exitFailureKind('container was OOM-killed (exit code 137)'), 'oom');
	assert.equal(exitFailureKind('container exited with code 137'), 'forced-stop');
	assert.match(exitFailureCopy('container was OOM-killed (exit code 137)'), /Out of memory/);
	assert.match(exitFailureCopy('container exited with code 137'), /Forced stop/);
	assert.notEqual(
		exitFailureCopy('container was OOM-killed (exit code 137)'),
		exitFailureCopy('container exited with code 137')
	);
});

test('server resource UI consults RuntimeProfile warnings', () => {
	const detail = fs.readFileSync(
		path.resolve(import.meta.dirname, '../../routes/servers/[id]/+page.svelte'),
		'utf-8'
	);
	const create = fs.readFileSync(
		path.resolve(import.meta.dirname, '../../routes/servers/new/+page.svelte'),
		'utf-8'
	);
	const overrides = fs.readFileSync(
		path.resolve(import.meta.dirname, '../components/docker-overrides-editor.svelte'),
		'utf-8'
	);
	assert.ok(detail.includes('memoryLimitCopy'), 'detail RAM tile must use memoryLimitCopy');
	assert.ok(detail.includes('worldDiskCopy'), 'detail must show world-disk advisory copy');
	assert.ok(detail.includes('exitFailureCopy'), 'error tile must keep OOM vs forced-stop distinct');
	assert.ok(
		create.includes('memoryLimitCopy') || create.includes('not a hard cap'),
		'create wizard must not present RAM as an always-enforced quota'
	);
	assert.ok(
		!create.toLowerCase().includes('resource quotas'),
		'create header must not call configured requests quotas'
	);
	assert.ok(
		overrides.toLowerCase().includes('advisory') || overrides.includes('not enforced'),
		'override memory field must warn that the engine may ignore the request'
	);
});
