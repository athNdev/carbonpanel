// SPDX-License-Identifier: MPL-2.0
// Guards the GitHub Actions context rule that broke editor-dialog.yml: the
// `runner` context is unavailable in `jobs.<job_id>.env` (only in step-level
// env / run). See the "Context availability" table in the Actions docs.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const workflowsDir = join(root, '.github', 'workflows');
const read = (name) => readFileSync(join(workflowsDir, name), 'utf8').replace(/\r\n/g, '\n');
const workflowNames = readdirSync(workflowsDir).filter((name) => /\.ya?ml$/.test(name));

// Job-level keys are indented four spaces; their block extends over the
// following more-indented lines. Keys such as `container.env` or a step's
// `env` are indented deeper and are intentionally not treated as job-level.
function jobEnvBlocks(source) {
	const lines = source.split('\n');
	const blocks = [];
	for (let index = 0; index < lines.length; index++) {
		if (!/^    env:\s*(?:#.*)?$/.test(lines[index])) continue;
		const block = [lines[index]];
		for (let next = index + 1; next < lines.length; next++) {
			const candidate = lines[next];
			if (/^\s*$/.test(candidate)) {
				block.push(candidate);
				continue;
			}
			if (candidate.match(/^ */)[0].length > 4) {
				block.push(candidate);
				continue;
			}
			break;
		}
		blocks.push({ line: index + 1, text: block.join('\n') });
	}
	return blocks;
}

test('workflow job-level env blocks never use the runner context', () => {
	const offenders = [];
	for (const name of workflowNames) {
		for (const block of jobEnvBlocks(read(name))) {
			if (/\brunner\s*\./.test(block.text)) offenders.push(`${name}:${block.line}`);
		}
	}
	assert.deepEqual(offenders, [], `runner context in job-level env: ${offenders.join(', ')}`);
});

test('editor dialog derives its private browser cache from RUNNER_TEMP in a step', () => {
	const source = read('editor-dialog.yml');
	for (const block of jobEnvBlocks(source)) {
		assert.doesNotMatch(block.text, /PLAYWRIGHT_BROWSERS_PATH/);
	}
	assert.match(
		source,
		/PLAYWRIGHT_BROWSERS_PATH\s*=\s*["']?\$RUNNER_TEMP\/carbonpanel-editor-browsers["']?/
	);
	assert.match(source, />>\s*["']?\$GITHUB_ENV["']?/);
});
