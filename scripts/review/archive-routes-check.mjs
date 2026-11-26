// SPDX-License-Identifier: MPL-2.0
// Normal-route browser evidence for OSS-428 archive portability.
//
// Logs in through the production login page, uses the production layout and
// drives /servers/[id] and /servers/new against a disposable review stack. It
// records refused/unacknowledged export states, authorized ZIP and tar.gz
// downloads with transfer status, filename, bytes and digest, and archive
// previews from both files. It never targets a shared panel or real service.
//
//   REVIEW_BASE=http://127.0.0.1:8099 \
//   PANEL_REVIEW_USER=reviewer PANEL_REVIEW_PASSWORD=... \
//     node scripts/review/archive-routes-check.mjs

import assert from "node:assert/strict";
import crypto from "node:crypto";
import { execFileSync } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { launchReviewBrowser } from "./browser.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../..");
const verifiedHead = execFileSync("git", ["rev-parse", "HEAD"], {
  cwd: repoRoot,
  encoding: "utf8",
}).trim();
const worktreeStatus = execFileSync("git", ["status", "--porcelain"], {
  cwd: repoRoot,
  encoding: "utf8",
}).trim();
const base = (process.env.REVIEW_BASE || "http://127.0.0.1:8099").replace(
  /\/$/,
  "",
);
const user = process.env.PANEL_REVIEW_USER || "reviewer";
const password = process.env.PANEL_REVIEW_PASSWORD || "Review-Panel-2026";
const out = path.resolve(
  process.env.REVIEW_ARCHIVE_OUT ||
    path.join(here, "../../tmp/review-output/archive-routes-live"),
);

const evidence = {
  head: {
    sha: verifiedHead,
    source: "git rev-parse HEAD",
    worktreeDirty: Boolean(worktreeStatus),
    worktreeStatus: worktreeStatus ? worktreeStatus.split("\n") : [],
  },
  scope:
    "Normal /servers/[id] and /servers/new production routes through the disposable review stack. Live-engine round trips remain separate evidence.",
  base: base.replace(/:\d+$/, ":<review-port>"),
  views: [],
  downloads: [],
  previews: [],
  keyboard: [],
  errors: [],
};

const sha256 = (buffer) => crypto.createHash("sha256").update(buffer).digest("hex");

async function setTheme(page, theme) {
  await page.evaluate((value) => {
    document.documentElement.setAttribute("data-carbon-theme", value);
    document.documentElement.classList.toggle("dark", value === "dark");
  }, theme);
  await page.waitForTimeout(350);
}

async function login(page) {
  await page.goto(`${base}/login`, { waitUntil: "domcontentloaded" });
  await page
    .locator('input[placeholder="Enter your username"]')
    .fill(user);
  await page.locator('input[placeholder="Enter your password"]').fill(password);
  await Promise.all([
    page.waitForURL((url) => !String(url).includes("/login"), {
      timeout: 15_000,
    }),
    page.locator('button[type="submit"]').click(),
  ]);
  await page.locator("main").waitFor({ timeout: 15_000 });
}

async function firstServerPath(page) {
  await page.goto(`${base}/servers`, { waitUntil: "domcontentloaded" });
  await page
    .locator('a[href^="/servers/"]')
    .first()
    .waitFor({ timeout: 30_000 });
  await page.waitForTimeout(750);
  const href = await page.evaluate(() => {
    const links = [...document.querySelectorAll('a[href^="/servers/"]')];
    return (
      links
        .map((link) => link.getAttribute("href"))
        .find((value) => value && value !== "/servers/new") || null
    );
  });
  assert.ok(href, "the disposable stack must expose a server detail link");
  return href;
}

async function closeExport(page) {
  if (
    (await page.getByTestId("archive-export-panel").count()) > 0 &&
    (await page.getByTestId("archive-export-panel").isVisible())
  ) {
    await page.keyboard.press("Escape");
    await page
      .getByTestId("archive-export-panel")
      .waitFor({ state: "hidden" });
  }
}

async function openExport(page) {
  const trigger = page.getByTestId("server-export-archive");
  await trigger.focus();
  await page.keyboard.press("Enter");
  evidence.keyboard.push("Enter opened the export modal");
  await page.getByTestId("archive-export-panel").waitFor();
  await page.waitForFunction(
    () => document.activeElement?.getAttribute("aria-label") === "Close modal",
  );
}

