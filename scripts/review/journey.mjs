// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// A small browser journey runner for manual review.
//
// It drives a real Chromium against a running panel, performs a scripted set
// of steps, asserts what a person would assert by looking, and writes one
// screenshot per named step plus a Markdown report. The point is evidence: the
// report and images can be attached to a review without re-running anything.
//
//	REVIEW_MODULES=~/.cache/review/node_modules \
//	  node scripts/review/journey.mjs --plan scripts/review/plans/console.json \
//	  --base http://127.0.0.1:8080 --out /tmp/review
//
// Credentials are read from the environment only (PANEL_REVIEW_USER /
// PANEL_REVIEW_PASSWORD) and are never written to the report.

import { createRequire } from "node:module";
import {
  mkdirSync,
  readFileSync,
  writeFileSync,
  existsSync,
  readdirSync,
} from "node:fs";
import { homedir } from "node:os";
import { dirname, join, resolve } from "node:path";

function parseArgs(argv) {
  const args = {};
  for (let i = 2; i < argv.length; i += 2) {
    const key = argv[i];
    if (!key.startsWith("--")) throw new Error(`unexpected argument ${key}`);
    args[key.slice(2)] = argv[i + 1];
  }
  return args;
}

function loadPlaywright() {
  const candidates = [
    process.env.REVIEW_MODULES,
    join(homedir(), "review", "node_modules"),
    join(process.cwd(), "node_modules"),
  ].filter(Boolean);

  for (const dir of candidates) {
    if (!existsSync(join(dir, "playwright-core"))) continue;
    const require = createRequire(join(dir, "noop.cjs"));
    return require("playwright-core");
  }
  throw new Error(
    "playwright-core not found. Install it (npm i playwright-core) and set REVIEW_MODULES to its node_modules directory.",
  );
}

function findChromium() {
  if (process.env.REVIEW_CHROME) return process.env.REVIEW_CHROME;
  const cache = join(homedir(), ".cache", "ms-playwright");
  if (!existsSync(cache)) return undefined;
  const builds = readdirSync(cache)
    .filter((name) => name.startsWith("chromium-"))
    .sort()
    .reverse();
  for (const build of builds) {
    const candidate = join(cache, build, "chrome-linux", "chrome");
    if (existsSync(candidate)) return candidate;
  }
  return undefined;
}

