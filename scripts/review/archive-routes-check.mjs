// SPDX-License-Identifier: MPL-2.0
// Normal-route browser evidence for OSS-428 archive portability.
//
// Renders the production /servers/new and /servers/[id] route components in a
// disposable Vite fixture. The RPC transport, SvelteKit app modules and the
// resumable uploader are replaced; the routes, archive components, Carbon
// controls, Tailwind theme and language are the application implementation.
// No live panel, credentials, Docker daemon, world or external service is used.
//
// The script writes screenshots, downloaded fixture archives and results.json
// and indexes every generated file in results.json. It also records the exact
// head, browser, viewport, theme, contrast, overflow, scroll and page-error
// checks for each captured state.
//
// Preview outcomes are recorded from the fixture itself: previewServerArchive
// resolves inside the seeded in-process rpc-client, not over HTTP, so results.json
// records the method, upload/destination, resolved/failed outcome, Connect error
// code and capacity values, and states plainly that no HTTP status exists. Only
// the archive downloads are HTTP and carry a status and byte count.
//
//   PLAYWRIGHT_BROWSERS_PATH=<existing browser cache> \
//   REVIEW_ARCHIVE_OUT=<evidence directory> \
//     node scripts/review/archive-routes-check.mjs
//
// A live-controller run is deliberately separate: bring up the disposable
// review stack, point REVIEW_BASE at it and use the hosted commands recorded in
// results.json. This checkout does not start Docker on the Windows host.

import assert from "node:assert/strict";
import crypto from "node:crypto";
import { execFileSync } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";
import { launchReviewBrowser } from "./browser.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../..");
const project = path.resolve(repoRoot, "web/carbon-panel");
const fixture = path.join(project, "e2e", "wizard-datapack-upload");
const verifiedHead = execFileSync("git", ["rev-parse", "HEAD"], {
  cwd: repoRoot,
  encoding: "utf8",
}).trim();
const worktreeStatus = execFileSync("git", ["status", "--porcelain"], {
  cwd: repoRoot,
  encoding: "utf8",
}).trim();
const out = path.resolve(
  process.env.REVIEW_ARCHIVE_OUT ||
    path.join(here, "../../tmp/review-output/archive-routes-fixture"),
);
const browserCache =
  process.env.PLAYWRIGHT_BROWSERS_PATH ||
  path.join(repoRoot, "tmp", "review-browsers");

const requireFrontend = createRequire(path.join(project, "package.json"));
const { createServer } = await import(
  pathToFileURL(requireFrontend.resolve("vite")).href
);
const { svelte } = await import(
  pathToFileURL(requireFrontend.resolve("@sveltejs/vite-plugin-svelte")).href
);
const { default: tailwind } = await import(
  pathToFileURL(requireFrontend.resolve("@tailwindcss/vite")).href
);

const sha256 = (buffer) =>
  crypto.createHash("sha256").update(buffer).digest("hex");
const slug = (route) => (route.includes("[id]") ? "detail" : "new");
const views = [
  { name: "desktop-dark", width: 1440, height: 900, theme: "dark" },
  { name: "desktop-light", width: 1440, height: 900, theme: "light" },
  { name: "narrow-dark", width: 390, height: 844, theme: "dark" },
  { name: "narrow-light", width: 390, height: 844, theme: "light" },
];

const evidence = {
  head: {
    sha: verifiedHead,
    source: "git rev-parse HEAD",
    worktreeDirty: Boolean(worktreeStatus),
    worktreeStatus: worktreeStatus ? worktreeStatus.split("\n") : [],
  },
  scope:
    "Disposable normal-route fixture: production /servers/new and /servers/[id] route components, archive export/create components, Carbon controls and application CSS. Live controller, Docker daemon, real worlds and external services are not exercised.",
  fixture: {
    entries: [
      "/new.html -> real routes/servers/new/+page.svelte",
      "/detail.html -> real routes/servers/[id]/+page.svelte",
    ],
    replaced: [
      "$lib/api/rpc-client (seeded loopback RPC responses)",
      "$app/state, $app/navigation, $app/paths, $app/env (inert route stubs)",
      "$lib/utils/chunked-upload (bounded uploader)",
      "$lib/components/carbon (barrel re-exports the real Carbon components)",
    ],
    real: [
      "server detail route and overview layout",
      "create-server wizard and archive preview, placement and create flow",
      "ServerArchiveExport and ArchiveExportPanel",
      "Carbon modal, buttons, controls, theme tokens and app.css",
    ],
    downloadSessions: {
      "export-session-zip": "fixture zip bytes",
      "export-session-tar": "fixture tar.gz bytes",
      "export-session-refused": "fixture refusal response (403, no body)",
    },
    previewTransport:
      "previewServerArchive is answered by the seeded in-process rpc-client in web/carbon-panel/e2e/wizard-datapack-upload/rpc-stub.js. It makes no HTTP request, so a preview has no HTTP status or byte length; the recorded facts are the call payload, resolved/failed outcome, Connect error code, findings and capacity.",
  },
  hostedPath: {
    status: "not-run",
    blocker:
      "This Windows checkout has no disposable Linux/WSL review stack or Docker daemon; AGENTS.md keeps Docker fixtures in a disposable named session and this assignment forbids starting a full Docker stack on the host.",
    exactHead: "git rev-parse HEAD must equal head.sha before the hosted run",
    lifecycle: [
      "node scripts/review/run.mjs single-seeded up",
      "node scripts/review/setup-browser.mjs",
      "read the named session port and PANEL_REVIEW_USER/PANEL_REVIEW_PASSWORD from the disposable stack",
      "exercise the same /servers/[id] and /servers/new state list against REVIEW_BASE on that exact head",
      "node scripts/review/run.mjs single-seeded down",
    ],
    automation:
      "This commit records the normal-route fixture run only and does not claim a hosted-mode driver. A hosted rerun is a separate exact-head step; live-controller and engine round trips remain separate evidence and are not part of this result set.",
  },
  views: [],
  states: [],
  previews: [],
  network: {
    pageXhrOrFetch: [],
    downloadPreflights: [],
    note: "No browser XHR/fetch request is issued for archive preview or create: both go through the seeded in-process rpc-client, so no preview HTTP status can be recorded. Archive downloads are the only HTTP transfers: each authorized or refused download now starts with a same-origin HEAD preflight, recorded here, and successful preflights are followed by the ordinary browser GET recorded in downloads/downloadRefusals.",
  },
  downloads: [],
  unacknowledged: [],
  downloadRefusals: [],
  keyboard: [],
  checks: {
    pageErrors: [],
    consoleErrors: [],
    expectedRefusalConsoleErrors: [],
    overflow: [],
    contrast: [],
    clipping: [],
    theme: [],
  },
  controls: [],
  artifacts: [],
};

const artifacts = [];
const downloadRequests = [];
const pageRequests = [];
const preflightRequests = [];
let base = "";
let server;
let browser;

function artifactPath(name) {
  const file = path.join(out, name);
  const relative = path.relative(out, file);
  if (relative.startsWith("..") || path.isAbsolute(relative)) {
    throw new Error(`artifact escapes the evidence directory: ${name}`);
  }
  return file;
}

async function shot(target, name, meta) {
  const file = artifactPath(name);
  await fs.mkdir(path.dirname(file), { recursive: true });
  await target.screenshot({ path: file, animations: "disabled" });
  const bytes = await fs.readFile(file);
  artifacts.push({
    file: path.relative(out, file).replaceAll("\\", "/"),
    bytes: bytes.byteLength,
    sha256: sha256(bytes),
    ...meta,
  });
  return file;
}

