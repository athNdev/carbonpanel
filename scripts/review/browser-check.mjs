// SPDX-License-Identifier: MPL-2.0
import assert from 'node:assert/strict';
import { launchReviewBrowser } from './browser.mjs';

const { browser, metadata } = await launchReviewBrowser();
try {
  const page = await browser.newPage();
  await page.goto('data:text/html,<title>review-driver-check</title><p>ready</p>');
  assert.equal(await page.title(), 'review-driver-check');
  assert.ok(metadata.driverVersion && metadata.browserVersion && metadata.executablePath);
  console.log(JSON.stringify({ check: 'review-browser', ...metadata }));
} finally { await browser.close(); }
