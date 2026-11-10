// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

test('activity list shows the journal request id when present', () => {
	const activityPath = path.resolve(import.meta.dirname, 'server-activity.svelte');
	const content = fs.readFileSync(activityPath, 'utf-8');
	assert.ok(content.includes('Request ID'), 'activity table must have a request id column');
	assert.ok(
		content.includes('entry.requestId'),
		'activity rows must render the persisted request id'
	);
	assert.ok(
		!content.includes('listActivityLogs(') || content.includes('serverId: server.id'),
		'activity list stays scoped to the open server'
	);
});