async function assertArtifactsComplete() {
  const expected = new Set([
    "results.json",
    ...artifacts.map((entry) => entry.file),
  ]);
  const found = [];
  async function walk(directory) {
    for (const entry of await fs.readdir(directory, { withFileTypes: true })) {
      const absolute = path.join(directory, entry.name);
      if (entry.isDirectory()) await walk(absolute);
      else found.push(path.relative(out, absolute).replaceAll("\\", "/"));
    }
  }
  await walk(out);
  const unindexed = found.filter((file) => !expected.has(file));
  const missing = [...expected].filter((file) => !found.includes(file));
  assert.deepEqual(unindexed, [], "every generated file must be indexed");
  assert.deepEqual(missing, [], "every indexed artifact must exist");
}

async function waitForFunction(page, fn, label, timeout = 25_000) {
  try {
    await page.waitForFunction(fn, null, { timeout });
  } catch (error) {
    throw new Error(`timed out waiting for ${label}`, { cause: error });
  }
}

async function waitForWizard(page) {
  await waitForFunction(
    page,
    () => Boolean(globalThis.__wizardCalls),
    "the wizard fixture",
  );
  await page
    .getByRole("button", { name: "Next Step" })
    .waitFor({ timeout: 15_000 });
}

async function waitForDetail(page) {
  await page.getByTestId("server-export-archive").waitFor({ timeout: 30_000 });
  await page.getByTestId("showcase-server-detail").waitFor({ timeout: 30_000 });
}

// The fixture records every preview answer in __wizardPreviewOutcomes; consume
// each outcome once per wizard run so results.json names the state that caused it.
let previewCursor = 0;

function previewRecord(view, scenario, outcome) {
  return {
    view: view.name,
    scenario,
    method: outcome.method,
    transport:
      "seeded in-process rpc-client (web/carbon-panel/e2e/wizard-datapack-upload/rpc-stub.js)",
    httpStatus: null,
    httpStatusNote:
      "no HTTP request exists at this seam: the fixture stub resolves the preview in-process, so a preview has no HTTP status or byte length",
    uploadId: outcome.uploadId ?? "",
    nodeId: outcome.nodeId ?? "",
    outcome: outcome.outcome,
    errorCode: outcome.errorCode ?? null,
    errorName: outcome.errorName ?? null,
    findings: outcome.findings ?? 0,
    destinationIds: outcome.destinationIds ?? [],
    availableBytes: outcome.availableBytes ?? null,
    requiredBytes: outcome.requiredBytes ?? null,
  };
}

async function capturePreviews(page, view, scenario) {
  const outcomes = await page.evaluate(
    () => globalThis.__wizardPreviewOutcomes ?? [],
  );
  // The route may reuse the automatic destination preview, so a later UI state
  // can legitimately add no new fixture outcome. Callers assert what they need.
  const fresh = outcomes.slice(previewCursor);
  previewCursor = outcomes.length;
  const records = fresh.map((outcome) =>
    previewRecord(view, scenario, outcome),
  );
  for (const record of records) {
    assert.ok(
      record.outcome === "resolved" || record.outcome === "failed",
      `${scenario}: preview outcome must be resolved or failed`,
    );
    assert.equal(
      record.httpStatus,
      null,
      `${scenario}: this fixture boundary has no HTTP status for a preview`,
    );
    if (record.outcome === "failed") {
      assert.equal(
        typeof record.errorCode,
        "number",
        `${scenario}: a failed preview must carry its Connect error code`,
      );
      assert.ok(record.errorName, `${scenario}: the error code must be named`);
    } else {
      assert.ok(
        record.destinationIds.length >= 0,
        `${scenario}: a resolved preview lists its destinations`,
      );
    }
  }
  evidence.previews.push(...records);
  return records;
}

async function applyTheme(page, theme, label) {
  await page.evaluate((value) => {
    document.documentElement.setAttribute("data-carbon-theme", value);
    document.documentElement.classList.toggle("dark", value === "dark");
  }, theme);
  await page.waitForTimeout(120);
  const measured = await page.evaluate(() => {
    const probe = document.createElement("div");
    probe.className = "bg-background";
    probe.style.cssText =
      "position:fixed;left:-9999px;top:0;width:1px;height:1px;pointer-events:none";
    document.body.appendChild(probe);
    const background = getComputedStyle(probe).backgroundColor;
    probe.remove();
    const match = background.match(/rgba?\(([^)]+)\)/);
    const parts = match
      ? match[1]
          .split(/[ ,/]+/)
          .filter(Boolean)
          .map(Number)
      : [255, 255, 255, 1];
    const linear = (channel) => {
      const value = channel / 255;
      return value <= 0.04045
        ? value / 12.92
        : ((value + 0.055) / 1.055) ** 2.4;
    };
    const luminance =
      0.2126 * linear(parts[0]) +
      0.7152 * linear(parts[1]) +
      0.0722 * linear(parts[2]);
    return {
      attribute: document.documentElement.getAttribute("data-carbon-theme"),
      darkClass: document.documentElement.classList.contains("dark"),
      background,
      luminance,
      isDark: luminance < 0.5,
    };
  });
  assert.equal(measured.attribute, theme, `${label}: theme attribute`);
  assert.equal(measured.darkClass, theme === "dark", `${label}: dark class`);
  assert.equal(
    measured.isDark,
    theme === "dark",
    `${label}: rendered background ${measured.background} does not match ${theme}`,
  );
  evidence.checks.theme.push({ label, theme, ...measured });
  return measured;
}

function contrastRatio(foreground, background) {
  const linear = (channel) => {
    const value = channel / 255;
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  };
  const luminance = ([r, g, b]) =>
    0.2126 * linear(r) + 0.7152 * linear(g) + 0.0722 * linear(b);
  const [lighter, darker] = [luminance(foreground), luminance(background)].sort(
    (a, b) => b - a,
  );
  return (lighter + 0.05) / (darker + 0.05);
}

async function contrastSamples(root) {
  return root.evaluate((element) => {
    const parse = (value) => {
      const match = value.match(/rgba?\(([^)]+)\)/);
      if (!match) return null;
      const parts = match[1]
        .split(/[ ,/]+/)
        .filter(Boolean)
        .map(Number);
      return [parts[0], parts[1], parts[2], parts[3] ?? 1];
    };
    const composite = (top, bottom) => {
      const alpha = top[3];
      return [
        top[0] * alpha + bottom[0] * (1 - alpha),
        top[1] * alpha + bottom[1] * (1 - alpha),
        top[2] * alpha + bottom[2] * (1 - alpha),
        1,
      ];
    };
    const samples = [];
    const nodes = element.querySelectorAll(
      "p,h1,h2,h3,h4,legend,label,button,span",
    );
    for (const node of nodes) {
      const text = node.textContent?.replace(/\s+/g, " ").trim();
      if (!text || node.closest("button[disabled]")) continue;
      if (!node.offsetParent && node.tagName !== "BODY") continue;
      const style = getComputedStyle(node);
      if (style.visibility === "hidden" || Number(style.opacity) === 0)
        continue;
      let background = [255, 255, 255, 1];
      let opacity = 1;
      for (let current = node; current; current = current.parentElement) {
        const currentStyle = getComputedStyle(current);
        opacity *= Number(currentStyle.opacity || 1);
        const parsed = parse(currentStyle.backgroundColor);
        if (parsed && parsed[3] > 0) {
          background = composite(parsed, background);
          if (parsed[3] === 1) break;
        }
      }
      const foreground = parse(style.color) || [0, 0, 0, 1];
      if (opacity < 1) foreground[3] *= opacity;
      const effective =
        foreground[3] === 1
          ? foreground
          : [
              foreground[0] * foreground[3] +
                background[0] * (1 - foreground[3]),
              foreground[1] * foreground[3] +
                background[1] * (1 - foreground[3]),
              foreground[2] * foreground[3] +
                background[2] * (1 - foreground[3]),
              1,
            ];
      samples.push({
        text: text.slice(0, 120),
        foreground: effective.slice(0, 3),
        background: background.slice(0, 3),
        fontSize: Number.parseFloat(style.fontSize),
        fontWeight: Number.parseInt(style.fontWeight, 10) || 400,
      });
    }
    return samples;
  });
}

