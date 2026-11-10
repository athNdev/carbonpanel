// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

import { ModuleStatus } from './proto/carbonpanel/v1/module_pb';
import { moduleIdentityFromRecord, moduleStatusLabel, shortContainerId } from './module-identity';

test('the Modules tab identity comes from the reconciled record, not a stale inspect helper', () => {
	const staleInspect = {
		containerId: 'stale-before-restart',
		status: ModuleStatus.STOPPED
	};
	const reconciled = {
		containerId: 'after-reconnectabcdef',
		status: ModuleStatus.RUNNING
	};
	const identity = moduleIdentityFromRecord(reconciled);
	assert.equal(identity.containerId, 'after-reconnectabcdef');
	assert.equal(identity.shortContainerId, shortContainerId(reconciled.containerId));
	assert.equal(identity.status, ModuleStatus.RUNNING);
	assert.equal(moduleStatusLabel(identity.status), 'Running');
	assert.notEqual(identity.containerId, staleInspect.containerId);
	assert.notEqual(moduleStatusLabel(identity.status), moduleStatusLabel(staleInspect.status));
});

test('ActiveModules and ServerModules render identity from the listModules record', () => {
	const files = [
		path.resolve(import.meta.dirname, '../routes/modules/ActiveModules.svelte'),
		path.resolve(import.meta.dirname, './components/server/ServerModules.svelte')
	];
	for (const file of files) {
		const src = fs.readFileSync(file, 'utf-8');
		assert.ok(
			src.includes('moduleIdentityFromRecord'),
			`${path.basename(file)} must use the reconciled record helper`
		);
		assert.ok(
			src.includes('identity.shortContainerId') || src.includes('identity.containerId'),
			`${path.basename(file)} must show the record container id`
		);
		assert.ok(
			src.includes('moduleStatusLabel(identity.status)') ||
				src.includes('getStatusLabel(identity.status)'),
			`${path.basename(file)} must show status from the record identity`
		);
	}
});