async function chooseFormat(page, format) {
  const label = format === "tar.gz" ? "TAR.GZ (.tar.gz)" : "ZIP (.zip)";
  await page.keyboard.press("Tab");
  if (format === "tar.gz") await page.keyboard.press("Tab");
  await page.keyboard.press("Space");
  assert.equal(
    await page.getByRole("radio", { name: label }).getAttribute("aria-checked"),
    "true",
  );
  evidence.keyboard.push(`Space selected ${label}`);
}

async function downloadArchive(page, format, view) {
  const label = format === "tar.gz" ? "TAR.GZ (.tar.gz)" : "ZIP (.zip)";
  await openExport(page);
  await chooseFormat(page, format);
  await page.screenshot({
    path: path.join(out, `detail-export-${format}-${view}.png`),
    fullPage: true,
  });

  assert.equal(
    await page.getByTestId("archive-download").count(),
    0,
    "download is not offered before preparation",
  );
  await page.getByTestId("archive-prepare").click();
  await page
    .getByTestId("archive-export-ready")
    .waitFor({ timeout: 10 * 60 * 1000 });
  const filename = (
    await page.getByTestId("archive-export-ready").textContent()
  ).trim();
  await page.screenshot({
    path: path.join(out, `detail-export-ready-${format}-${view}.png`),
    fullPage: true,
  });

  assert.equal(
    await page.getByTestId("archive-download").isDisabled(),
    true,
    "download must stay disabled until the notice is acknowledged",
  );
  await page
    .getByLabel("I have reviewed this notice before downloading.")
    .check();
  await page.getByTestId("archive-download").focus();
  const downloadPromise = page.waitForEvent("download", {
    timeout: 10 * 60 * 1000,
  });
  await page.keyboard.press("Enter");
  evidence.keyboard.push("Enter authorized the archive download");
  const download = await downloadPromise;
  const suggested = download.suggestedFilename();
  const saved = path.join(out, `${view}-${suggested}`);
  await download.saveAs(saved);
  const bytes = await fs.readFile(saved);
  // Inspect the same authorized session through the browser context so the
  // evidence can state the HTTP status and headers without transferring the
  // archive a second time.
  const response = await page.context().request.fetch(download.url(), {
    method: "HEAD",
  });
  const result = {
    format,
    view,
    status: response.status(),
    url: download.url().replace(base, "<review-base>"),
    filename: suggested,
    byteLength: bytes.byteLength,
    sha256: sha256(bytes),
    contentType: response.headers()["content-type"] || "",
    disposition: response.headers()["content-disposition"] || "",
    declaredLength: Number(response.headers()["content-length"] || 0),
  };
  assert.equal(
    result.declaredLength,
    result.byteLength,
    "the authorized response length matches the downloaded bytes",
  );
  assert.equal(result.status, 200);
  assert.ok(
    result.filename.endsWith(format === "tar.gz" ? ".tar.gz" : ".zip"),
    `download filename ${result.filename} matches ${format}`,
  );
  assert.ok(result.byteLength > 0, "the authorized transfer carries bytes");
  assert.ok(
    result.disposition.includes(suggested),
    "the download response names the current archive filename",
  );
  evidence.downloads.push(result);
  await page
    .getByTestId("archive-export-panel")
    .waitFor({ state: "hidden" });
  return saved;
}

async function previewArchive(page, archivePath, format, view) {
  await page.goto(`${base}/servers/new`, { waitUntil: "domcontentloaded" });
  await page.getByPlaceholder("survival-smp-01").fill("archive-route-fixture");
  const tile = page.getByTestId("create-from-archive-tile");
  await tile.focus();
  await page.keyboard.press("Space");
  evidence.keyboard.push("Space selected the create-from-archive tile");
  await page.locator('[data-testid="archive-file-input"]').setInputFiles(archivePath);
  await page.getByTestId("archive-preview").waitFor({ timeout: 10 * 60 * 1000 });
  const label = format === "tar.gz" ? "TAR.GZ (.tar.gz)" : "ZIP (.zip)";
  await page
    .getByTestId("archive-preview")
    .getByText(label, { exact: true })
    .waitFor();
  await page.screenshot({
    path: path.join(out, `new-preview-${format}-${view}.png`),
    fullPage: true,
  });
  await page
    .getByTestId("archive-preview")
    .screenshot({ path: path.join(out, `new-preview-${format}-${view}-panel.png`) });
  for (let step = 0; step < 4; step++)
    await page.getByRole("button", { name: "Next Step" }).click();
  const create = page.getByRole("button", { name: "Create Server", exact: true });
  await create.waitFor();
  evidence.previews.push({
    format,
    view,
    filename: path.basename(archivePath),
    screenshot: `new-preview-${format}-${view}.png`,
    createEnabled: await create.isEnabled(),
  });
  await page.screenshot({
    path: path.join(out, `new-create-${format}-${view}.png`),
    fullPage: true,
  });
}