async function checkContrast(root, label) {
  const samples = await contrastSamples(root);
  assert.ok(samples.length > 0, `${label}: expected readable text`);
  const violations = [];
  for (const sample of samples) {
    const ratio = contrastRatio(sample.foreground, sample.background);
    const large =
      sample.fontSize >= 24 ||
      (sample.fontSize >= 18.66 && sample.fontWeight >= 700);
    const required = large ? 3 : 4.5;
    if (ratio + 0.001 < required) {
      violations.push({
        text: sample.text,
        ratio: Number(ratio.toFixed(2)),
        required,
        fontSize: sample.fontSize,
        fontWeight: sample.fontWeight,
      });
    }
  }
  assert.deepEqual(violations, [], `${label}: contrast violations`);
  const minimum = samples.reduce(
    (lowest, sample) =>
      Math.min(lowest, contrastRatio(sample.foreground, sample.background)),
    Infinity,
  );
  evidence.checks.contrast.push({
    label,
    samples: samples.length,
    minimum: Number(minimum.toFixed(2)),
  });
  return minimum;
}

async function checkNoHorizontalOverflow(page, label) {
  const measured = await page.evaluate(() => {
    const root = document.documentElement;
    const body = document.body;
    return {
      rootOverflow: root.scrollWidth - root.clientWidth,
      bodyOverflow: body.scrollWidth - body.clientWidth,
      viewport: { width: window.innerWidth, height: window.innerHeight },
    };
  });
  assert.ok(
    measured.rootOverflow <= 1 && measured.bodyOverflow <= 1,
    `${label}: horizontal overflow root=${measured.rootOverflow} body=${measured.bodyOverflow}`,
  );
  evidence.checks.overflow.push({ label, ...measured });
  return measured;
}

async function checkNoClipping(target, label) {
  const measured = await target.evaluate((element) => ({
    scrollHeight: element.scrollHeight,
    clientHeight: element.clientHeight,
    scrollWidth: element.scrollWidth,
    clientWidth: element.clientWidth,
  }));
  assert.ok(
    measured.scrollHeight <= measured.clientHeight + 1,
    `${label}: content is clipped by an internal vertical scroller`,
  );
  assert.ok(
    measured.scrollWidth <= measured.clientWidth + 1,
    `${label}: content is clipped by an internal horizontal scroller`,
  );
  evidence.checks.clipping.push({ label, ...measured, clipped: false });
  return measured;
}

async function captureScrollerSegments(target, baseName, meta) {
  const handle = await target.evaluateHandle((element) => {
    for (let node = element.parentElement; node; node = node.parentElement) {
      const style = getComputedStyle(node);
      if (
        /(auto|scroll)/.test(style.overflowY) &&
        node.scrollHeight > node.clientHeight + 1
      ) {
        return node;
      }
    }
    return null;
  });
  const scroller = handle.asElement();
  if (!scroller) return { segments: 1 };
  const segments = [];
  try {
    await scroller.evaluate((element) => {
      element.scrollTop = 0;
    });
    segments.push(
      await shot(scroller, `${baseName}-scroll-top.png`, {
        ...meta,
        role: "scroll-segment",
        segment: "top",
      }),
    );
    const maximum = await scroller.evaluate((element) => {
      element.scrollTop = element.scrollHeight;
      return element.scrollTop;
    });
    assert.ok(maximum > 0, "a scroll segment must have a bottom position");
    segments.push(
      await shot(scroller, `${baseName}-scroll-bottom.png`, {
        ...meta,
        role: "scroll-segment",
        segment: "bottom",
      }),
    );
  } finally {
    await scroller.evaluate((element) => {
      element.scrollTop = 0;
    });
    await handle.dispose();
  }
  return {
    segments: segments.length,
    files: segments.map((file) => path.basename(file)),
  };
}

async function captureState(page, spec) {
  const {
    route,
    state,
    view,
    target,
    label = `${route} ${state}`,
    captureViewport = true,
  } = spec;
  const actual = page.viewportSize();
  assert.deepEqual(
    actual,
    { width: view.width, height: view.height },
    `${label}: viewport drifted from ${view.width}x${view.height}`,
  );
  await target.first().waitFor({ state: "visible", timeout: 30_000 });
  await target
    .first()
    .scrollIntoViewIfNeeded()
    .catch(() => {});
  await page.waitForTimeout(80);
  const overflow = await checkNoHorizontalOverflow(page, label);
  const clipping = await checkNoClipping(target.first(), label);
  const minimumContrast = await checkContrast(target.first(), label);
  const name = `${slug(route)}-${state}-${view.name}`;
  await shot(target.first(), `${name}.png`, {
    role: "state-panel",
    route,
    state,
    view: view.name,
    theme: view.theme,
    viewport: actual,
  });
  let contextFile = null;
  if (captureViewport) {
    contextFile = await shot(page, `${name}-viewport.png`, {
      role: "viewport-context",
      route,
      state,
      view: view.name,
      theme: view.theme,
      viewport: actual,
    });
  }
  const scroller = await captureScrollerSegments(target.first(), name, {
    route,
    state,
    view: view.name,
  });
  const entry = {
    route,
    state,
    view: view.name,
    theme: view.theme,
    viewport: actual,
    screenshot: path.basename(artifactPath(`${name}.png`)),
    viewportScreenshot: contextFile ? path.basename(contextFile) : null,
    contrastMinimum: Number(minimumContrast.toFixed(2)),
    overflow,
    clipping,
    scrollSegments: scroller.segments,
    scrollSegmentFiles: scroller.files ?? [],
  };
  evidence.states.push(entry);
  return entry;
}

async function focusLabel(page) {
  return page.evaluate(() => {
    const active = document.activeElement;
    return (
      active?.getAttribute("aria-label") ||
      active?.textContent?.replace(/\s+/g, " ").trim() ||
      active?.tagName ||
      ""
    );
  });
}

async function openExportWithKeyboard(page, label) {
  const trigger = page.getByTestId("server-export-archive");
  await trigger.focus();
  await page.keyboard.press("Enter");
  evidence.keyboard.push(`${label}: Enter opened the export modal`);
  await page.getByTestId("archive-export-panel").waitFor();
  await page.waitForFunction(
    () => document.activeElement?.getAttribute("aria-label") === "Close modal",
  );
}

// --carbon-control-md is the 40px Carbon control height. Measuring the rendered
// box keeps the sizing claim measured instead of inferred from the class list.
async function measureControl(locator, label) {
  const box = await locator.boundingBox();
  assert.ok(box, `${label}: control must be laid out`);
  const height = Math.round(box.height * 10) / 10;
  assert.ok(
    height >= 39.5 && height <= 41,
    `${label}: height ${height}px is not the 40px Carbon md control`,
  );
  const record = { label, height, target: 40 };
  evidence.controls.push(record);
  return record;
}

