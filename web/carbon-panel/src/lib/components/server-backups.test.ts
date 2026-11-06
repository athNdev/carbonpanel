// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

const content = fs.readFileSync(
	path.resolve(import.meta.dirname, 'server-backups.svelte'),
	'utf-8'
);

test('backups tab offers native one-click backup', () => {
	assert.ok(content.includes('Back up now'), 'tab must offer a Back up now action');
	assert.ok(
		content.includes('rpcClient.backup.createBackup'),
		'button must call the native CreateBackup RPC'
	);
	assert.ok(content.includes('CreateBackupRequestSchema'), 'request must use the generated schema');
});

test('backup requires no sidecar module', () => {
	assert.ok(
		!content.toLowerCase().includes('mc-backup') && !content.includes('builtin-mc-backup'),
		'tab must not point at the retired sidecar module'
	);
});
