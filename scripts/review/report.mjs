// SPDX-License-Identifier: MPL-2.0
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

// Stable machine-readable envelope shared by both profiles. Existing detailed
// Markdown/legacy findings remain available beside this authoritative summary.
export function writeReviewReport({ out, profile, base, driver, checks }) {
  const url = new URL(base);
  url.username = ''; url.password = ''; url.search = ''; url.hash = '';
  const status = checks.length === 0 ? 'incomplete' : checks.some(item => item.status === 'failed') ? 'failed' : checks.some(item => item.status === 'review-required') ? 'review-required' : 'passed';
  const report = { schemaVersion: 1, scope: 'ui-review', profile, base: url.toString(), at: new Date().toISOString(), status, driver, checks };
  writeFileSync(join(out, 'review.json'), JSON.stringify(report, null, 2) + '\n');
  return report;
}