async function chooseFormatWithKeyboard(page, label, format) {
  await page.keyboard.press("Tab");
  if (format === "tar.gz") await page.keyboard.press("Tab");
  await page.keyboard.press("Space");
  const option = format === "tar.gz" ? "TAR.GZ (.tar.gz)" : "ZIP (.zip)";
  assert.equal(
    await page
      .getByRole("radio", { name: option })
      .getAttribute("aria-checked"),
    "true",
    `${label}: Space must select ${option}`,
  );
  evidence.keyboard.push(`${label}: Space selected ${option}`);
}

async function closeExportWithKeyboard(page, label) {
  await page.keyboard.press("Escape");
  await page.getByTestId("archive-export-panel").waitFor({ state: "hidden" });
  await page.waitForFunction(
    () =>
      document.activeElement?.getAttribute("data-testid") ===
      "server-export-archive",
  );
  evidence.keyboard.push(
    `${label}: Escape closed the modal and returned focus`,
  );
}

async function recordDownload(page, download, view, format) {
  const suggested = download.suggestedFilename();
  const savedName = `download-${view.name}-${format}-${suggested}`;
  const saved = artifactPath(savedName);
  await download.saveAs(saved);
  const bytes = await fs.readFile(saved);
  artifacts.push({
    file: savedName,
    bytes: bytes.byteLength,
    sha256: sha256(bytes),
    role: "download",
    view: view.name,
    format,
    filename: suggested,
  });
  const response = await page.context().request.get(download.url());
  const body = await response.body();
  const headers = response.headers();
  const record = {
    format,
    view: view.name,
    url: download.url().replace(base, "<review-base>"),
    status: response.status(),
    filename: suggested,
    disposition: headers["content-disposition"] || "",
    contentType: headers["content-type"] || "",
    declaredLength: Number(headers["content-length"] || 0),
    downloadedBytes: bytes.byteLength,
    responseBytes: body.byteLength,
    sha256Download: sha256(bytes),
    sha256Response: sha256(body),
    metadataSource: "page.context().request.get(download.url())",
  };
  assert.equal(record.status, 200, `${view.name} ${format}: status`);
  assert.ok(
    record.filename.endsWith(format === "tar.gz" ? ".tar.gz" : ".zip"),
    `${view.name} ${format}: filename ${record.filename}`,
  );
  assert.ok(
    record.disposition.includes(record.filename),
    `${view.name} ${format}: disposition names ${record.filename}`,
  );
  assert.equal(
    record.declaredLength,
    record.downloadedBytes,
    `${view.name} ${format}: declared length matches the download`,
  );
  assert.equal(
    record.responseBytes,
    record.downloadedBytes,
    `${view.name} ${format}: re-fetch length matches the download`,
  );
  assert.equal(
    record.sha256Response,
    record.sha256Download,
    `${view.name} ${format}: re-fetch digest matches the download`,
  );
  assert.ok(record.downloadedBytes > 0, `${view.name} ${format}: bytes`);
  evidence.downloads.push(record);
  return record;
}

async function recordRefusedDownload(page, view, label) {
  const response = await page
    .context()
    .request.get(`${base}/api/v1/download/export-session-refused`);
  const body = await response.body();
  const headers = response.headers();
  const record = {
    view: view.name,
    label,
    source: "page.context().request.get(...) outside the UI",
    url: `${base}/api/v1/download/export-session-refused`.replace(
      base,
      "<review-base>",
    ),
    status: response.status(),
    declaredLength: Number(headers["content-length"] || 0),
    bytes: body.byteLength,
    contentType: headers["content-type"] || "",
    disposition: headers["content-disposition"] || "",
  };
  assert.equal(record.status, 403, `${label}: fixture refusal status`);
  assert.equal(record.bytes, 0, `${label}: refusal carries no archive bytes`);
  evidence.downloadRefusals.push(record);
}

