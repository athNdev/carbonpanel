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

const fallthroughPattern = 'case ServerStatus.ERROR:\n\t\t\tcase ServerStatus.UNHEALTHY:';

test('UNHEALTHY renders yellow while ERROR stays red', () => {
	for (const [name, content] of [
		['overview', overview],
		['list', list],
		['detail', detail]
	] as const) {
		assert.ok(
			content.includes('case ServerStatus.UNHEALTHY:') && content.includes("return 'yellow'"),
			`${name} must map UNHEALTHY to the yellow tag`
		);
		assert.ok(
			!content.includes(fallthroughPattern),
			`${name} must not share an ERROR/UNHEALTHY red fallthrough`
		);
	}
});

test('overview pips and buckets treat unhealthy as degraded, not critical', () => {
	assert.ok(overview.includes("yellow: 'bg-[#f1c21b]'"), 'pip palette needs a yellow entry');
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

test('detail tile shows amber heartbeat and names the condition', () => {
	assert.ok(
		detail.includes('{:else if server.status === ServerStatus.ERROR}') &&
			detail.includes('{:else if server.status === ServerStatus.UNHEALTHY}'),
		'error and unhealthy heartbeats must be separate branches'
	);
	assert.ok(
		detail.includes('Server running but failing health checks'),
		'unhealthy tile must name the condition instead of a daemon code'
	);
});

test('list issues pill goes amber when nothing actually errored', () => {
	assert.ok(list.includes('hasErrorIssue'), 'pill must distinguish error from degraded');
	assert.ok(list.includes('border-[#f1c21b]'), 'degraded-only pill must use amber styling');
});

test('error tile names the recorded failure cause', () => {
	const detail = fs.readFileSync(path.resolve(import.meta.dirname, './[id]/+page.svelte'), 'utf-8');
	assert.ok(detail.includes('exitFailureCopy'), 'error tile must read the recorded reason');
	assert.ok(
		detail.includes('exitFailureCopy(server.statusReason)'),
		'error tile must name the failure instead of only a daemon code'
	);
});
