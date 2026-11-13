// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

test('toolchain gate accepts consistent defaults and detects image and CI drift', () => {
	const source = resolve(dirname(fileURLToPath(import.meta.url)), '..');
	const root = mkdtempSync(join(tmpdir(), 'carbon-toolchain-policy-'));
	try {
		for (const path of [
			'toolchain-versions.json',
			'go.mod',
			'Makefile',
			'docker',
			'.github/workflows',
			'scripts/proto-generate.mjs',
			'scripts/check-toolchain-policy.mjs'
		]) {
			mkdirSync(dirname(join(root, path)), { recursive: true });
			cpSync(join(source, path), join(root, path), { recursive: true });
		}
		const check = () =>
			spawnSync(process.execPath, [join(root, 'scripts/check-toolchain-policy.mjs')], {
				encoding: 'utf8'
			});
		assert.equal(check().status, 0);
		const image = join(root, 'docker/Dockerfile.status');
		const imageSource = readFileSync(image, 'utf8');
		writeFileSync(image, imageSource.replace(/golang:[^\s]+-alpine/, 'golang:0.0.0-alpine'));
		const imageFailure = check();
		assert.notEqual(imageFailure.status, 0);
		assert.match(imageFailure.stderr, /Dockerfile.status/);
		writeFileSync(image, imageSource);
		const workflow = join(root, '.github/workflows/toolchain-policy.yml');
		const policy = JSON.parse(readFileSync(join(root, 'toolchain-versions.json'), 'utf8'));
		writeFileSync(
			workflow,
			readFileSync(workflow, 'utf8').replace(`bun-version: '${policy.bun}'`, "bun-version: '0.0.0'")
		);
		const workflowFailure = check();
		assert.notEqual(workflowFailure.status, 0);
		assert.match(workflowFailure.stderr, /toolchain-policy.yml/);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});