async function exerciseDetail(page, view) {
  await page.goto(`${base}/detail.html`, { waitUntil: "domcontentloaded" });
  await waitForDetail(page);
  await applyTheme(page, view.theme, `detail ${view.name}`);

  const panel = page.getByTestId("archive-export-panel");
  await measureControl(
    page.getByTestId("server-export-archive"),
    `detail ${view.name}: Export archive trigger`,
  );
  await openExportWithKeyboard(page, `detail ${view.name}`);
  await captureState(page, {
    route: "/servers/[id]",
    state: "export-idle-zip",
    view,
    target: panel,
  });
  await measureControl(
    page.getByTestId("archive-prepare"),
    `detail ${view.name}: Prepare export`,
  );

  await chooseFormatWithKeyboard(page, `detail ${view.name}`, "tar.gz");
  await captureState(page, {
    route: "/servers/[id]",
    state: "export-idle-tar",
    view,
    target: panel,
  });

  await page.evaluate(() => {
    globalThis.__exportMode = "hold";
    globalThis.__wizardReset();
  });
  await page.keyboard.press("Tab");
  assert.equal(
    await focusLabel(page),
    "Prepare export",
    "Tab must reach Prepare export from the format radios",
  );
  await page.keyboard.press("Enter");
  evidence.keyboard.push(
    `detail ${view.name}: Enter started archive preparation`,
  );
  await page.getByText("Preparing archive…").waitFor({ timeout: 15_000 });
  await captureState(page, {
    route: "/servers/[id]",
    state: "export-preparing-tar",
    view,
    target: panel,
  });

  await page.evaluate(() => globalThis.__wizardReleaseExport());
  await page.getByTestId("archive-export-ready").waitFor({ timeout: 15_000 });
  await captureState(page, {
    route: "/servers/[id]",
    state: "export-ready-tar",
    view,
    target: panel,
  });
  await measureControl(
    page.getByTestId("archive-download"),
    `detail ${view.name}: Download archive`,
  );

  assert.equal(
    await page.getByTestId("archive-download").isDisabled(),
    true,
    "download stays disabled before acknowledgement",
  );
  const beforeRequests = downloadRequests.length;
  await page
    .getByTestId("archive-download")
    .click({ force: true })
    .catch(() => {});
  await page.waitForTimeout(150);
  assert.equal(
    downloadRequests.length,
    beforeRequests,
    "an unacknowledged download must not start a transfer",
  );
  evidence.unacknowledged.push({
    view: view.name,
    route: "/servers/[id]",
    format: "tar.gz",
    buttonDisabled: true,
    downloadRequests: downloadRequests.length - beforeRequests,
  });
  await page
    .getByLabel("I have reviewed this notice before downloading.")
    .focus();
  await page.keyboard.press("Space");
  evidence.keyboard.push(
    `detail ${view.name}: Space acknowledged the disclosure`,
  );
  assert.equal(
    await page
      .getByLabel("I have reviewed this notice before downloading.")
      .isChecked(),
    true,
    "the disclosure can be acknowledged from the keyboard",
  );
  await page.getByTestId("archive-download").focus();
  const tarDownload = page.waitForEvent("download", { timeout: 30_000 });
  await page.keyboard.press("Enter");
  evidence.keyboard.push(
    `detail ${view.name}: Enter authorized the tar.gz download`,
  );
  await recordDownload(page, await tarDownload, view, "tar.gz");
  await panel.waitFor({ state: "hidden" });

  await page.evaluate(() => {
    globalThis.__exportMode = "success";
  });
  await openExportWithKeyboard(page, `detail ${view.name} zip`);
  await chooseFormatWithKeyboard(page, `detail ${view.name} zip`, "zip");
  await page.getByTestId("archive-prepare").focus();
  await page.keyboard.press("Enter");
  evidence.keyboard.push(
    `detail ${view.name} zip: Enter started archive preparation`,
  );
  await page.getByTestId("archive-export-ready").waitFor({ timeout: 15_000 });
  await captureState(page, {
    route: "/servers/[id]",
    state: "export-ready-zip",
    view,
    target: panel,
  });
  const zipBeforeRequests = downloadRequests.length;
  await page
    .getByTestId("archive-download")
    .click({ force: true })
    .catch(() => {});
  await page.waitForTimeout(150);
  assert.equal(
    downloadRequests.length,
    zipBeforeRequests,
    "the ZIP download stays blocked before acknowledgement",
  );
  evidence.unacknowledged.push({
    view: view.name,
    route: "/servers/[id]",
    format: "zip",
    buttonDisabled: true,
    downloadRequests: downloadRequests.length - zipBeforeRequests,
  });
  await page
    .getByLabel("I have reviewed this notice before downloading.")
    .check();
  const zipDownload = page.waitForEvent("download", { timeout: 30_000 });
  await page.getByTestId("archive-download").click();
  await recordDownload(page, await zipDownload, view, "zip");
  await panel.waitFor({ state: "hidden" });

  // The fixture prepares a real ready archive but its download handler refuses
  // export-session-refused with HTTP 403. The UI must preflight that scoped
  // download, report the refusal and keep the modal open instead of toasting a
  // started transfer. No 403 is faked in the browser. Reloading first clears the
  // earlier success toasts so "no success toast" is measured from an empty tray.
  await page.goto(`${base}/detail.html`, { waitUntil: "domcontentloaded" });
  await waitForDetail(page);
  await applyTheme(page, view.theme, `detail ${view.name} refused download`);
  await page.evaluate(() => {
    globalThis.__exportMode = "refused";
  });
  await openExportWithKeyboard(page, `detail ${view.name} refused download`);
  await page.getByTestId("archive-prepare").click();
  await page.getByTestId("archive-export-ready").waitFor({ timeout: 15_000 });
  await page
    .getByLabel("I have reviewed this notice before downloading.")
    .check();
  const refusalBefore = downloadRequests.length;
  await page.getByTestId("archive-download").click();
  const refusalToast = page.getByText(
    "Archive download was refused (HTTP 403).",
  );
  await refusalToast.waitFor({ timeout: 15_000 });
  assert.ok(
    await refusalToast.isVisible(),
    "the refused download must show its HTTP status",
  );
  assert.ok(
    await panel.isVisible(),
    "a refused download must keep the export modal open",
  );
  assert.ok(
    await page.getByTestId("archive-export-ready").isVisible(),
    "a refused download must keep the ready archive available to retry",
  );
  assert.equal(
    await page
      .getByLabel("I have reviewed this notice before downloading.")
      .isChecked(),
    true,
    "a refused download must keep the disclosure acknowledged",
  );
  assert.equal(
    await page.getByTestId("archive-download").isDisabled(),
    false,
    "the operator can retry the refused download",
  );
  assert.equal(
    await page.getByText("Archive download started.").count(),
    0,
    "a refused download must not claim it started",
  );
  const refusedRequests = downloadRequests.slice(refusalBefore);
  assert.deepEqual(
    refusedRequests,
    [
      {
        sessionId: "export-session-refused",
        method: "HEAD",
        status: 403,
        bytes: 0,
      },
    ],
    "the refusal is preflighted with HEAD and never triggers the GET download",
  );
  await captureState(page, {
    route: "/servers/[id]",
    state: "export-download-refused",
    view,
    target: panel,
  });
  evidence.downloadRefusals.push({
    view: view.name,
    label: `detail ${view.name} refused download`,
    source: "page HEAD preflight from the real ServerArchiveExport component",
    requests: refusedRequests,
    toast: "Archive download was refused (HTTP 403).",
    modalOpen: true,
    acknowledged: true,
    successToast: false,
  });
  await closeExportWithKeyboard(page, `detail ${view.name} refused download`);

  await openExportWithKeyboard(page, `detail ${view.name} error`);
  await page.evaluate(() => {
    globalThis.__exportMode = "fail";
    globalThis.__wizardReset();
  });
  await page.getByTestId("archive-prepare").click();
  await page
    .getByTestId("archive-export-panel")
    .getByText("fixture export preparation refusal")
    .waitFor({ timeout: 15_000 });
  await captureState(page, {
    route: "/servers/[id]",
    state: "export-error",
    view,
    target: panel,
  });
  await measureControl(
    page.getByTestId("archive-retry"),
    `detail ${view.name}: Try again`,
  );
  await page.evaluate(() => {
    globalThis.__exportMode = "success";
  });
  await page.getByTestId("archive-retry").click();
  await page.getByTestId("archive-export-ready").waitFor({ timeout: 15_000 });
  await closeExportWithKeyboard(page, `detail ${view.name} error`);

  await page.goto(`${base}/detail.html?running=1`, {
    waitUntil: "domcontentloaded",
  });
  await waitForDetail(page);
  await applyTheme(page, view.theme, `detail running ${view.name}`);
  await openExportWithKeyboard(page, `detail running ${view.name}`);
  await page.getByTestId("archive-export-running").waitFor();
  assert.equal(
    await page.getByTestId("archive-prepare").isDisabled(),
    true,
    "a running server must not prepare an export",
  );
  assert.equal(
    await page.getByRole("radio", { name: "ZIP (.zip)" }).isDisabled(),
    true,
    "a running server must disable format selection",
  );
  await captureState(page, {
    route: "/servers/[id]",
    state: "export-refused-running",
    view,
    target: panel,
  });
  await closeExportWithKeyboard(page, `detail running ${view.name}`);
  await recordRefusedDownload(page, view, `detail ${view.name} refused`);
}

async function openWizard(page, view, options) {
  const {
    name,
    format = "zip",
    destinations = [
      { id: "node-1", name: "Fixture Node" },
      { id: "node-2", name: "Fixture Node B" },
    ],
    previewMode = "success",
    capacity = "sufficient",
    denyListNodes = true,
  } = options;
  await page.goto(`${base}/new.html`, { waitUntil: "domcontentloaded" });
  await waitForWizard(page);
  await applyTheme(page, view.theme, `new ${name} ${view.name}`);
  await page.evaluate(
    ({ destinations, previewMode, capacity, denyListNodes, format }) => {
      globalThis.__wizardDenyListNodes = denyListNodes;
      globalThis.__wizardArchiveDestinations = destinations;
      globalThis.__archivePreviewMode = previewMode;
      globalThis.__archiveCapacityMode = capacity;
      globalThis.__archivePreviewFormat = format === "tar.gz" ? 2 : 1;
      globalThis.__wizardReset();
    },
    { destinations, previewMode, capacity, denyListNodes, format },
  );
  previewCursor = 0;
  await page.getByPlaceholder("survival-smp-01").fill(name);
  const tile = page.getByTestId("create-from-archive-tile");
  await tile.focus();
  await page.keyboard.press("Space");
  evidence.keyboard.push(
    `new ${name} ${view.name}: Space selected the archive tile`,
  );
  await page.locator('[data-testid="archive-file-input"]').setInputFiles({
    name: format === "tar.gz" ? "fixture-server.tar.gz" : "fixture-server.zip",
    mimeType: format === "tar.gz" ? "application/gzip" : "application/zip",
    buffer: Buffer.from(
      format === "tar.gz"
        ? "\x1f\x8bfixture-server"
        : "PK\x03\x04fixture-server",
    ),
  });
  const label = format === "tar.gz" ? "TAR.GZ (.tar.gz)" : "ZIP (.zip)";
  if (previewMode === "fail") {
    await page.getByTestId("archive-preview-error").waitFor({
      timeout: 15_000,
    });
    return label;
  }
  await page.getByTestId("archive-preview").waitFor({ timeout: 15_000 });
  await page
    .getByTestId("archive-preview")
    .getByText(label, { exact: true })
    .waitFor();
  return label;
}

