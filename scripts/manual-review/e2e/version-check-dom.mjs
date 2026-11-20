// Bounded real-browser DOM collector for scripts/browser_version_check.py.
// Uses this package's pinned Playwright (1.61.0) with ONE default headless
// launch; every assertion stays in the Python caller. No site writes.
import { chromium } from "playwright";
import { writeFile } from "node:fs/promises";

function option(name) {
  const flag = "--" + name;
  const index = process.argv.indexOf(flag);
  if (index < 0 || index + 1 >= process.argv.length) {
    throw new Error("missing option " + flag);
  }
  return process.argv[index + 1];
}

const origin = option("origin").replace(/\/$/, "");
const output = option("out");
const requests = JSON.parse(option("requests"));
if (!Array.isArray(requests) || requests.length === 0) {
  throw new Error("requests must be a non-empty array");
}

const results = {};
let browser;
let context;
let page;
try {
  // No executablePath override: use the exact browser installed by this
  // package's pinned Playwright, with its default headless launch.
  browser = await chromium.launch({
    headless: true,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  page = await context.newPage();
  for (const request of requests) {
    if (Object.prototype.hasOwnProperty.call(results, request.key)) continue;
    try {
      await page.setViewportSize({ width: request.width, height: 900 });
      const response = await page.goto(origin + request.path, {
        waitUntil: "domcontentloaded",
        timeout: 30000,
      });
      const status = response ? response.status() : null;
      const url = page.url();
      const html = await page.evaluate(() => document.documentElement.outerHTML);
      results[request.key] = { html, status, url };
    } catch (error) {
      results[request.key] = {
        error: String((error && error.message) || error || "unknown browser error"),
      };
    }
  }
} finally {
  if (page) await page.close();
  if (context) await context.close();
  if (browser) await browser.close();
}
await writeFile(output, JSON.stringify(results));
