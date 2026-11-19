// SPDX-License-Identifier: MPL-2.0
// Guards the repository-side half of the OSS closure guard. The native Jira
// workflow is the blocking boundary; these checks only constrain the repo.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const read = (p) => readFileSync(join(root, p), 'utf8').replace(/\r\n/g, '\n');
const wf = read('.github/workflows/acceptance-evidence.yml');

test('acceptance-evidence workflow validates and audits real records', () => {
  assert.match(wf, /^permissions:\n  contents: read/m);
  assert.match(wf, /\bvalidate:\n/);
  assert.match(wf, /\bgithub-drift:\n/);
  assert.match(wf, /go test \.\/internal\/acceptanceevidence\/\.\.\./);
  assert.match(wf, /go run \.\/cmd\/acceptance-evidence validate/);
  assert.match(wf, /go run \.\/cmd\/acceptance-evidence verify-github/);
  assert.match(wf, /cron: '0 6 \* \* 1'/);
  assert.doesNotMatch(wf, /contents: write/);
});

test('drift job has explicit least-privilege read permissions and only the built-in token', () => {
  assert.match(wf, /github-drift:\n    runs-on: ubuntu-latest\n    permissions:\n      contents: read\n      pull-requests: read\n      actions: read/);
  const refs = wf.match(/secrets\.[A-Z_]+/g) ?? [];
  for (const ref of refs) assert.equal(ref, 'secrets.GITHUB_TOKEN');
  assert.match(wf, /GITHUB_TOKEN: \$\{\{ secrets\.GITHUB_TOKEN \}\}/);
});

test('gate paths cover the contract, validator and this workflow', () => {
  for (const path of [
    'docs/acceptance/**',
    'cmd/acceptance-evidence/**',
    'internal/acceptanceevidence/**',
    '.github/workflows/acceptance-evidence.yml',
  ]) {
    assert.ok(wf.includes(path), `missing path filter: ${path}`);
  }
});

test('contract is a non-empty record audit with a real deploy and reference run', () => {
  const contract = JSON.parse(read('docs/acceptance/oss-acceptance-evidence.json'));
  assert.equal(contract.version, '1');
  assert.equal(contract.project.key, 'OSS');
  assert.equal(contract.project.id, '10001');
  assert.equal(contract.gate.scope, 'record-auditor');
  assert.ok(contract.candidates.length > 0, 'candidate set must not be empty');
  assert.ok(contract.reference_runs.length > 0, 'at least one live reference run is required');
  const c = contract.candidates[0];
  assert.equal(c.issue_key, 'OSS-409');
  assert.equal(c.closure_state, 'recorded');
  assert.equal(c.tier, 'LIVE-VERIFIED');
  assert.ok(c.pr.merge_sha.length === 40);
  assert.ok(c.trusted_deploy_sha.length === 40);
  assert.equal(c.deploy.job, 'deploy');
  assert.equal(c.deploy.run_id, 44522728666);
  assert.equal(c.deploy.revision_sha, c.trusted_deploy_sha);
  assert.equal(contract.reference_runs[0].run_id, 44522728666);
  for (const tier of ['CODE-MERGED', 'CI-GREEN', 'BUILD-ONLY', 'CAPTURE-ONLY', 'DEPLOY-SKIPPED', 'LIVE-VERIFIED']) {
    assert.ok(contract.tiers[tier], `missing tier: ${tier}`);
  }
});

test('evidence artifact records the actual named deploy job and optional skip', () => {
  const ev = JSON.parse(read('docs/acceptance/evidence/OSS-409.json'));
  assert.equal(ev.deploy.workflow, 'Deploy Website to GitHub Pages');
  assert.equal(ev.deploy.head_sha, '69ddb46d596aea8ed117a3dba6a1f63521572664');
  const named = ev.deploy.jobs.find((j) => j.name === 'deploy');
  assert.equal(named.conclusion, 'success');
  assert.ok(ev.deploy.jobs.some((j) => j.name === 'notify-indexnow' && j.conclusion === 'skipped'));
});

test('documentation states the true native and scheduled-audit limits', () => {
  const doc = read('docs/acceptance/README.md');
  assert.match(doc, /cannot gate a Jira transition/);
  assert.match(doc, /does not (read Jira|prevent|compare Jira Done)/);
  assert.match(doc, /single human actor/);
  assert.match(doc, /Completion Evidence:/);
  assert.match(doc, /record-auditor/);
  assert.match(doc, /proposed.*fresh|freshness applies/i);
  assert.match(doc, /weekly cron therefore does not turn red/);
});
