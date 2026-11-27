#!/usr/bin/env node
// SPDX-License-Identifier: MPL-2.0
// Hosted production-UI half of the server-portability acceptance rehearsal.
//
// This driver is only started by scripts/qualify-server-archive-hosted.sh inside
// the disposable GitHub-hosted job. It logs in to the candidate controller with
// a throwaway local administrator, drives the real SvelteKit /servers/[id] and
// /servers/new routes, downloads the archive the UI prepared, uploads that same
// file back through the create-from-archive wizard, and records what the browser
// actually observed.
//
// It deliberately does not verify Minecraft bytes, engine state or cleanup: the
// Python rehearsal owns those checks, reads the downloaded file from disk, and
// refuses any record whose head, identifiers or hashes disagree. Keeping the two
// halves separate makes this script testable without a browser or Docker.
//
//   ARCHIVE_UI_USER=<fixture admin> ARCHIVE_UI_PASSWORD=<throwaway> \
//   PLAYWRIGHT_BROWSERS_PATH=<pinned cache> \
//     node scripts/qualify-archive-ui-hosted.mjs \
//       --plan <plan.json> --out <evidence/ui> --phase export
import assert from "node:assert/strict";
import crypto from "node:crypto";
import { execFileSync } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "..");

export const PLAN_SCHEMA = "archive-ui-acceptance-plan-v1";
export const RECORD_SCHEMA = "archive-ui-acceptance-run-v1";
export const PHASES = Object.freeze(["refusal", "export", "import"]);
export const FORMATS = Object.freeze(["zip", "tar.gz"]);
const FORMAT_LABELS = Object.freeze({
  zip: "ZIP (.zip)",
  "tar.gz": "TAR.GZ (.tar.gz)",
});
const LOOPBACK_HOSTS = new Set(["127.0.0.1", "[::1]", "localhost"]);

const sha256 = (buffer) =>
  crypto.createHash("sha256").update(buffer).digest("hex");

export function formatLabel(format) {
  const label = FORMAT_LABELS[format];
  if (!label) throw new Error(`unsupported archive format: ${format}`);
  return label;
}

export function assertLoopbackBase(raw) {
  if (typeof raw !== "string" || raw.length === 0) {
    throw new Error("controller base URL is required");
  }
  let url;
  try {
    url = new URL(raw);
  } catch {
    throw new Error(`controller base URL is invalid: ${raw}`);
  }
  if (url.protocol !== "http:") {
    throw new Error(`controller base URL must be plain http: ${raw}`);
  }
  if (!LOOPBACK_HOSTS.has(url.hostname)) {
    throw new Error(`controller base URL must be loopback, not ${url.hostname}`);
  }
  if (url.username || url.password) {
    throw new Error("controller base URL must not carry credentials");
  }
  if (url.pathname !== "/" || url.search || url.hash) {
    throw new Error(`controller base URL must be a bare origin: ${raw}`);
  }
  return url.origin;
}

export function safeArtifactName(suggested, fallback) {
  const base = path.basename(String(suggested ?? ""));
  const cleaned = base.replace(/[^A-Za-z0-9._-]/g, "_");
  if (cleaned !== "" && cleaned !== "." && cleaned !== "..") return cleaned;
  if (typeof fallback !== "string" || fallback === "") {
    throw new Error("artifact needs a safe fallback name");
  }
  return fallback;
}

export function formatFromFilename(name) {
  const lower = String(name ?? "").toLowerCase();
  if (lower.endsWith(".tar.gz")) return "tar.gz";
  if (lower.endsWith(".zip")) return "zip";
  return null;
}

// The product creates servers with canonical UUIDs (uuid.New().String()) and
// the detail route is /servers/[id]. Archive creation has no setup tasks, so
// /servers/new navigates straight to that route; the URL is the stable source
// of the created id.
const SERVER_ID_PATTERN =
  /^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/i;

