// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { reviewBrowserPath, reviewDriver } from './browser.mjs';

test('review browser uses an owned cache and retains explicit overrides', () => {
  const previous = process.env.PLAYWRIGHT_BROWSERS_PATH;
  try {
    delete process.env.PLAYWRIGHT_BROWSERS_PATH;
    const expected = fileURLToPath(new URL('../../tmp/review-browsers/1.49.1', import.meta.url));
    assert.equal(resolve(reviewBrowserPath()), resolve(expected));
    const driver = reviewDriver();
    assert.equal(driver.version, '1.49.1');
    process.env.PLAYWRIGHT_BROWSERS_PATH = 'explicit-browser-cache';
    assert.equal(reviewBrowserPath(), 'explicit-browser-cache');
  } finally {
    if (previous === undefined) delete process.env.PLAYWRIGHT_BROWSERS_PATH;
    else process.env.PLAYWRIGHT_BROWSERS_PATH = previous;
  }
});
