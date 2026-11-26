// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

test('Server detail page correctly differentiates proxied vs direct port ingress', () => {
	const serverPagePath = path.resolve(
		import.meta.dirname,
		'../../routes/servers/[id]/+page.svelte'
	);
	const content = fs.readFileSync(serverPagePath, 'utf-8');

	// Must fetch proxy status to verify if proxy is actually active
	assert.ok(
		content.includes('rpcClient.proxy') && content.includes('getProxyStatus'),
		'Server detail page must query proxy status'
	);

	// Must derive isProxied checking proxyStatus, hostname, and non-zero proxy port
	assert.ok(
		content.includes('isProxied') && content.includes('server?.proxyPort'),
		'Must derive isProxied with proxy port guard'
	);

	assert.match(content, /onclick=\{\(\) => copyConnection\(connectAddress\)\}/);
	assert.match(content, /<span>Copy Address<\/span>/);

	// Direct port must use window.location.hostname for remote clients
	assert.ok(
		content.includes('window.location.hostname'),
		'Direct port address must resolve window.location.hostname'
	);
});

test('Server creation form does not leak proxy parameters on direct port mode', () => {
	const newPagePath = path.resolve(import.meta.dirname, '../../routes/servers/new/+page.svelte');
	const content = fs.readFileSync(newPagePath, 'utf-8');

	// createRequest must only attach proxyHostname and proxyListenerId when useProxyMode is true
	assert.ok(
		content.includes("proxyHostname: useProxyMode && proxyEnabled ? formData.proxyHostname : ''"),
		'Must clear proxyHostname when not in proxy mode'
	);
	assert.ok(
		content.includes(
			"proxyListenerId: useProxyMode && proxyEnabled ? formData.proxyListenerId : ''"
		),
		'Must clear proxyListenerId when not in proxy mode'
	);
});

test('Server creation wizard can queue local datapack uploads for post-create install', () => {
	const newPagePath = path.resolve(import.meta.dirname, '../../routes/servers/new/+page.svelte');
	const content = fs.readFileSync(newPagePath, 'utf-8');

	assert.ok(
		content.includes("import { uploadFile } from '$lib/utils/chunked-upload';"),
		'wizard must use chunked upload utility for local datapacks'
	);
	assert.ok(
		content.includes('pendingDatapackUploads'),
		'wizard must track pending local datapack uploads'
	);
	assert.ok(
		content.includes('handleWizardDatapackUpload'),
		'wizard must handle local datapack archive uploads'
	);
	assert.ok(
		content.includes('rpcClient.datapack.uploadDatapack'),
		'wizard must install queued local datapacks after server creation'
	);
	assert.ok(
		content.includes('Upload local datapack'),
		'wizard datapack step must expose a local upload workflow'
	);
	assert.ok(
		content.includes('Local upload'),
		'queued local datapack uploads must be labeled in the selection list'
	);
});
