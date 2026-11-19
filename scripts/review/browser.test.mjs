// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { reviewBrowserPath, reviewBrowserVersion, reviewDriver } from './browser.mjs';

test('review browser uses an owned cache and retains explicit overrides', () => {
  const previous = process.env.PLAYWRIGHT_BROWSERS_PATH;
  try {
    delete process.env.PLAYWRIGHT_BROWSERS_PATH;
    const expected = fileURLToPath(new URL('../../tmp/review-browsers/', import.meta.url));
    assert.equal(resolve(reviewBrowserPath()), resolve(expected, reviewBrowserVersion));
    const driver = reviewDriver();
    // reviewDriver() already verifies the installed package against the same
    // authoritative pin; assert the reported metadata too.
    assert.equal(driver.version, reviewBrowserVersion);
    process.env.PLAYWRIGHT_BROWSERS_PATH = 'explicit-browser-cache';
    assert.equal(reviewBrowserPath(), 'explicit-browser-cache');
  } finally {
    if (previous === undefined) delete process.env.PLAYWRIGHT_BROWSERS_PATH;
    else process.env.PLAYWRIGHT_BROWSERS_PATH = previous;
  }
});