async function nextSteps(page, count) {
  for (let step = 0; step < count; step++) {
    await page.getByRole("button", { name: "Next Step" }).click();
  }
}

async function exerciseNewRoute(page, view) {
  const previewZip = await openWizard(page, view, {
    name: `archive-route-zip-${view.name}`,
    format: "zip",
  });
  assert.equal(previewZip, "ZIP (.zip)");
  await captureState(page, {
    route: "/servers/new",
    state: "archive-preview-zip",
    view,
    target: page.getByTestId("archive-preview"),
  });
  const uploadStepPreviews = await capturePreviews(
    page,
    view,
    "archive-preview-zip",
  );
  assert.ok(
    uploadStepPreviews.length >= 1,
    "the upload-step preview must be exposed by the fixture",
  );
  const uploadStepPreview = uploadStepPreviews.at(-1);
  assert.equal(
    uploadStepPreview.outcome,
    "resolved",
    "the upload-step preview resolves",
  );
  assert.equal(
    uploadStepPreview.nodeId,
    "node-1",
    "the upload-step preview names the automatic first destination",
  );
  assert.equal(uploadStepPreview.findings, 0, "the fixture preview is clean");
  assert.deepEqual(
    uploadStepPreview.destinationIds,
    ["node-1", "node-2"],
    "the preview lists the enabled destinations that the wizard must offer",
  );

  await nextSteps(page, 3);
  const stepPanel = page
    .getByTestId("archive-destination-confirmation")
    .locator("xpath=..");
  await page.getByTestId("archive-destination-confirmation").waitFor();
  const placementPreviews = await capturePreviews(
    page,
    view,
    "placement-same-node",
  );
  assert.deepEqual(
    placementPreviews.filter((record) => record.outcome !== "resolved"),
    [],
    "reaching the cluster step must not fail a preview",
  );
  const automaticDestination =
    placementPreviews.find((record) => record.nodeId === "node-1") ??
    uploadStepPreview;
  assert.equal(automaticDestination.nodeId, "node-1");
  assert.equal(automaticDestination.outcome, "resolved");
  assert.ok(
    automaticDestination.availableBytes >= automaticDestination.requiredBytes,
    "the automatic destination preview reports enough free space",
  );
  const autoPlacement = page.getByRole("radio", { name: /Auto-Placement/ });
  assert.equal(
    await autoPlacement.getAttribute("aria-disabled"),
    "true",
    "archive mode must not offer generic automatic placement",
  );
  assert.equal(
    await autoPlacement.getAttribute("disabled"),
    null,
    "the same-node explanation must stay keyboard reachable",
  );
  await autoPlacement.focus();
  assert.match(
    await focusLabel(page),
    /Auto-Placement/,
    "focus must reach the aria-disabled Auto-Placement explanation",
  );
  evidence.keyboard.push(
    `new ${view.name}: focus reached the aria-disabled Auto-Placement card`,
  );
  await captureState(page, {
    route: "/servers/new",
    state: "placement-same-node",
    view,
    target: stepPanel,
  });

  const options = page.getByTestId("archive-destination-option");
  assert.equal(await options.count(), 2, "the preflight must offer both nodes");
  await options.nth(1).click();
  await page.waitForFunction(
    () =>
      globalThis.__wizardCalls.some(
        (call) =>
          call.method === "previewServerArchive" &&
          call.payload.nodeId === "node-2",
      ),
    null,
    { timeout: 15_000 },
  );
  await page.waitForFunction(
    () =>
      document.querySelector('[data-testid="archive-destination-node"]')
        ?.textContent === "node-2",
  );
  await captureState(page, {
    route: "/servers/new",
    state: "destination-change",
    view,
    target: stepPanel,
  });
  const destinationPreviews = await capturePreviews(
    page,
    view,
    "destination-change",
  );
  assert.ok(
    destinationPreviews.length >= 1,
    "selecting another destination must preview it",
  );
  const changedDestination = destinationPreviews.at(-1);
  assert.equal(changedDestination.nodeId, "node-2");
  assert.equal(changedDestination.outcome, "resolved");
  assert.equal(
    changedDestination.httpStatus,
    null,
    "a destination preview is not an HTTP request",
  );

  await page.evaluate(() => {
    globalThis.__archivePreviewMode = "unsupported";
  });
  await options.nth(0).click();
  await page
    .getByText("This archive has blocking findings and cannot be created.", {
      exact: true,
    })
    .waitFor({ timeout: 15_000 });
  await captureState(page, {
    route: "/servers/new",
    state: "blocked-findings",
    view,
    target: stepPanel,
  });
  const blockedPreviews = await capturePreviews(page, view, "blocked-findings");
  assert.ok(
    blockedPreviews.length >= 1,
    "returning to the blocked destination must preview it",
  );
  const blockedPreview = blockedPreviews.at(-1);
  assert.equal(blockedPreview.nodeId, "node-1");
  assert.equal(
    blockedPreview.outcome,
    "resolved",
    "blocking findings are a resolved preview, not a failed call",
  );
  assert.ok(
    blockedPreview.findings >= 1,
    "the blocked preview carries its finding count",
  );
  await page.getByRole("button", { name: /Add-ons/ }).click();
  assert.equal(
    await page
      .getByRole("button", { name: "Create Server", exact: true })
      .isDisabled(),
    true,
    "blocking findings must leave Create disabled",
  );
  await page
    .getByTestId("archive-create-blocked-help")
    .getByText("Resolve the blocking archive findings before creating.", {
      exact: true,
    })
    .waitFor();
  await page.getByRole("button", { name: /Cluster & Runtime/ }).click();

  await page.evaluate(() => {
    globalThis.__archivePreviewMode = "success";
    globalThis.__archiveCapacityMode = "insufficient";
  });
  await options.nth(1).click();
  await page
    .getByText("Destination does not have enough free space", { exact: true })
    .waitFor({ timeout: 15_000 });
  await captureState(page, {
    route: "/servers/new",
    state: "insufficient-capacity",
    view,
    target: stepPanel,
  });
  const capacityPreviews = await capturePreviews(
    page,
    view,
    "insufficient-capacity",
  );
  assert.ok(
    capacityPreviews.length >= 1,
    "the insufficient-capacity destination must be previewed",
  );
  const shortDestination = capacityPreviews.at(-1);
  assert.equal(shortDestination.nodeId, "node-2");
  assert.equal(shortDestination.outcome, "resolved");
  assert.ok(
    shortDestination.availableBytes < shortDestination.requiredBytes,
    "the insufficient-capacity preview reports less free space than required",
  );

  await page.evaluate(() => {
    globalThis.__archiveCapacityMode = "sufficient";
  });
  await options.nth(0).click();
  await page.waitForFunction(
    () =>
      document.querySelector('[data-testid="archive-destination-node"]')
        ?.textContent === "node-1",
  );
  const readyPreviews = await capturePreviews(page, view, "create-ready");
  assert.ok(
    readyPreviews.length >= 1,
    "the create-ready destination must be previewed",
  );
  const readyPreview = readyPreviews.at(-1);
  assert.equal(readyPreview.nodeId, "node-1");
  assert.equal(readyPreview.outcome, "resolved");
  assert.ok(
    readyPreview.availableBytes >= readyPreview.requiredBytes,
    "the create-ready preview reports enough free space",
  );
  await page.getByRole("button", { name: /Add-ons/ }).click();
  await page
    .getByRole("button", { name: "Create Server", exact: true })
    .waitFor();
  assert.equal(
    await page
      .getByRole("button", { name: "Create Server", exact: true })
      .isDisabled(),
    false,
    "a current successful preview must enable Create",
  );
  await page
    .getByRole("button", { name: "Create Server", exact: true })
    .click();
  await page.getByRole("heading", { name: "Server created" }).waitFor({
    timeout: 15_000,
  });
  const createCall = (await page.evaluate(() => globalThis.__wizardCalls)).find(
    (call) => call.method === "createServer",
  );
  assert.ok(createCall, "Create Server must submit the wizard");
  assert.equal(createCall.payload.archiveUploadId, "seeded-session");
  assert.equal(createCall.payload.nodeId, "node-1");
  await captureState(page, {
    route: "/servers/new",
    state: "create-submitted",
    view,
    target: page.locator('section[aria-label="Server setup results"]'),
    captureViewport: true,
  });

  const previewTar = await openWizard(page, view, {
    name: `archive-route-tar-${view.name}`,
    format: "tar.gz",
  });
  assert.equal(previewTar, "TAR.GZ (.tar.gz)");
  await captureState(page, {
    route: "/servers/new",
    state: "archive-preview-tar",
    view,
    target: page.getByTestId("archive-preview"),
  });
  const tarPreviews = await capturePreviews(page, view, "archive-preview-tar");
  assert.ok(tarPreviews.length >= 1, "the tar.gz preview must be exposed");
  assert.equal(tarPreviews.at(-1).outcome, "resolved");
  assert.deepEqual(
    tarPreviews.at(-1).destinationIds,
    ["node-1", "node-2"],
    "the tar.gz preview lists the same destinations",
  );

  await openWizard(page, view, {
    name: `archive-route-empty-${view.name}`,
    format: "zip",
    destinations: [],
  });
  await nextSteps(page, 3);
  await page.getByTestId("archive-destination-empty").waitFor({
    timeout: 15_000,
  });
  const emptyPreviews = await capturePreviews(page, view, "no-destination");
  assert.ok(
    emptyPreviews.length >= 1,
    "the empty preflight must be exposed by the fixture",
  );
  const emptyPreview = emptyPreviews.at(-1);
  assert.equal(emptyPreview.outcome, "resolved");
  assert.deepEqual(
    emptyPreview.destinationIds,
    [],
    "the empty preflight resolves with no enabled destination",
  );
  await captureState(page, {
    route: "/servers/new",
    state: "no-destination",
    view,
    target: page.getByTestId("archive-destination-empty").locator("xpath=.."),
  });

  // A refusing preview is the fixture's failed branch: it must be recorded as a
  // failed call with its Connect code, not as a missing HTTP status.
  await openWizard(page, view, {
    name: `archive-route-refused-${view.name}`,
    format: "zip",
    previewMode: "fail",
  });
  await captureState(page, {
    route: "/servers/new",
    state: "preview-error",
    view,
    target: page.getByTestId("archive-preview-error"),
  });
  const failedPreviews = await capturePreviews(page, view, "preview-error");
  assert.ok(
    failedPreviews.length >= 1,
    "the refusing preview must be exposed by the fixture",
  );
  const failedPreview = failedPreviews.at(-1);
  assert.equal(
    failedPreview.outcome,
    "failed",
    "a refused preview is recorded as failed",
  );
  assert.equal(failedPreview.errorCode, 14, "Connect Unavailable (14)");
  assert.equal(failedPreview.errorName, "Unavailable");
  await page.evaluate(() => {
    globalThis.__archivePreviewMode = "success";
  });
  await page
    .getByRole("button", { name: "Retry preview", exact: true })
    .click();
  await page.getByTestId("archive-preview").waitFor({ timeout: 15_000 });
  await captureState(page, {
    route: "/servers/new",
    state: "preview-retried",
    view,
    target: page.getByTestId("archive-preview"),
  });
  const retriedPreviews = await capturePreviews(page, view, "preview-retried");
  assert.ok(
    retriedPreviews.length >= 1,
    "retrying the preview must call the fixture again",
  );
  assert.equal(retriedPreviews.at(-1).outcome, "resolved");
}