export function serverIdFromUrl(raw) {
  let pathname;
  try {
    pathname =
      raw instanceof URL
        ? raw.pathname
        : new URL(String(raw ?? ""), "http://127.0.0.1").pathname;
  } catch {
    return null;
  }
  const match = /^\/servers\/([^/]+)\/?$/.exec(pathname);
  if (!match) return null;
  let decoded;
  try {
    decoded = decodeURIComponent(match[1]);
  } catch {
    return null;
  }
  return SERVER_ID_PATTERN.test(decoded) ? decoded.toLowerCase() : null;
}

function nonEmptyString(value, label) {
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`${label} must be a non-empty string`);
  }
  return value.trim();
}

function portNumber(value, label) {
  const port = Number(value);
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`${label} must be a TCP port`);
  }
  return port;
}

function optionalPositive(value, fallback, label) {
  if (value === undefined || value === null) return fallback;
  const number = Number(value);
  if (!Number.isFinite(number) || number < 1000) {
    throw new Error(`${label} must be at least one second`);
  }
  return number;
}

export function parsePlan(raw) {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    throw new Error("UI acceptance plan must be an object");
  }
  if (raw.schema !== PLAN_SCHEMA) {
    throw new Error(`UI acceptance plan schema is ${raw.schema}, want ${PLAN_SCHEMA}`);
  }
  const head = nonEmptyString(raw.head, "plan.head");
  if (!/^[0-9a-f]{40}$/.test(head)) {
    throw new Error("plan.head must be a full 40-character commit");
  }
  const phase = nonEmptyString(raw.phase, "plan.phase");
  if (!PHASES.includes(phase)) {
    throw new Error(`plan.phase is ${phase}, want one of ${PHASES.join(", ")}`);
  }
  const base = assertLoopbackBase(raw.base);
  const sourceId = nonEmptyString(raw.source?.serverId, "plan.source.serverId");
  const timeouts = {
    routeMs: optionalPositive(raw.timeouts?.routeMs, 60_000, "plan.timeouts.routeMs"),
    exportMs: optionalPositive(raw.timeouts?.exportMs, 600_000, "plan.timeouts.exportMs"),
    previewMs: optionalPositive(raw.timeouts?.previewMs, 600_000, "plan.timeouts.previewMs"),
    createMs: optionalPositive(raw.timeouts?.createMs, 600_000, "plan.timeouts.createMs"),
  };
  const plan = { head, phase, base, source: { serverId: sourceId }, timeouts };

  if (phase === "import") {
    plan.remoteNode = {
      id: nonEmptyString(raw.remoteNode?.id, "plan.remoteNode.id"),
      name: nonEmptyString(raw.remoteNode?.name, "plan.remoteNode.name"),
    };
    if (!Array.isArray(raw.imports) || raw.imports.length !== FORMATS.length) {
      throw new Error("plan.imports must name ZIP and tar.gz");
    }
    const seen = new Set();
    plan.imports = raw.imports.map((entry, index) => {
      const label = `plan.imports[${index}]`;
      if (!entry || typeof entry !== "object") throw new Error(`${label} must be an object`);
      const format = nonEmptyString(entry.format, `${label}.format`);
      if (!FORMATS.includes(format)) throw new Error(`${label}.format is unsupported`);
      if (seen.has(format)) throw new Error(`${label}.format is duplicated`);
      seen.add(format);
      const file = nonEmptyString(entry.file, `${label}.file`);
      if (path.basename(file) !== file || file === "." || file === "..") {
        throw new Error(`${label}.file must be a plain evidence filename`);
      }
      const expected = nonEmptyString(entry.expectedLabel, `${label}.expectedLabel`);
      if (expected !== formatLabel(format)) {
        throw new Error(`${label}.expectedLabel does not match ${format}`);
      }
      return {
        format,
        file,
        expectedLabel: expected,
        name: nonEmptyString(entry.name, `${label}.name`),
        port: portNumber(entry.port, `${label}.port`),
      };
    });
    if (new Set(plan.imports.map((entry) => entry.port)).size !== plan.imports.length) {
      throw new Error("plan.imports ports must be distinct");
    }
    plan.imports.sort((left, right) => FORMATS.indexOf(left.format) - FORMATS.indexOf(right.format));
  } else if (phase === "export") {
    if (!Array.isArray(raw.exports) || raw.exports.length !== FORMATS.length) {
      throw new Error("plan.exports must name ZIP and tar.gz");
    }
    const seen = new Set();
    plan.exports = raw.exports.map((entry, index) => {
      const label = `plan.exports[${index}]`;
      if (!entry || typeof entry !== "object") throw new Error(`${label} must be an object`);
      const format = nonEmptyString(entry.format, `${label}.format`);
      if (!FORMATS.includes(format)) throw new Error(`${label}.format is unsupported`);
      if (seen.has(format)) throw new Error(`${label}.format is duplicated`);
      seen.add(format);
      const expected = nonEmptyString(entry.expectedLabel, `${label}.expectedLabel`);
      if (expected !== formatLabel(format)) {
        throw new Error(`${label}.expectedLabel does not match ${format}`);
      }
      return { format, expectedLabel: expected };
    });
    plan.exports.sort((left, right) => FORMATS.indexOf(left.format) - FORMATS.indexOf(right.format));
  }
  return plan;
}

