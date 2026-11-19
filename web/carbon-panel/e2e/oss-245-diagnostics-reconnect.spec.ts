// SPDX-License-Identifier: MPL-2.0
// OSS-245 / OSS-185 named live fixture.
//
// The four scenarios below drive the real ServerConsole in Chromium through
// the repository's existing owned review-browser harness
// (scripts/review/browser.mjs + scripts/review/oss-245-diagnostics-reconnect-check.mjs).
// They are skipped in the ordinary `bun run test` run because that job does
// not install a browser; the dedicated CI journey job sets
// OSS245_LIVE_BROWSER=1 and provides the owned browser cache.
//
//   OSS245_LIVE_BROWSER=1 REVIEW_CHROME=/path/to/chrome.exe \
//     bun test --conditions=browser e2e/oss-245-diagnostics-reconnect.spec.ts

import { test } from 'bun:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { resolve } from 'node:path';

const live = process.env.OSS245_LIVE_BROWSER === '1';
const runner = resolve(
	import.meta.dirname,
	'../../../scripts/review/oss-245-diagnostics-reconnect-check.mjs'
);

function runScenario(name: string) {
	const result = spawnSync('node', [runner, '--scenario', name], {
		encoding: 'utf8',
		timeout: 180000,
		env: { ...process.env }
	});
	assert.equal(
		result.status,
		0,
		`${name} failed (exit ${result.status ?? 'signal'}):\n${result.stdout}\n${result.stderr}`
	);
	assert.match(result.stdout, /^PASS:/m, `${name} did not report a PASS line`);
}

const LIVE_TIMEOUT_MS = 180000;

test.skipIf(!live)(
	'stale A log listing cannot render or save on B (Chromium)',
	() => runScenario('stale-listing'),
	LIVE_TIMEOUT_MS
);

test.skipIf(!live)(
	'AUTH_FAIL signs out without a reconnect spinner (Chromium)',
	() => runScenario('auth-fail'),
	LIVE_TIMEOUT_MS
);

test.skipIf(!live)(
	'a failed non-idempotent request is not blindly retried (Chromium)',
	() => runScenario('non-idempotent'),
	LIVE_TIMEOUT_MS
);

test.skipIf(!live)(
	'console subscription survives container recreation (Chromium)',
	() => runScenario('recreation'),
	LIVE_TIMEOUT_MS
);