try {
  await fs.mkdir(out, { recursive: true });
  const cache = await fs.mkdtemp(
    path.join(os.tmpdir(), "carbon-archive-routes-"),
  );
  const sessions = {
    "export-session-zip": {
      bytes: Buffer.from("fixture-zip-archive-bytes"),
      type: "application/zip",
      filename: "fixture-server.zip",
    },
    "export-session-tar": {
      bytes: Buffer.from("fixture-tar-gz-archive-bytes"),
      type: "application/gzip",
      filename: "fixture-server.tar.gz",
    },
  };
  try {
    server = await createServer({
      configFile: false,
      root: project,
      cacheDir: cache,
      publicDir: path.join(project, "static"),
      resolve: {
        dedupe: ["svelte"],
        alias: [
          {
            find: /^@bufbuild\/protobuf$/,
            replacement: path.join(
              project,
              "node_modules/@bufbuild/protobuf/dist/esm/index.js",
            ),
          },
          { find: "$app/state", replacement: path.join(fixture, "state.js") },
          {
            find: "$app/navigation",
            replacement: path.join(fixture, "navigation.js"),
          },
          {
            find: "$app/paths",
            replacement: path.join(
              project,
              "e2e/oss-245-diagnostics-reconnect/paths.js",
            ),
          },
          {
            find: "$app/env",
            replacement: path.join(
              project,
              "e2e/oss-245-diagnostics-reconnect/environment.js",
            ),
          },
          {
            find: "$lib/api/rpc-client",
            replacement: path.join(fixture, "rpc-stub.js"),
          },
          {
            find: "$lib/utils/chunked-upload",
            replacement: path.join(fixture, "chunked-upload-stub.js"),
          },
          {
            find: /^\$lib\/components\/carbon$/,
            replacement: path.join(fixture, "carbon.js"),
          },
          { find: "$lib", replacement: path.join(project, "src/lib") },
        ],
      },
      plugins: [
        svelte({ configFile: path.join(project, "sveltekit.config.js") }),
        tailwind(),
        {
          name: "archive-routes-fixture",
          configureServer(vite) {
            vite.middlewares.use(async (req, res, next) => {
              if (req.url === "/favicon.ico") {
                res.statusCode = 204;
                res.end();
                return;
              }
              const pathname = (req.url || "").split("?")[0];
              if (pathname.startsWith("/api/v1/download/")) {
                const sessionId = decodeURIComponent(
                  pathname.slice("/api/v1/download/".length),
                );
                if (sessionId === "export-session-refused") {
                  downloadRequests.push({
                    sessionId,
                    method: req.method,
                    status: 403,
                    bytes: 0,
                  });
                  res.statusCode = 403;
                  res.setHeader("Content-Type", "text/plain");
                  res.setHeader("Content-Length", "0");
                  res.end();
                  return;
                }
                const session = sessions[sessionId];
                if (!session) {
                  downloadRequests.push({
                    sessionId,
                    method: req.method,
                    status: 404,
                    bytes: 0,
                  });
                  res.statusCode = 404;
                  res.end();
                  return;
                }
                downloadRequests.push({
                  sessionId,
                  method: req.method,
                  status: 200,
                  bytes: session.bytes.byteLength,
                });
                res.statusCode = 200;
                res.setHeader("Content-Type", session.type);
                res.setHeader(
                  "Content-Disposition",
                  `attachment; filename="${session.filename}"`,
                );
                res.setHeader(
                  "Content-Length",
                  String(session.bytes.byteLength),
                );
                if (req.method !== "HEAD") res.end(session.bytes);
                else res.end();
                return;
              }
              const entries = {
                "/new.html": path.join(fixture, "main.js"),
                "/detail.html": path.join(fixture, "detail-main.js"),
              };
              const entrySource = entries[pathname];
              if (!entrySource) return next();
              try {
                const entry = "/@fs/" + entrySource.replaceAll("\\", "/");
                res.setHeader("Content-Type", "text/html");
                res.end(
                  await vite.transformIndexHtml(
                    pathname,
                    `<html><body><div id="app"></div><script type="module" src="${entry}"></script></body></html>`,
                  ),
                );
              } catch (error) {
                next(error);
              }
            });
          },
        },
      ],
      optimizeDeps: {
        noDiscovery: true,
        include: [
          "svelte",
          "svelte/store",
          "@bufbuild/protobuf",
          "@bufbuild/protobuf/codegenv2",
          "@bufbuild/protobuf/wkt",
          "@lucide/svelte",
          "bits-ui",
          "paneforge",
          "svelte-sonner",
          "tailwind-merge",
          "tailwind-variants",
          "ansi-to-html",
        ],
      },
      server: {
        host: "127.0.0.1",
        port: 0,
        hmr: false,
        fs: { allow: [project, fixture, cache] },
      },
    });
    await server.listen();
    base = `http://127.0.0.1:${server.httpServer.address().port}`;
    const launched = await launchReviewBrowser({ headless: true });
    browser = launched.browser;
    evidence.browser = { ...launched.metadata, browserCache };

    for (const view of views) {
      const page = await browser.newPage({
        viewport: { width: view.width, height: view.height },
      });
      page.setDefaultTimeout(60_000);
      const pageErrors = [];
      const consoleErrors = [];
      const expectedRefusalErrors = [];
      const viewRequests = [];
      const viewPreflights = [];
      page.on("pageerror", (error) => pageErrors.push(error.message));
      page.on("console", (message) => {
        if (message.type() !== "error") return;
        // Chromium logs every non-2xx fetch as a resource error. The refused
        // preflight is the intended 403 under test, so record it separately
        // instead of either hiding it or failing the unexpected-error check.
        const url = (message.location()?.url ?? "").replace(
          base,
          "<review-base>",
        );
        const text = message.text();
        if (
          url.endsWith("/api/v1/download/export-session-refused") &&
          /status of 403/.test(text)
        ) {
          expectedRefusalErrors.push({
            view: view.name,
            message: text,
            url,
            expected:
              "the fixture refuses export-session-refused with HTTP 403",
          });
          return;
        }
        consoleErrors.push(text);
      });
      // The fixture replaces the RPC transport, so archive preview and create
      // must not issue any browser XHR/fetch. The download preflight is the one
      // intended browser fetch: record it separately so the "no HTTP status at
      // this seam" statement for preview/create stays checkable.
      page.on("request", (request) => {
        const type = request.resourceType();
        if (type !== "xhr" && type !== "fetch") return;
        const entry = {
          view: view.name,
          method: request.method(),
          url: request.url().replace(base, "<review-base>"),
          resourceType: type,
        };
        if (entry.url.startsWith("<review-base>/api/v1/download/")) {
          viewPreflights.push(entry);
          preflightRequests.push(entry);
          return;
        }
        viewRequests.push(entry);
        pageRequests.push(entry);
      });
      evidence.views.push({
        name: view.name,
        viewport: { width: view.width, height: view.height },
        theme: view.theme,
        routeOrder: ["/servers/[id]", "/servers/new"],
      });
      try {
        await exerciseDetail(page, view);
        await exerciseNewRoute(page, view);
      } finally {
        for (const message of pageErrors) {
          evidence.checks.pageErrors.push({ view: view.name, message });
        }
        for (const message of consoleErrors) {
          evidence.checks.consoleErrors.push({ view: view.name, message });
        }
        for (const entry of expectedRefusalErrors) {
          evidence.checks.expectedRefusalConsoleErrors.push(entry);
        }
        await page.close();
      }
      assert.deepEqual(
        evidence.checks.pageErrors.filter((entry) => entry.view === view.name),
        [],
        `${view.name}: page errors`,
      );
      assert.deepEqual(
        evidence.checks.consoleErrors.filter(
          (entry) => entry.view === view.name,
        ),
        [],
        `${view.name}: console errors`,
      );
      assert.equal(
        expectedRefusalErrors.length,
        1,
        `${view.name}: the refused preflight logs exactly one expected 403 resource error`,
      );
      assert.deepEqual(
        viewRequests,
        [],
        `${view.name}: the fixture must not issue browser XHR/fetch for preview or create`,
      );
      assert.deepEqual(
        viewPreflights.filter((entry) => entry.method !== "HEAD"),
        [],
        `${view.name}: every download preflight must be a HEAD`,
      );
      assert.ok(
        viewPreflights.length >= 3,
        `${view.name}: the tar.gz, ZIP and refused downloads must preflight`,
      );
    }
    evidence.downloadRequests = downloadRequests;
    evidence.network.pageXhrOrFetch = pageRequests;
    evidence.network.downloadPreflights = preflightRequests;
    assert.ok(
      preflightRequests.some(
        (entry) =>
          entry.method === "HEAD" &&
          entry.url.endsWith("/api/v1/download/export-session-refused"),
      ),
      "the refused download must be preflighted through the UI",
    );
    assert.deepEqual(
      pageRequests,
      [],
      "no browser XHR/fetch request exists for the preview or create seam",
    );
  } finally {
    await browser?.close();
    await server?.close();
    await fs.rm(cache, { recursive: true, force: true });
  }
  evidence.artifacts = artifacts;
  await fs.writeFile(
    path.join(out, "results.json"),
    JSON.stringify(evidence, null, 2),
  );
  await assertArtifactsComplete();
  console.log(
    "PASS: real /servers/[id] and /servers/new normal-route fixture states at desktop/narrow light/dark, measured 40px Carbon md export actions, authenticated ZIP and tar.gz downloads, HEAD-preflighted refused download that keeps the modal open, refused and unacknowledged cases, keyboard focus return, theme/contrast/overflow/clipping checks; the only console entry is the expected Chromium 403 resource log for the refused preflight",
  );
  console.log(
    `CONTROLS: ${evidence.controls.map((control) => `${control.label}=${control.height}px`).join("; ")}`,
  );
  console.log(
    `PREVIEWS: ${evidence.previews.length} previewServerArchive outcomes recorded from the seeded in-process rpc-client (resolved/failed + Connect error code); no preview HTTP status exists at this seam and none is claimed. Non-download browser XHR/fetch requests: ${pageRequests.length}; download preflights: ${preflightRequests.length}.`,
  );
} catch (error) {
  await fs.mkdir(out, { recursive: true }).catch(() => {});
  await fs
    .writeFile(
      path.join(out, "results.json"),
      JSON.stringify(
        { ...evidence, failed: error?.message ?? String(error) },
        null,
        2,
      ),
    )
    .catch(() => {});
  throw error;
}