export function readHead(expected, cwd = repoRoot) {
  const observed = execFileSync("git", ["rev-parse", "HEAD"], {
    cwd,
    encoding: "utf8",
  }).trim();
  const status = execFileSync("git", ["status", "--porcelain", "--untracked-files=no"], {
    cwd,
    encoding: "utf8",
  }).trim();
  return {
    expected,
    observed,
    worktreeClean: status === "",
    match: observed === expected,
  };
}

export function artifactPath(outDir, file) {
  const resolved = path.resolve(outDir, file);
  const root = path.resolve(outDir);
  if (resolved !== root && !resolved.startsWith(root + path.sep)) {
    throw new Error(`evidence file escapes the output directory: ${file}`);
  }
  return resolved;
}

export function planArtifactPath(outDir, file) {
  if (path.basename(file) !== file) {
    throw new Error(`plan artifact must be a plain filename: ${file}`);
  }
  return artifactPath(outDir, file);
}

function normalizeText(value) {
  return String(value ?? "")
    .replace(/\s+/g, " ")
    .trim();
}

export function previewFacts(text) {
  const normalized = normalizeText(text);
  return {
    text: normalized,
    looksLikePreview: normalized.includes("Archive preview"),
    saysNoFindings: normalized.includes("No compatibility findings."),
    exporterMentioned: /exporter\s+\S+/i.test(normalized),
  };
}

async function setTheme(page, theme) {
  await page.evaluate((value) => {
    document.documentElement.setAttribute("data-carbon-theme", value);
    document.documentElement.classList.toggle("dark", value === "dark");
  }, theme);
  await page.waitForTimeout(150);
}

async function capture(page, outDir, name, viewport, theme, screenshots, fullPage = false) {
  await page.setViewportSize(viewport);
  await setTheme(page, theme);
  const file = safeArtifactName(`${name}.png`, `${name}.png`);
  const target = artifactPath(outDir, file);
  await page.screenshot({ path: target, fullPage });
  screenshots.push({
    name,
    file,
    viewport: { width: viewport.width, height: viewport.height },
    theme,
  });
  return file;
}

async function login(page, base, credentials, timeouts) {
  await page.goto(`${base}/login`, { waitUntil: "domcontentloaded" });
  const username = page.locator('input[placeholder="Enter your username"]');
  const password = page.locator('input[placeholder="Enter your password"]');
  await username.waitFor({ timeout: timeouts.routeMs });
  await username.fill(credentials.username);
  await password.fill(credentials.password);
  await page.locator('button[type="submit"]').click();
  await page.locator('a[href^="/servers/"]').first().waitFor({ timeout: timeouts.routeMs });
}

async function openDetail(page, plan) {
  await page.goto(`${plan.base}/servers/${encodeURIComponent(plan.source.serverId)}`, {
    waitUntil: "domcontentloaded",
  });
  await page.getByTestId("server-export-archive").waitFor({ timeout: plan.timeouts.routeMs });
}

async function openExportModal(page) {
  await page.getByTestId("server-export-archive").click();
  await page.getByTestId("archive-export-panel").waitFor({ timeout: 15_000 });
}