async function main() {
  const args = parseArgs(process.argv);
  if (!args.plan || !args.base) {
    throw new Error("--plan and --base are required");
  }

  const plan = JSON.parse(readFileSync(resolve(args.plan), "utf8"));
  const out = resolve(args.out ?? "review-out");
  mkdirSync(out, { recursive: true });

  const { chromium } = loadPlaywright();
  const browser = await chromium.launch({
    executablePath: findChromium(),
    args: ["--no-sandbox"],
  });
  const context = await browser.newContext({
    viewport: plan.viewport ?? { width: 1440, height: 900 },
    ignoreHTTPSErrors: true,
    deviceScaleFactor: plan.deviceScaleFactor ?? 1,
  });
  const page = await context.newPage();

  const results = [];
  const consoleErrors = [];
  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.push(msg.text());
  });
  page.on("pageerror", (err) => consoleErrors.push(String(err)));

  // A failed request is a finding even when the page looks fine, and the
  // browser console only says "400" without saying which call.
  const failedResponses = [];
  page.on("response", (response) => {
    if (response.status() >= 400) {
      failedResponses.push(
        `${response.status()} ${response.request().method()} ${response.url()}`,
      );
    }
  });

  const base = args.base.replace(/\/$/, "");
  const record = (name, ok, detail) => results.push({ name, ok, detail });

  async function step(kind, spec) {
    const label =
      spec.name ??
      `${kind}:${Object.values(spec).find((v) => typeof v === "string") ?? ""}`;
    try {
      switch (kind) {
        case "goto":
          // Not networkidle: a page that fetches an unreachable index keeps
          // a request pending forever, and the navigation then times out on a
          // page that rendered fine. Use explicit waitFor steps instead.
          await page.goto(base + spec.goto, {
            waitUntil: "domcontentloaded",
            timeout: 30000,
          });
          break;
        case "select":
          await page
            .locator(spec.select)
            .first()
            .selectOption(spec.value, { timeout: 15000 });
          break;
        case "click":
          await page.locator(spec.click).first().click({ timeout: 15000 });
          break;
        case "fill":
          await page
            .locator(spec.fill)
            .first()
            .fill(spec.value ?? "", { timeout: 15000 });
          break;
        case "press":
          await page.keyboard.press(spec.press);
          break;
        case "waitFor":
          await page
            .locator(spec.waitFor)
            .first()
            .waitFor({ timeout: spec.timeout ?? 20000 });
          break;
        case "wait":
          await page.waitForTimeout(spec.wait);
          break;
        case "assertVisible":
          await page
            .locator(spec.assertVisible)
            .first()
            .waitFor({
              state: "visible",
              timeout: spec.timeout ?? 15000,
            });
          break;
        case "assertHidden":
          await page
            .locator(spec.assertHidden)
            .first()
            .waitFor({
              state: "hidden",
              timeout: spec.timeout ?? 15000,
            });
          break;
        case "assertText": {
          const text = await page
            .locator(spec.assertText)
            .first()
            .innerText({ timeout: 15000 });
          if (!text.includes(spec.contains ?? "")) {
            throw new Error(
              `text ${JSON.stringify(text)} does not contain ${JSON.stringify(spec.contains)}`,
            );
          }
          break;
        }
        case "assertNoScrollbar": {
          // A tab strip or toolbar must never grow a scrollbar: it is the
          // symptom that makes the console feel jittery as bars appear
          // and disappear under the pointer.
          const box = await page
            .locator(spec.assertNoScrollbar)
            .first()
            .evaluate((el) => ({
              overflowY: getComputedStyle(el).overflowY,
              scrollHeight: el.scrollHeight,
              clientHeight: el.clientHeight,
            }));
          const vertical =
            box.overflowY === "auto" || box.overflowY === "scroll";
          if (vertical && box.scrollHeight > box.clientHeight + 1) {
            throw new Error(
              `${spec.assertNoScrollbar} scrolls vertically (${box.scrollHeight} > ${box.clientHeight})`,
            );
          }
          break;
        }
        case "screenshot":
          break;
        default:
          throw new Error(`unknown step ${kind}`);
      }

      if (spec.screenshot) {
        // Settle first. A screenshot taken while the tab indicator or a
        // stagger animation is still moving shows a state no person ever
        // sees, and the review then argues about a phantom bug.
        await page
          .evaluate(() =>
            Promise.race([
              Promise.all(
                document.getAnimations().map((a) => a.finished.catch(() => {})),
              ),
              new Promise((resolve) => setTimeout(resolve, 1500)),
            ]),
          )
          .catch(() => {});
        await page.waitForTimeout(120);
        const file = join(out, `${spec.screenshot}.png`);
        if (spec.element) {
          // Cropped capture: scroll the element into view, measure its box,
          // then clip a viewport screenshot to it. A viewport-clipped shot
          // renders through the same path as a full page (locator-level
          // element shots have rendered CSS-var-themed content wrong).
          const target = page.locator(spec.element).first();
          // Full-height page crops start at the top of the scroll container and
          // must not be scrolled: scrollIntoViewIfNeeded aligns a too-tall
          // element under the fixed 48px app header, so the capture shows a
          // strip of chrome and a half-hidden page header. Plans set
          // scroll:false for those and capture from the current (top) scroll.
          if (spec.scroll !== false) {
            await target.scrollIntoViewIfNeeded({ timeout: 15000 });
          }
          const box = await target.boundingBox({ timeout: 15000 });
          if (!box) throw new Error(`no bounding box for ${spec.element}`);
          const vp = page.viewportSize() ?? { width: 1440, height: 900 };
          const clip = {
            x: Math.max(0, Math.round(box.x)),
            y: Math.max(0, Math.round(box.y)),
            width: Math.min(
              Math.round(box.width),
              vp.width - Math.max(0, Math.round(box.x)),
            ),
            height: Math.min(
              Math.round(box.height),
              vp.height - Math.max(0, Math.round(box.y)),
              spec.maxHeight ?? Infinity,
            ),
          };
          if (clip.width < 2 || clip.height < 2)
            throw new Error(`empty clip for ${spec.element}`);
          await page.screenshot({ path: file, clip });
        } else {
          await page.screenshot({
            path: file,
            fullPage: spec.fullPage ?? false,
          });
        }
        record(label, true, `screenshot ${file}`);
      } else {
        record(label, true, "");
      }
    } catch (error) {
      record(
        label,
        false,
        error instanceof Error ? error.message : String(error),
      );
      if (spec.screenshot) {
        const file = join(out, `FAILED-${spec.screenshot}.png`);
        await page.screenshot({ path: file, fullPage: true }).catch(() => {});
      }
    }
  }

  // A brand-new review stack shows first-user setup instead of a login form.
  // Bootstrap only runs when that form is actually on screen, so the same plan
  // works against a fresh stack and a seeded one.
  const bootstrap = plan.bootstrap;
  if (bootstrap) {
    // Same credentials as the login step: one account, one place to set it.
    const setupUser = process.env[bootstrap.usernameEnv ?? "PANEL_REVIEW_USER"];
    const setupPass =
      process.env[bootstrap.passwordEnv ?? "PANEL_REVIEW_PASSWORD"];
    if (!setupUser || !setupPass) {
      throw new Error(
        "PANEL_REVIEW_USER / PANEL_REVIEW_PASSWORD must be set to bootstrap the review panel",
      );
    }
    await step("goto", {
      goto: bootstrap.path ?? "/login",
      name: "open setup",
    });
    const hasLoginForm =
      (await page.locator(bootstrap.detectSelector).count()) > 0;
    if (hasLoginForm) {
      record("bootstrap", true, "already set up; skipped");
    } else {
      await step("fill", {
        fill: bootstrap.usernameSelector,
        value: setupUser,
        name: "choose admin username",
      });
      await step("fill", {
        fill: bootstrap.passwordSelector,
        value: setupPass,
        name: "choose admin password",
      });
      await step("fill", {
        fill: bootstrap.confirmSelector,
        value: setupPass,
        name: "confirm admin password",
      });
      await step("click", {
        click: bootstrap.submitSelector,
        name: "submit setup",
      });
      await step("waitFor", {
        waitFor: bootstrap.successSelector,
        name: "admin account created",
      });
    }
  }

  const login = plan.login;
  if (login) {
    const username = process.env[login.usernameEnv ?? "PANEL_REVIEW_USER"];
    const password = process.env[login.passwordEnv ?? "PANEL_REVIEW_PASSWORD"];
    if (!username || !password) {
      throw new Error(
        `${login.usernameEnv ?? "PANEL_REVIEW_USER"} / ${login.passwordEnv ?? "PANEL_REVIEW_PASSWORD"} must be set`,
      );
    }
    await step("goto", { goto: login.path ?? "/login", name: "open login" });
    await step("fill", {
      fill: login.usernameSelector,
      value: username,
      name: "fill username",
    });
    await step("fill", {
      fill: login.passwordSelector,
      value: password,
      name: "fill password",
    });
    await step("click", { click: login.submitSelector, name: "submit login" });
    await step("waitFor", {
      waitFor: login.successSelector,
      name: "logged in",
    });
  }

  for (const spec of plan.steps ?? []) {
    if (spec.screenshot && Object.keys(spec).length === 1) {
      await step("screenshot", spec);
      continue;
    }
    const kind = Object.keys(spec).find((key) =>
      [
        "goto",
        "click",
        "fill",
        "select",
        "press",
        "waitFor",
        "wait",
        "assertVisible",
        "assertHidden",
        "assertText",
        "assertNoScrollbar",
        "screenshot",
      ].includes(key),
    );
    if (!kind) throw new Error(`step has no action: ${JSON.stringify(spec)}`);
    await step(kind, spec);
  }

  await browser.close();

  const failed = results.filter((r) => !r.ok);
  const lines = [
    `# Manual review: ${plan.title ?? plan.name ?? "journey"}`,
    "",
    `Base: \`${base}\``,
    `Result: ${failed.length === 0 ? "PASS" : `FAIL (${failed.length})`}`,
    "",
    "| Step | Result | Detail |",
    "| --- | --- | --- |",
    ...results.map(
      (r) =>
        `| ${r.name} | ${r.ok ? "pass" : "**fail**"} | ${r.detail.replace(/\|/g, "\\|")} |`,
    ),
  ];
  if (failedResponses.length > 0) {
    lines.push(
      "",
      "## Failed requests",
      "",
      "```",
      ...failedResponses.slice(0, 40),
      "```",
    );
  }
  if (consoleErrors.length > 0) {
    lines.push(
      "",
      "## Console errors",
      "",
      "```",
      ...consoleErrors.slice(0, 40),
      "```",
    );
  }
  const report = join(out, "report.md");
  writeFileSync(report, lines.join("\n") + "\n", "utf8");

  process.stdout.write(lines.join("\n") + "\n");
  process.exit(failed.length === 0 ? 0 : 1);
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : error);
  process.exit(2);
});
