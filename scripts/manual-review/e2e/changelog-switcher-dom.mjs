// Bounded real-browser DOM/navigation collector for scripts/changelog_browser_check.py.
// Uses this package's pinned Playwright (1.61.0). Every assertion stays in the
// Python caller; this helper only observes the built site. One page per request
// keeps a pending switch navigation from interrupting the next route load.
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
try {
  // No executablePath override: use the exact browser installed by this
  // package's pinned Playwright, with its default headless launch.
  browser = await chromium.launch({
    headless: true,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  for (const request of requests) {
    if (Object.prototype.hasOwnProperty.call(results, request.key)) continue;
    const page = await context.newPage();
    try {
      await page.setViewportSize({ width: request.width, height: 900 });
      const response = await page.goto(origin + request.path, {
        waitUntil: "domcontentloaded",
        timeout: 30000,
      });
      const entry = {
        html: await page.evaluate(() => document.documentElement.outerHTML),
        // textContent (not innerText): the channel line has CSS capitalize,
        // which would otherwise disguise the exact rendered version string.
        banner: await page.evaluate(
          () => document.querySelector(".version-banner")?.textContent || ""),
        links: await page.evaluate(() => Array.from(document.querySelectorAll("a[href]"))
          .map((a) => ({ href: a.getAttribute("href"), text: a.textContent || "" }))),
        status: response ? response.status() : null,
        url: page.url(),
      };
      if (request.switchTo) {
        // The banner switcher is an accessible <select> whose onchange assigns
        // the option value to location. Drive that exact control like a user.
        const select = page.locator("select[data-version-switcher]").first();
        if (await select.count() === 0) {
          entry.switchError = "the accessible channel switcher is missing";
        } else if (await select.locator(`option[value="${request.switchTo}"]`).count() === 0) {
          entry.switchError = `switcher has no matching-page option ${request.switchTo}`;
        } else {
          const before = page.url();
          await select.selectOption(request.switchTo);
          await page.waitForURL((url) => String(url) !== before, { timeout: 15000 });
          entry.navigatedUrl = page.url();
        }
      }
      results[request.key] = entry;
    } catch (error) {
      results[request.key] = {
        error: String((error && error.message) || error || "unknown browser error"),
      };
    } finally {
      await page.close();
    }
  }
} finally {
  if (context) await context.close();
  if (browser) await browser.close();
}
await writeFile(output, JSON.stringify(results));