async function refusalPhase(page, plan, outDir, record) {
  await openDetail(page, plan);
  await openExportModal(page);

  const warning = page.getByTestId("archive-export-running");
  await warning.waitFor({ timeout: 15_000 });
  const warningVisible = await warning.isVisible();
  const prepareDisabled = await page.getByTestId("archive-prepare").isDisabled();
  const radios = page.getByTestId("archive-export-formats").getByRole("radio");
  const formatCount = await radios.count();
  const formatsDisabled = [];
  for (let index = 0; index < formatCount; index += 1) {
    formatsDisabled.push(await radios.nth(index).isDisabled());
  }

  let exportRequests = 0;
  const countExport = (request) => {
    if (request.url().includes("/carbonpanel.v1.ServerService/ExportServerArchive")) {
      exportRequests += 1;
    }
  };
  page.on("request", countExport);
  await page.getByTestId("archive-prepare").click({ force: true }).catch(() => {});
  await page.waitForTimeout(300);
  page.off("request", countExport);
  const readyVisible = await page
    .getByTestId("archive-export-ready")
    .isVisible()
    .catch(() => false);

  await capture(
    page,
    outDir,
    "refusal-running-export",
    { width: 1440, height: 900 },
    "dark",
    record.screenshots,
  );

  await page.keyboard.press("Escape");
  await page.getByTestId("archive-export-panel").waitFor({ state: "hidden" });

  record.refusal = {
    warningVisible,
    prepareDisabled,
    formatCount,
    formatsDisabled,
    exportRequests,
    readyVisible,
  };
  assert.equal(warningVisible, true, "running warning must be visible");
  assert.equal(prepareDisabled, true, "a running server must disable Prepare export");
  assert.equal(formatCount, 2, "both archive formats must be offered");
  assert.deepEqual(formatsDisabled, [true, true], "a running server must disable both formats");
  assert.equal(exportRequests, 0, "a disabled Prepare control must not call the export RPC");
  assert.equal(readyVisible, false, "a refused preparation must not produce a ready archive");
}

async function prepareExport(page, plan, entry) {
  const radio = page.getByRole("radio", { name: entry.expectedLabel });
  await radio.waitFor({ timeout: 15_000 });
  await radio.click();
  assert.equal(
    await radio.getAttribute("aria-checked"),
    "true",
    `${entry.format}: the format radio must be selected`,
  );

  await page.getByTestId("archive-prepare").click();
  await page.getByTestId("archive-export-ready").waitFor({
    timeout: plan.timeouts.exportMs,
  });
}

async function downloadPrepared(page, plan, entry, outDir, record) {
  const downloadButton = page.getByTestId("archive-download");
  assert.equal(
    await downloadButton.isDisabled(),
    true,
    `${entry.format}: download must wait for the disclosure acknowledgement`,
  );
  const acknowledgement = page.getByLabel(
    "I have reviewed this notice before downloading.",
  );
  await acknowledgement.check();
  assert.equal(
    await acknowledgement.isChecked(),
    true,
    `${entry.format}: the disclosure acknowledgement must be set`,
  );

  const headPromise = page.waitForResponse(
    (response) =>
      response.request().method() === "HEAD" &&
      response.url().includes("/api/v1/download/"),
    { timeout: plan.timeouts.exportMs },
  );
  const downloadPromise = page.waitForEvent("download", {
    timeout: plan.timeouts.exportMs,
  });
  await downloadButton.click();
  const head = await headPromise;
  const toast = page.getByText("Archive download started.", { exact: true }).first();
  await toast.waitFor({ timeout: 30_000 });
  const download = await downloadPromise;
  assert.equal(head.status(), 200, `${entry.format}: the download preflight must succeed`);

  const filename = download.suggestedFilename();
  const detected = formatFromFilename(filename);
  assert.equal(detected, entry.format, `${entry.format}: downloaded filename ${filename}`);
  const file = safeArtifactName(filename, `export-${entry.format}`);
  const target = artifactPath(outDir, file);
  await download.saveAs(target);
  const bytes = await fs.readFile(target);
  assert.ok(bytes.byteLength > 0, `${entry.format}: the download must carry bytes`);

  await page.getByTestId("archive-export-panel").waitFor({ state: "hidden" });
  const recordEntry = {
    format: entry.format,
    file,
    filename,
    bytes: bytes.byteLength,
    sha256: sha256(bytes),
    preflightStatus: head.status(),
    preflightPath: new URL(head.url()).pathname,
    toast: "Archive download started.",
  };
  record.exports.push(recordEntry);
  return recordEntry;
}

