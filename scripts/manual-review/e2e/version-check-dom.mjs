// Bounded real-browser DOM collector for scripts/browser_version_check.py.
// Uses this package's pinned Playwright (1.61.0) with ONE browser session;
// every assertion stays in the Python caller. No network or site writes.
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

const executablePath = process.env.CHROME_PATH || undefined;
const browser = await chromium.launch({
  headless: true,
  ...(executablePath ? { executablePath } : {}),
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await context.newPage();
const results = {};
try {
  for (const request of requests) {
    if (Object.prototype.hasOwnProperty.call(results, request.key)) continue;
    try {
      await page.setViewportSize({ width: request.width, height: 900 });
      await page.goto(origin + request.path, {
        waitUntil: "domcontentloaded",
        timeout: 30000,
      });
      const html = await page.evaluate(() => document.documentElement.outerHTML);
      results[request.key] = html
        ? { html }
        : { error: "browser returned an empty document DOM" };
    } catch (error) {
      results[request.key] = {
        error: String((error && error.message) || error || "unknown browser error"),
      };
    }
  }
} finally {
  await page.close();
  await context.close();
  await browser.close();
}
await writeFile(output, JSON.stringify(results));
