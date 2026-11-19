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

test('acceptance-evidence workflow validates, audits drift and is read-only', () => {
  assert.match(wf, /^permissions:\n  contents: read/m);
  assert.match(wf, /\bvalidate:\n/);
  assert.match(wf, /\bgithub-drift:\n/);
  assert.match(wf, /go test \.\/internal\/acceptanceevidence\/\.\.\./);
  assert.match(wf, /go run \.\/cmd\/acceptance-evidence validate/);
  assert.match(wf, /go run \.\/cmd\/acceptance-evidence verify-github/);
  assert.match(wf, /cron: '0 6 \* \* 1'/);
  assert.doesNotMatch(wf, /contents: write/);
});

test('drift audit uses only the built-in token, never a new secret', () => {
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

test('contract declares the OSS project, tiers and an empty candidate list', () => {
  const contract = JSON.parse(read('docs/acceptance/oss-acceptance-evidence.json'));
  assert.equal(contract.version, '1');
  assert.equal(contract.project.key, 'OSS');
  assert.equal(contract.project.id, '10001');
  assert.deepEqual(contract.candidates, []);
  for (const tier of ['CODE-MERGED', 'CI-GREEN', 'BUILD-ONLY', 'CAPTURE-ONLY', 'DEPLOY-SKIPPED', 'LIVE-VERIFIED']) {
    assert.ok(contract.tiers[tier], `missing tier: ${tier}`);
  }
});

test('documentation states the true native limit without claiming a false guarantee', () => {
  const doc = read('docs/acceptance/README.md');
  assert.match(doc, /cannot gate a Jira transition/);
  assert.match(doc, /single human actor/);
  assert.match(doc, /Completion Evidence:/);
  assert.match(doc, /not magically fix/);
});
