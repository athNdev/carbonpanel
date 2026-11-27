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

  // Publication advances through trusted main history and needs a canonical set.
  assert.match(publish, /outside live main history/);
  assert.match(publish, /merge-base --is-ancestor/);
  assert.match(publish, /README-canonical screenshot missing/);
  assert.match(publish, /already-current=true/);
  assert.match(publish, /needs\.capture\.outputs\.canonical == 'true'/);

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

// gh CLI authenticates from GH_TOKEN on its OWN step: a token set on another
// step or job is not inherited. The v2.0.0 publish gate failed closed because
// verify-publication-ref called gh (via scripts/release.py) without its own
// GH_TOKEN. Guard every gh-calling publication gate step in both lanes.
const GH_SUBCOMMANDS = new Set([
  'plan',
  'preflight-immutable',
  'verify-publication-ref',
  'verify-main-ci',
  'release-record',
  'publish',
  'verify-release-record',
  'alias-promotion',
  'promote-alias',
]);

function parseSteps(source) {
  const lines = source.split('\n');
  const steps = [];
  let current = null;
  let mode = 'none';
  for (let index = 0; index < lines.length; index++) {
    const line = lines[index];
    if (/^      - /.test(line)) {
      if (current) steps.push(current);
      current = { line: index + 1, name: '', env: new Set(), run: '' };
      mode = 'none';
    }
    if (!current || /^\s*$/.test(line)) continue;
    const named = line.match(/^      - name:\s*(.+?)\s*$/);
    if (named) current.name = named[1];
    if (/^        env:\s*$/.test(line)) {
      mode = 'env';
      continue;
    }
    if (/^        run:/.test(line)) {
      mode = 'run';
      current.run += line.replace(/^        run:\s*/, '') + '\n';
      continue;
    }
    if (mode === 'env') {
      const key = line.match(/^          ([A-Za-z_][A-Za-z0-9_]*):/);
      if (key) current.env.add(key[1]);
      else if (/^\s{0,10}\S/.test(line)) mode = 'none';
    } else if (mode === 'run') {
      if (/^ {10,}/.test(line)) current.run += line.trim() + '\n';
      else mode = 'none';
    }
  }
  if (current) steps.push(current);
  return steps;
}

function callsGh(step) {
  const run = step.run.split('\n').filter((line) => !/^\s*#/.test(line)).join('\n');
  if (/\bgh\s/.test(run)) return true;
  return [...run.matchAll(/scripts\/release\.py\s+([a-z0-9-]+)/g)]
    .some((match) => GH_SUBCOMMANDS.has(match[1]));
}

test('every gh-calling publication step carries its own GH_TOKEN', () => {
  const offenders = [];
  for (const name of ['release.yml', 'cd.yml']) {
    for (const step of parseSteps(read(name))) {
      if (callsGh(step) && !step.env.has('GH_TOKEN')) {
        offenders.push(`${name}:${step.line} (${step.name || 'unnamed'})`);
      }
    }
  }
  assert.deepEqual(offenders, [], `gh-calling steps without GH_TOKEN: ${offenders.join(', ')}`);
});

test('verify-publication-ref gate sets GH_TOKEN in its own step env', () => {
  const step = parseSteps(read('release.yml')).find(
    (candidate) => candidate.name === 'Verify trusted ref and exact main CI for an explicit publication'
  );
  assert.ok(step, 'verify-publication-ref step is missing');
  assert.match(step.run, /verify-publication-ref/);
  assert.ok(
    step.env.has('GH_TOKEN'),
    'verify-publication-ref calls gh; it must set GH_TOKEN in its own step env'
  );
});

test('server archive UI rehearsal keeps the RPC job and adds a read-only disposable UI job', () => {
  const source = read('server-archive-rehearsal.yml');

  // The existing RPC receipt keeps its exact artifact name and permissions.
  assert.match(source, /permissions:\n  contents: read/);
  assert.match(
    source,
    /name: server-archive-rehearsal-\$\{\{ github\.event\.pull_request\.head\.sha \|\| github\.sha \}\}/
  );
  assert.match(source, /bash scripts\/qualify-server-archive-hosted\.sh "\$RUNNER_TEMP\/server-archive-rehearsal"/);

  const uiStart = source.indexOf('\n  ui:\n');
  assert.ok(uiStart >= 0, 'the production UI job is missing');
  const ui = source.slice(uiStart);

  // Fork PRs still cannot run PR-contributed code with a Docker socket.
  assert.match(ui, /github\.event\.pull_request\.head\.repo\.full_name == github\.repository/);
  // The UI job checks out the same PR head it builds and photographs.
  assert.match(ui, /ref: \$\{\{ github\.event\.pull_request\.head\.sha \|\| github\.sha \}\}/);
  assert.match(ui, /persist-credentials: false/);
  // The production frontend mode is explicit and uses the shared hosted wrapper.
  assert.match(ui, /ARCHIVE_UI: '1'/);
  assert.match(ui, /bash scripts\/qualify-server-archive-hosted\.sh "\$RUNNER_TEMP\/server-archive-ui"/);
  // The browser driver is validated offline before any disposable service starts.
  assert.match(ui, /node --test scripts\/qualify-archive-ui-hosted\.test\.mjs/);
  assert.match(ui, /node --check scripts\/qualify-archive-ui-hosted\.mjs/);
  // Evidence survives a failed run but no write scope or credential is added.
  assert.match(
    ui,
    /name: server-archive-ui-\$\{\{ github\.event\.pull_request\.head\.sha \|\| github\.sha \}\}/
  );
  assert.match(ui, /if: always\(\)[\s\S]*actions\/upload-artifact/);
  assert.doesNotMatch(ui, /secrets\./);
  assert.doesNotMatch(ui, /contents: write|packages: write|id-token: write/);
});
