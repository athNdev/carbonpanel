// SPDX-License-Identifier: MPL-2.0
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';

// Retain the existing locked package location while both review profiles use
// this shared entrypoint. No home-directory module or browser search is used.
const require = createRequire(new URL('../manual-review/e2e/package.json', import.meta.url));
const expectedVersion = require('./package.json').dependencies.playwright;
const root = fileURLToPath(new URL('../../', import.meta.url));

// Owned, disposable cache; an explicit operator override remains supported.
export function reviewBrowserPath() {
  return process.env.PLAYWRIGHT_BROWSERS_PATH || join(root, 'tmp', 'review-browsers', expectedVersion);
}

export function reviewDriver() {
  process.env.PLAYWRIGHT_BROWSERS_PATH = reviewBrowserPath();
  const actual = require('playwright/package.json').version;
  if (actual !== expectedVersion) throw new Error(`Review driver version ${actual}; expected ${expectedVersion}. Run node scripts/review/setup-browser.mjs.`);
  return { chromium: require('playwright').chromium, version: actual };
}

export async function launchReviewBrowser(options = {}) {
  const driver = reviewDriver();
  const executablePath = process.env.REVIEW_CHROME || process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH;
  const browser = await driver.chromium.launch({ headless: true, ...options, ...(executablePath ? { executablePath } : {}) });
  return { browser, metadata: {
    driver: 'playwright', driverVersion: driver.version,
    browserVersion: browser.version(),
    executablePath: executablePath || driver.chromium.executablePath(),
    explicitBrowserOverride: Boolean(executablePath),
    browserCache: reviewBrowserPath(),
    setup: fileURLToPath(new URL('./setup-browser.mjs', import.meta.url))
  } };
}
