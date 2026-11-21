// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

const overview = fs.readFileSync(path.resolve(import.meta.dirname, '../+page.svelte'), 'utf-8');
const list = fs.readFileSync(path.resolve(import.meta.dirname, './+page.svelte'), 'utf-8');
const detail = fs.readFileSync(path.resolve(import.meta.dirname, './[id]/+page.svelte'), 'utf-8');
const runtimeStatus = fs.readFileSync(
	path.resolve(import.meta.dirname, '../../lib/components/server-ui/ServerRuntimeStatus.svelte'),
	'utf-8'
);

test('all inventory routes consume the shared severity policy', () => {
	for (const [name, content] of [
		['overview', overview],
		['list', list],
		['detail', detail]
	] as const) {
		assert.ok(
			content.includes("from '$lib/features/servers/presentation'"),
			`${name} must use shared status policy`
		);
		assert.ok(
			content.includes('getStatusTagType(server.status)'),
			`${name} renders the shared status tone`
		);
	}
});

test('overview pips and buckets treat unhealthy as degraded, not critical', () => {
	assert.ok(
		overview.includes("yellow: 'bg-warning'"),
		'pip palette needs a tokenized yellow entry'
	);
	assert.ok(
		overview.includes('s.status === ServerStatus.UNHEALTHY'),
		'warning bucket must count unhealthy'
	);
	const criticalBlock = overview.slice(
		overview.indexOf('critical: dashboardServers.filter('),
		overview.indexOf('critical: dashboardServers.filter(') + 400
	);
	assert.ok(!criticalBlock.includes('UNHEALTHY'), 'critical bucket must not count unhealthy');
});

test('detail runtime card distinguishes degraded and failed states', () => {
	assert.ok(
		runtimeStatus.includes('{:else if status === ServerStatus.ERROR}') &&
			runtimeStatus.includes('{:else if status === ServerStatus.UNHEALTHY}'),
		'error and unhealthy descriptions must be separate branches'
	);
	assert.ok(
		runtimeStatus.includes('Server running but failing health checks'),
		'unhealthy tile must name the condition instead of a daemon code'
	);
});

test('list issues pill goes amber when nothing actually errored', () => {
	assert.ok(list.includes('hasErrorIssue'), 'pill must distinguish error from degraded');
	assert.ok(list.includes('border-warning'), 'degraded-only pill must use tokenized amber styling');
});

test('error tile names the recorded failure cause', () => {
	assert.ok(
		detail.includes('statusReason={server.statusReason}'),
		'detail must pass the recorded reason'
	);
	assert.ok(
		runtimeStatus.includes('exitFailureCopy(statusReason)'),
		'error tile must name the failure instead of only a daemon code'
	);
});