let browser;
let runError;
try {
  await fs.mkdir(out, { recursive: true });
  const launched = await launchReviewBrowser({ headless: true });
  browser = launched.browser;
  evidence.browser = launched.metadata;
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1000 },
  });
  page.setDefaultTimeout(60_000);
  page.on("pageerror", (error) => evidence.errors.push(error.message));

  await login(page);
  const detailPath = await firstServerPath(page);

  for (const view of [
    { name: "desktop-dark", width: 1440, height: 1000, theme: "dark" },
    { name: "desktop-light", width: 1440, height: 1000, theme: "light" },
    { name: "narrow-dark", width: 390, height: 900, theme: "dark" },
    { name: "narrow-light", width: 390, height: 900, theme: "light" },
  ]) {
    await page.setViewportSize({ width: view.width, height: view.height });
    await page.goto(`${base}${detailPath}`, { waitUntil: "domcontentloaded" });
    await page.getByTestId("server-export-archive").waitFor({
      timeout: 30_000,
    });
    await setTheme(page, view.theme);
    evidence.views.push({
      route: "/servers/[id]",
      name: view.name,
      width: view.width,
      height: view.height,
      theme: view.theme,
    });

    // A running server is refused rather than silently stopped.
    if ((await page.getByRole("button", { name: "Stop", exact: true }).count()) > 0) {
      await openExport(page);
      await page.getByTestId("archive-export-running").waitFor();
      assert.equal(
        await page.getByTestId("archive-prepare").isDisabled(),
        true,
        "a live server must not prepare an export",
      );
      await page.screenshot({
        path: path.join(out, `detail-export-refused-${view.name}.png`),
        fullPage: true,
      });
      await closeExport(page);

      await page.getByRole("button", { name: "Stop", exact: true }).click();
      await page
        .getByRole("button", { name: "Start Server", exact: true })
        .waitFor({ timeout: 5 * 60 * 1000 });
    }

    await page.screenshot({
      path: path.join(out, `detail-${view.name}.png`),
      fullPage: true,
    });
  }

  const zipPath = await downloadArchive(page, "zip", "desktop-light");
  const tarPath = await downloadArchive(page, "tar.gz", "desktop-light");

  for (const view of [
    { name: "desktop-dark", width: 1440, height: 1000, theme: "dark" },
    { name: "desktop-light", width: 1440, height: 1000, theme: "light" },
    { name: "narrow-dark", width: 390, height: 900, theme: "dark" },
    { name: "narrow-light", width: 390, height: 900, theme: "light" },
  ]) {
    await page.setViewportSize({ width: view.width, height: view.height });
    await setTheme(page, view.theme);
    evidence.views.push({
      route: "/servers/new",
      name: view.name,
      width: view.width,
      height: view.height,
      theme: view.theme,
    });
    await previewArchive(page, zipPath, "zip", view.name);
    await previewArchive(page, tarPath, "tar.gz", view.name);
  }

  assert.deepEqual(evidence.errors, [], "the production routes must not throw");
  await page.screenshot({ path: path.join(out, "result.png") });
} catch (error) {
  runError = error;
} finally {
  const cleanupErrors = [];
  for (const [name, action] of [["browser", () => browser?.close()]]) {
    try {
      await action();
    } catch (error) {
      cleanupErrors.push({ name, error });
    }
  }
  if (cleanupErrors.length > 0) {
    const details = cleanupErrors
      .map(({ name, error }) => `${name}: ${error?.message ?? error}`)
      .join("; ");
    if (runError) {
      runError.message = `${runError.message}\nCleanup also failed: ${details}`;
      runError.cause = runError.cause ?? cleanupErrors[0].error;
    } else {
      runError = new AggregateError(
        cleanupErrors.map(({ error }) => error),
        `Fixture cleanup failed: ${details}`,
      );
    }
  }
}
await fs.writeFile(
  path.join(out, "results.json"),
  JSON.stringify(evidence, null, 2),
);
if (runError) throw runError;
console.log(
  "PASS: normal /servers/[id] and /servers/new routes, login/layout, refused and unacknowledged export, authorized ZIP and tar.gz transfers, and both archive previews",
);