async function exportPhase(page, plan, outDir, record) {
  for (const entry of plan.exports) {
    await openDetail(page, plan);
    await openExportModal(page);
    await prepareExport(page, plan, entry);

    if (entry.format === "zip") {
      await capture(
        page,
        outDir,
        "export-zip-ready-desktop-dark",
        { width: 1440, height: 900 },
        "dark",
        record.screenshots,
      );
    } else {
      await capture(
        page,
        outDir,
        "export-tar-ready-narrow-light",
        { width: 390, height: 844 },
        "light",
        record.screenshots,
      );
      // The narrow crop is evidence only; continue the same browser session at
      // the desktop viewport so the modal actions stay in their normal layout.
      await page.setViewportSize({ width: 1440, height: 900 });
    }

    await downloadPrepared(page, plan, entry, outDir, record);
  }
}

async function importPhase(page, plan, outDir, record, persist) {
  for (const entry of plan.imports) {
    await page.goto(`${plan.base}/servers/new`, { waitUntil: "domcontentloaded" });
    await page.getByRole("button", { name: "Next Step" }).waitFor({
      timeout: plan.timeouts.routeMs,
    });

    await page.getByPlaceholder("survival-smp-01").fill(entry.name);
    const tile = page.getByTestId("create-from-archive-tile");
    await tile.waitFor();
    await tile.click();

    const archive = planArtifactPath(outDir, entry.file);
    await page.locator('[data-testid="archive-file-input"]').setInputFiles(archive);
    await page.getByTestId("archive-preview").waitFor({
      timeout: plan.timeouts.previewMs,
    });
    const previewText = await page.getByTestId("archive-preview").innerText();
    const preview = previewFacts(previewText);
    assert.equal(
      preview.looksLikePreview,
      true,
      `${entry.format}: the preview panel must render`,
    );
    assert.ok(
      preview.text.includes(entry.expectedLabel),
      `${entry.format}: the preview must name ${entry.expectedLabel}`,
    );
    assert.equal(
      preview.saysNoFindings,
      true,
      `${entry.format}: the fixture archive must preview without findings`,
    );

    if (entry.format === "zip") {
      await capture(
        page,
        outDir,
        "import-zip-preview-desktop-light",
        { width: 1440, height: 900 },
        "light",
        record.screenshots,
      );
    } else {
      await capture(
        page,
        outDir,
        "import-tar-preview-narrow-light",
        { width: 390, height: 844 },
        "light",
        record.screenshots,
      );
      await page.setViewportSize({ width: 1440, height: 900 });
    }

    await page.getByRole("button", { name: "Apply runtime defaults" }).click();
    await page
      .getByText("Archive runtime defaults applied. Review them before creating.", {
        exact: true,
      })
      .first()
      .waitFor({ timeout: 15_000 });

    // Step 0 basic -> 1 engine -> 2 resources.
    await page.getByRole("button", { name: "Next Step" }).click();
    await page.getByRole("button", { name: "Next Step" }).click();
    const portInput = page.locator("#port");
    await portInput.waitFor({ timeout: 15_000 });
    await portInput.fill(String(entry.port));
    await page.getByRole("button", { name: "Next Step" }).click();

    // Step 3 placement: bind the disposable remote engine, not the source node.
    await page.getByTestId("archive-destination-confirmation").waitFor({
      timeout: plan.timeouts.previewMs,
    });
    const options = page.getByTestId("archive-destination-option");
    await options.first().waitFor({ timeout: plan.timeouts.previewMs });
    const optionCount = await options.count();
    let chosen = -1;
    for (let index = 0; index < optionCount; index += 1) {
      const text = normalizeText(await options.nth(index).innerText());
      if (text.includes(plan.remoteNode.name)) {
        chosen = index;
        break;
      }
    }
    assert.ok(
      chosen >= 0,
      `${entry.format}: the remote node ${plan.remoteNode.name} must be offered`,
    );
    await options.nth(chosen).click();
    await page.waitForFunction(
      (nodeId) =>
        document
          .querySelector('[data-testid="archive-destination-node"]')
          ?.textContent?.trim() === nodeId,
      plan.remoteNode.id,
      { timeout: plan.timeouts.previewMs },
    );
    await page
      .getByText("Destination has enough free space", { exact: true })
      .waitFor({ timeout: plan.timeouts.previewMs });

    await capture(
      page,
      outDir,
      `import-${entry.format === "zip" ? "zip" : "tar"}-destination-desktop-dark`,
      { width: 1440, height: 900 },
      "dark",
      record.screenshots,
    );

    await page.getByRole("button", { name: "Next Step" }).click();
    await page.getByTestId("archive-addons-disabled").waitFor({ timeout: 15_000 });

    const create = page.getByRole("button", { name: "Create Server", exact: true });
    await create.waitFor();
    const deadline = Date.now() + plan.timeouts.createMs;
    while (await create.isDisabled()) {
      if (Date.now() > deadline) {
        throw new Error(`${entry.format}: Create Server stayed disabled`);
      }
      await page.waitForTimeout(250);
    }
    await create.click();
    // Archive creation commits the server and /servers/new immediately navigates
    // to /servers/[id] because an archive import has no setup tasks. Waiting for
    // the real route is deterministic; the transient "Server created" panel and
    // its result link are not.
    await page.waitForURL((url) => serverIdFromUrl(url) !== null, {
      timeout: plan.timeouts.createMs,
    });
    const current = new URL(page.url());
    const createdServerId = serverIdFromUrl(current);
    assert.ok(
      createdServerId,
      `${entry.format}: create did not navigate to /servers/<uuid>`,
    );

    const createdEntry = {
      format: entry.format,
      name: entry.name,
      file: entry.file,
      expectedLabel: entry.expectedLabel,
      preview: {
        text: preview.text.slice(0, 600),
        saysNoFindings: preview.saysNoFindings,
        exporterMentioned: preview.exporterMentioned,
      },
      destinationNodeId: plan.remoteNode.id,
      destinationNodeName: plan.remoteNode.name,
      port: entry.port,
      createdServerId,
      createdRoute: current.pathname,
    };
    // Custody first: the id reaches disk before any screenshot or theme
    // evaluation can fail, so Python cleanup still owns the created server.
    await recordCreatedImport({
      record,
      entry: createdEntry,
      persist,
      capture: async () => {
        await page.getByTestId("showcase-server-detail").waitFor({
          timeout: plan.timeouts.routeMs,
        });
        await capture(
          page,
          outDir,
          `import-${entry.format === "zip" ? "zip" : "tar"}-created-detail-desktop-light`,
          { width: 1440, height: 900 },
          "light",
          record.screenshots,
        );
      },
    });
  }
}

