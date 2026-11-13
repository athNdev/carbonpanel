// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { reviewCommand } from './run.mjs';
import { writeReviewReport } from './report.mjs';

test('profiles dispatch explicit stack/review commands without shell interpretation', () => {
  const single = reviewCommand(['single-seeded', 'up', '--label with spaces']);
  const dual = reviewCommand(['dual-ownership', 'down']);
  assert.match(single[1][0], /devstack\.sh$/);
  assert.equal(single[1][2], '--label with spaces');
  assert.match(dual[1][0], /manual-review[/\\]review\.sh$/);
  assert.throws(() => reviewCommand(['single-seeded', 'reset']));
  assert.throws(() => reviewCommand(['unrelated', 'down']));
  assert.throws(() => reviewCommand(['dual-ownership', 'review', '--ignored-option']));
});

test('both profiles share a versioned scoped report and redact URL credentials', () => {
  const out = mkdtempSync(join(tmpdir(), 'carbon-review-report-'));
  try {
    for (const profile of ['single-seeded', 'dual-ownership']) {
      const report = writeReviewReport({ out, profile, base: 'http://user:private@localhost:8080/path?token=private#secret', driver: {driverVersion:'fixture'}, checks: [{name:'check',status:'passed'}] });
      assert.equal(report.schemaVersion, 1);
      assert.equal(report.scope, 'ui-review');
      assert.equal(report.status, 'passed');
      assert.doesNotMatch(readFileSync(join(out, 'review.json'), 'utf8'), /private|token|secret/);
    }
    assert.equal(writeReviewReport({out, profile:'single-seeded',base:'http://localhost',checks:[]}).status, 'incomplete');
    assert.equal(writeReviewReport({out, profile:'single-seeded',base:'http://localhost',checks:[{status:'failed'}]}).status, 'failed');
  } finally { rmSync(out, { recursive:true, force:true }); }
});
