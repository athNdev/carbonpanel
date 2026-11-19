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

test('showcase capture is read-only and publication is confined to the asset branch', () => {
  const source = read('showcase.yml');
  const captureStart = source.indexOf('\n  capture:\n');
  const publishStart = source.indexOf('\n  publish:\n');
  assert.ok(captureStart >= 0 && publishStart > captureStart, 'showcase jobs are missing');
  const capture = source.slice(captureStart, publishStart);
  const publish = source.slice(publishStart);

  // Workflow default is read-only; the write scope exists only in publish.
  assert.match(source, /permissions:\n  contents: read/);
  assert.match(publish, /permissions:\n      contents: write/);
  assert.match(publish, /github\.event_name != 'pull_request'/);
  assert.match(publish, /github\.ref == 'refs\/heads\/main'/);

  // Capture never persists write credentials.
  assert.match(capture, /persist-credentials: false/);
  assert.doesNotMatch(capture, /git push/);

  // Publish checks out ONLY the dedicated branch and never checks out main.
  assert.match(publish, /ref: showcase-assets/);
  assert.doesNotMatch(publish, /ref: main\n/);
  assert.match(publish, /ref: \$\{\{ github\.sha \}\}\n\s+path: trusted-source/);
  assert.match(publish, /git push origin HEAD:showcase-assets/);
  assert.match(publish, /rev-parse --abbrev-ref HEAD\)" != "showcase-assets"/);

  // The artifact is data only: trusted code and plan come from a pinned,
  // credential-free source checkout in a separate path.
  assert.match(publish, /path: trusted-source/);
  assert.match(publish, /ref: \$\{\{ github\.sha \}\}/);
  assert.match(publish, /persist-credentials: false/);
  assert.match(publish, /trusted-source\/scripts\/showcase_assets\.py validate/);
  assert.match(publish, /trusted-source\/scripts\/review\/plan-showcase\.json/);
  assert.doesNotMatch(publish, /python3 \/tmp\/showcase-artifact\/showcase_assets\.py/);
  assert.match(publish, /artifact may not carry executable code or a plan/);

  // Publication refuses delayed/older/divergent captures and partial sets.
  assert.match(publish, /is not live main/);
  assert.match(publish, /merge-base --is-ancestor/);
  assert.match(publish, /README-canonical screenshot missing/);
  assert.match(publish, /would replace published assets with different bytes/);

  // No new secret or token: the only secret reference is the built-in token.
  const secretRefs = source.match(/secrets\.[A-Z_]+/g) ?? [];
  for (const ref of secretRefs) assert.equal(ref, 'secrets.GITHUB_TOKEN');
});

test('showcase publication cannot be cancelled by a routine verification run', () => {
  const source = read('showcase.yml');
  assert.match(source, /group: showcase-publish\n      cancel-in-progress: false/);
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