export async function runDriver({ planPath, outDir, env = process.env, cwd = repoRoot }) {
  const plan = parsePlan(JSON.parse(await fs.readFile(planPath, "utf8")));
  const head = readHead(plan.head, cwd);
  if (!head.match) {
    throw new Error(`checkout head ${head.observed} is not the planned ${plan.head}`);
  }
  if (!head.worktreeClean) {
    throw new Error("tracked files changed after the exact-head checkout");
  }

  const username = env.ARCHIVE_UI_USER;
  const password = env.ARCHIVE_UI_PASSWORD;
  if (!username || !password) {
    throw new Error("ARCHIVE_UI_USER and ARCHIVE_UI_PASSWORD are required");
  }

  await fs.mkdir(outDir, { recursive: true });
  const record = {
    schema: RECORD_SCHEMA,
    phase: plan.phase,
    generatedAt: new Date().toISOString(),
    head,
    base: plan.base,
    source: { serverId: plan.source.serverId },
    browser: null,
    screenshots: [],
    refusal: null,
    exports: [],
    imports: [],
    error: null,
  };

  // Imported lazily so the offline tests can exercise the plan/evidence helpers
  // without a browser package installed on the test host.
  const { launchReviewBrowser } = await import("./review/browser.mjs");
  const { browser, metadata } = await launchReviewBrowser();
  record.browser = metadata;
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    acceptDownloads: true,
  });
  const page = await context.newPage();
  try {
    await login(page, plan.base, { username, password }, plan.timeouts);
    const persist = () =>
      writeDriverRecord(record, outDir, plan.phase);
    if (plan.phase === "refusal") {
      await refusalPhase(page, plan, outDir, record);
    } else if (plan.phase === "export") {
      await exportPhase(page, plan, outDir, record);
    } else {
      await importPhase(page, plan, outDir, record, persist);
    }
  } catch (error) {
    record.error = String((error && error.message) || error);
    // Attach before the best-effort failure screenshot so the partial record
    // (including any created id) survives even if that capture fails.
    error.record = record;
    await page
      .screenshot({
        path: artifactPath(outDir, `FAILED-${plan.phase}.png`),
        fullPage: true,
      })
      .catch(() => {});
    throw error;
  } finally {
    await context.close().catch(() => {});
    await browser.close().catch(() => {});
  }
  return record;
}

export function upsertImportEntry(record, entry) {
  if (!record || !Array.isArray(record.imports)) {
    throw new Error("driver record has no imports list");
  }
  const format = nonEmptyString(entry?.format, "import.format");
  const index = record.imports.findIndex(
    (existing) => existing && existing.format === format,
  );
  if (index === -1) {
    record.imports.push(entry);
  } else {
    record.imports[index] = { ...record.imports[index], ...entry };
  }
  return entry;
}

// Record the created server id, persist it atomically, and only then do any
// risky browser work (screenshots, theme evaluation). Callers keep the entry
// in memory even if capture fails, so the driver failure path can attach the
// partial record and Python cleanup still owns the server.
export async function recordCreatedImport({ record, entry, persist, capture }) {
  if (typeof persist !== "function") {
    throw new Error("recordCreatedImport requires a persist callback");
  }
  upsertImportEntry(record, entry);
  await persist();
  if (capture) await capture();
  return entry;
}

export async function writeDriverRecord(record, outDir, phase) {
  const target = artifactPath(outDir, `${phase}-run.json`);
  const temporary = `${target}.tmp`;
  await fs.writeFile(temporary, `${JSON.stringify(record, null, 2)}\n`, "utf8");
  await fs.rename(temporary, target);
  return target;
}

function requiredArgument(argv, name) {
  const index = argv.indexOf(`--${name}`);
  if (index === -1 || !argv[index + 1]) {
    throw new Error(`--${name} is required`);
  }
  return argv[index + 1];
}

async function main(argv = process.argv.slice(2)) {
  const planPath = requiredArgument(argv, "plan");
  const outDir = path.resolve(requiredArgument(argv, "out"));
  const phase = requiredArgument(argv, "phase");
  if (!PHASES.includes(phase)) {
    throw new Error(`--phase is ${phase}, want one of ${PHASES.join(", ")}`);
  }
  let record = null;
  try {
    record = await runDriver({ planPath, outDir });
    assert.equal(record.phase, phase, "driver phase must match the plan");
    await writeDriverRecord(record, outDir, phase);
    process.stdout.write(
      `${JSON.stringify({
        pass: true,
        phase,
        record: artifactPath(outDir, `${phase}-run.json`),
      })}\n`,
    );
    return 0;
  } catch (error) {
    if (record) {
      await writeDriverRecord(record, outDir, phase).catch(() => {});
    } else if (error && error.record) {
      await writeDriverRecord(error.record, outDir, phase).catch(() => {});
    }
    process.stderr.write(
      `${JSON.stringify({ pass: false, phase, error: String((error && error.message) || error) })}\n`,
    );
    return 1;
  }
}

const invokedDirectly =
  process.argv[1] !== undefined &&
  pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url;

if (invokedDirectly) {
  main().then((code) => {
    process.exitCode = code;
  });
}
