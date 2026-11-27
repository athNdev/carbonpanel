// SPDX-License-Identifier: MPL-2.0
// Seeded Chromium fixture for the create-server wizard datapack upload slice
// and the server archive export control.
//
// Drives the real /servers/new route and the real archive components through
// the normal UI seam. This fixture replaces the RPC transport, the resumable
// chunked-upload helper and SvelteKit's app runtime modules, and narrows the
// Carbon barrel; the route components, archive components, Carbon controls and
// application CSS remain the production implementation. No live panel,
// credentials, worlds, daemon or external infrastructure.
//
//   REVIEW_CHROME=/path/to/chrome.exe \
//     node scripts/review/wizard-datapack-upload-check.mjs

import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { createRequire } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";
import { launchReviewBrowser } from "./browser.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../..");
// Evidence names the commit it was produced from, resolved independently from
// the worktree rather than copied from a ticket or commit message.
const verifiedHead = execFileSync("git", ["rev-parse", "HEAD"], {
  cwd: repoRoot,
  encoding: "utf8",
}).trim();
const worktreeStatus = execFileSync("git", ["status", "--porcelain"], {
  cwd: repoRoot,
  encoding: "utf8",
}).trim();
const project = path.resolve(here, "../../web/carbon-panel");
const fixture = path.join(project, "e2e", "wizard-datapack-upload");
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

const cache = await fs.mkdtemp(
  path.join(os.tmpdir(), "carbon-wizard-datapack-"),
);
const downloadRequests = [];
let server;
let browser;

async function waitForFixtureReady(page) {
  let lastError;
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      await page.waitForFunction(
        () => Boolean(globalThis.__wizardCalls),
        null,
        {
          timeout: 25_000,
        },
      );
      await page
        .getByRole("button", { name: "Next Step" })
        .waitFor({ timeout: 10_000 });
      return;
    } catch (error) {
      lastError = error;
      if (attempt < 2) await page.reload();
    }
  }
  throw lastError;
}

async function waitForExportReady(page) {
  await page.waitForFunction(() => Boolean(globalThis.__wizardCalls), null, {
    timeout: 25_000,
  });
  await page.getByTestId("server-export-archive").waitFor({ timeout: 10_000 });
}

async function goToDatapackStep(page) {
  await page
    .getByPlaceholder("survival-smp-01")
    .fill("wizard-datapack-fixture");
  for (let step = 0; step < 4; step++) {
    await page.getByRole("button", { name: "Next Step" }).click();
  }
  await page.getByText("Upload local datapack", { exact: true }).waitFor();
}

async function uploadFixtureArchive(page) {
  await page.locator('input[type="file"]').setInputFiles({
    name: "fixture-pack.zip",
    mimeType: "application/zip",
    buffer: Buffer.from("PK\x03\x04fixture"),
  });
}

async function uploadServerArchive(page, format = "zip") {
  const tar = format === "tar.gz";
  await page.locator('[data-testid="archive-file-input"]').setInputFiles({
    name: tar ? "fixture-server.tar.gz" : "fixture-server.zip",
    mimeType: tar ? "application/gzip" : "application/zip",
    buffer: Buffer.from(
      tar ? "\x1f\x8bfixture-server" : "PK\x03\x04fixture-server",
    ),
  });
}

async function showWizardStep(page, title) {
  await page.getByRole("button", { name: new RegExp(title) }).click();
}

// Color measurement uses the rendered token values, including any ancestor
// opacity, so a dimmed explanatory card cannot pass just because its source
// class name looks semantic.
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

async function renderedContrast(locator, { includeOpacity = true } = {}) {
  const sample = await locator.evaluate(
    (element, withOpacity) => {
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
      let background = [255, 255, 255, 1];
      let accumulatedOpacity = 1;
      for (let node = element; node; node = node.parentElement) {
        const style = getComputedStyle(node);
        if (withOpacity) accumulatedOpacity *= Number(style.opacity || 1);
        const parsed = parse(style.backgroundColor);
        if (parsed && parsed[3] > 0) {
          background = composite(parsed, background);
          if (parsed[3] === 1) break;
        }
      }
      const foreground = parse(getComputedStyle(element).color) || [0, 0, 0, 1];
      if (withOpacity && accumulatedOpacity < 1) {
        foreground[3] *= accumulatedOpacity;
      }
      const effectiveForeground =
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
      return {
        foreground: effectiveForeground,
        background,
        sourceColor: getComputedStyle(element).color,
        sourceBackground: background,
      };
    },
    includeOpacity,
  );
  return contrastRatio(
    sample.foreground.slice(0, 3),
    sample.background.slice(0, 3),
  );
}

async function keyboardFocusLabel(page) {
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

const keyboard = [];
const summaries = [];

let runError;

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
        name: "wizard-datapack-upload",
        configureServer(vite) {
          vite.middlewares.use(async (req, res, next) => {
            if (req.url === "/favicon.ico") {
              res.statusCode = 204;
              res.end();
              return;
            }
            const pathname = (req.url || "").split("?")[0];
            if (pathname.startsWith("/api/v1/download/")) {
              downloadRequests.push({
                url: req.url,
                method: req.method,
                status: 200,
                contentType: "application/gzip",
              });
              res.statusCode = 200;
              res.setHeader("Content-Type", "application/gzip");
              res.setHeader(
                "Content-Disposition",
                'attachment; filename="fixture-server.tar.gz"',
              );
              res.end("current-tar-bytes");
              return;
            }
            const entries = {
              "/wizard.html": path.join(fixture, "main.js"),
              "/export.html": path.join(fixture, "export-main.js"),
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
  const base = `http://127.0.0.1:${server.httpServer.address().port}/wizard.html`;
  const launched = await launchReviewBrowser({ headless: true });
  browser = launched.browser;
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1000 },
  });
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));

  const out = process.env.REVIEW_ARCHIVE_OUT
    ? path.resolve(process.env.REVIEW_ARCHIVE_OUT)
    : path.join(here, "../../tmp/review-output/wizard-datapack-upload");
  await fs.mkdir(out, { recursive: true });
  // Scenario 1: a successful local upload is queued and installed post-create.
  await page.goto(base);
  await waitForFixtureReady(page);
  await page.evaluate(() => {
    globalThis.__wizardUploadMode = "success";
    globalThis.__wizardReset();
  });
  await goToDatapackStep(page);
  await uploadFixtureArchive(page);
  await page.getByText("fixture-pack", { exact: true }).waitFor();
  await page.getByText("Local upload", { exact: true }).waitFor();
  assert.equal(
    (await page.evaluate(() => globalThis.__wizardUploadCalls)).length,
    1,
    "the wizard must call the uploadFile seam once",
  );
  await page
    .getByRole("button", { name: "Create Server", exact: true })
    .click();
  await page.waitForFunction(
    () =>
      globalThis.__wizardCalls.some((call) => call.method === "uploadDatapack"),
    null,
    { timeout: 15_000 },
  );
  let calls = await page.evaluate(() => globalThis.__wizardCalls);
  const createCall = calls.find((call) => call.method === "createServer");
  const uploadCall = calls.find((call) => call.method === "uploadDatapack");
  assert.ok(createCall, "createServer must be called");
  assert.deepEqual(
    {
      serverId: uploadCall.payload.serverId,
      uploadSessionId: uploadCall.payload.uploadSessionId,
      fileName: uploadCall.payload.fileName,
    },
    {
      serverId: "wizard-server",
      uploadSessionId: "seeded-session",
      fileName: "fixture-pack.zip",
    },
    "queued local archive must be installed with the upload session after create",
  );
  assert.equal(
    calls.some((call) => call.method === "installDatapack"),
    false,
    "a local-only queue must not call the indexed install path",
  );
  summaries.push({
    scenario: "success",
    calls: calls.map((call) => ({
      method: call.method,
      payload: call.payload,
    })),
    uploadCalls: await page.evaluate(() =>
      globalThis.__wizardUploadCalls.slice(),
    ),
  });

  // Scenario 2: a failed upload surfaces an error and is never queued/installed.
  await page.reload();
  await waitForFixtureReady(page);
  await page.evaluate(() => {
    globalThis.__wizardUploadMode = "fail";
    globalThis.__wizardReset();
  });
  await goToDatapackStep(page);
  await uploadFixtureArchive(page);
  await page
    .getByText("seeded upload failure", { exact: true })
    .waitFor({ timeout: 10_000 });
  assert.equal(
    await page.getByText("fixture-pack.zip", { exact: true }).count(),
    0,
    "a failed upload must not enter the queued selection list",
  );
  await page
    .getByRole("button", { name: "Create Server", exact: true })
    .click();
  await page.waitForFunction(
    () =>
      globalThis.__wizardCalls.some((call) => call.method === "createServer"),
    null,
    { timeout: 15_000 },
  );
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  assert.equal(
    calls.some((call) => call.method === "uploadDatapack"),
    false,
    "a failed upload must not be installed after server creation",
  );
  summaries.push({
    scenario: "failure",
    calls: calls.map((call) => ({
      method: call.method,
      payload: call.payload,
    })),
    uploadCalls: await page.evaluate(() =>
      globalThis.__wizardUploadCalls.slice(),
    ),
  });

  // The normal wizard can browse a non-favorite pack and pin a pre-existing release.
  await page.reload();
  await waitForFixtureReady(page);
  await page.getByRole("radio", { name: /From Modpack Preset/ }).click();
  await page.getByRole("button", { name: "Next page", exact: true }).click();
  await page.getByText("31–31 of 31", { exact: true }).waitFor();
  await page.getByRole("button", { name: /Indexed Fabric Pack/ }).click();
  await page
    .locator('#preset-version option[value="unknown-version"]:disabled')
    .waitFor({ state: "attached" });
  await page.getByLabel("Version", { exact: true }).selectOption("old-version");
  await page.getByRole("button", { name: "Next Step" }).click();
  assert.equal(
    await page.getByLabel("Minecraft Version", { exact: true }).inputValue(),
    "1.20.1",
  );
  for (let step = 0; step < 3; step++)
    await page.getByRole("button", { name: "Next Step" }).click();
  await page.getByRole("button", { name: "Mods", exact: true }).click();
  await page.getByText(/The modpack provisions its own mods/).waitFor();
  await page
    .getByRole("button", { name: "Create Server", exact: true })
    .click();
  await page.getByRole("heading", { name: "Server created" }).waitFor();
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  const pinned = calls.find((call) => call.method === "createServer").payload;
  assert.equal(pinned.modpackId, "existing-pack");
  assert.equal(pinned.modpackVersionId, "old-version");
  assert.equal(pinned.mcVersion, "1.20.1");
  assert.equal(pinned.modLoader, 15);
  summaries.push({
    scenario: "non-favorite modpack with a pinned earlier release",
    calls,
  });

  await page.reload();
  await waitForFixtureReady(page);
  await page.getByPlaceholder("survival-smp-01").fill("addon-retry-fixture");
  await page.getByRole("button", { name: "Next Step" }).click();
  await page
    .getByLabel("Mod Loader / Server Software")
    .selectOption({ label: "Fabric" });
  await page.getByRole("button", { name: "Next Step" }).click();
  await page.getByRole("button", { name: "Next Step" }).click();
  await page.getByRole("checkbox", { name: /Start Immediately/ }).check();
  await page.getByRole("button", { name: "Next Step" }).click();
  await page.getByRole("button", { name: "Mods", exact: true }).click();
  await page
    .getByRole("button", { name: "Browse & install", exact: true })
    .click();
  await page
    .getByRole("dialog", { name: "Browse compatible mods" })
    .waitFor();
  // The dialog's accessible name follows the selected mod, so keep the scope
  // on the single modal while asserting the initial and detail states by text.
  const browseDialog = page.getByRole("dialog");
  await browseDialog
    .getByText("Fixture Optimization", { exact: true })
    .waitFor();
  assert.equal(await browseDialog.getByText("Client-only Project").count(), 0);
  const openingSearch = (
    await page.evaluate(() => globalThis.__wizardCalls)
  ).find((call) => call.method === "searchMods")?.payload;
  assert.deepEqual(
    [
      openingSearch?.serverId,
      openingSearch?.loader,
      openingSearch?.mcVersion,
      openingSearch?.side,
    ],
    ["", "fabric", "1.21.1", "server"],
    "the initial catalog request must keep the same server-side filter as later searches",
  );
  await browseDialog.getByRole("button", { name: "Select Version" }).click();
  await browseDialog
    .getByRole("button", { name: "Add to server setup" })
    .click();
  await browseDialog.getByText("Selected", { exact: true }).waitFor();
  await page.keyboard.press("Escape");
  await browseDialog.waitFor({ state: "hidden" });
  const removeQueued = page.getByRole("button", {
    name: "Remove Fixture Optimization",
    exact: true,
  });
  await removeQueued.waitFor();
  const queuedCard = removeQueued.locator(
    'xpath=ancestor::div[contains(concat(" ", normalize-space(@class), " "), " bg-card ")][1]',
  );
  await queuedCard.getByText("Fixture Optimization", { exact: true }).waitFor();
  await queuedCard.getByText("v1.0", { exact: true }).waitFor();
  await queuedCard.getByText("Selected", { exact: true }).waitFor();
  await page.getByRole("button", { name: "Modules", exact: true }).click();
  await page
    .getByRole("button", { name: "Add Resource Pack Host", exact: true })
    .click();
  await page.screenshot({ path: path.join(out, "add-ons.png") });
  await page.evaluate(() => {
    globalThis.__wizardPreparingPolls = 1;
    globalThis.__wizardHoldCreate = true;
    globalThis.__wizardFailStart = true;
    globalThis.__wizardModuleMode = "error";
    globalThis.__wizardJournalLocked = true;
  });
  await page
    .getByRole("button", { name: "Create Server", exact: true })
    .click();
  await page.locator("form[inert]").waitFor();
  await page.waitForFunction(
    () => typeof globalThis.__wizardReleaseCreate === "function",
  );
  const removeModule = page.locator(
    'button[aria-label="Remove Resource Pack Host"]',
  );
  const removeBox = await removeModule.boundingBox();
  assert.ok(removeBox, "the selected module is visible while the form waits");
  await page.mouse.click(
    removeBox.x + removeBox.width / 2,
    removeBox.y + removeBox.height / 2,
  );
  assert.equal(
    await removeModule.count(),
    1,
    "inert setup controls must ignore edits during create",
  );
  await page.evaluate(() => globalThis.__wizardReleaseCreate());
  await page
    .getByText("Waiting for the server container before adding content…")
    .waitFor();
  await page
    .getByText("Fixture download interrupted.", { exact: true })
    .waitFor();
  await page
    .getByText("The module container failed to initialize.", { exact: false })
    .waitFor();
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  assert.equal(
    calls.find((call) => call.method === "createServer").payload
      .startImmediately,
    false,
  );
  assert.equal(
    calls.some((call) => call.method === "startServer"),
    false,
  );
  assert.equal(
    calls
      .slice(
        calls.findIndex((call) => call.method === "createServer") + 1,
        calls.findIndex((call) => call.method === "installMods"),
      )
      .filter((call) => call.method === "getServer").length,
    2,
  );
  assert.deepEqual(
    calls
      .filter((call) => call.method === "installMods")
      .map((call) => call.payload.items[0].fileName),
    ["fixture-mod.jar", "required-api.jar"],
  );
  const search = calls.find((call) => call.method === "searchMods").payload;
  assert.deepEqual(
    [search.serverId, search.loader, search.mcVersion, search.side],
    ["", "fabric", "1.21.1", "server"],
  );
  const moduleCall = calls.find(
    (call) => call.method === "createModule",
  ).payload;
  assert.equal(moduleCall.serverId, "wizard-server");
  assert.equal(moduleCall.templateId, "builtin-resource-pack-host");
  assert.equal(moduleCall.autoStart, false);
  assert.equal(moduleCall.startImmediately, false);
  assert.equal(JSON.parse(moduleCall.volumeOverrides)[0].read_only, true);
  await page.screenshot({ path: path.join(out, "retry.png") });
  await page
    .getByRole("button", { name: "Retry failed add-ons", exact: true })
    .click();
  await page.getByRole("button", { name: "Retry start", exact: true }).click();
  await page
    .getByText("Fixture previous start still holds its lock.")
    .waitFor();
  assert.equal(
    await page.evaluate(
      () =>
        globalThis.__wizardCalls.filter((call) => call.method === "startServer")
          .length,
    ),
    1,
    "an errored server with an in-flight journal receipt must not start again",
  );
  await page.evaluate(() => {
    globalThis.__wizardJournalLocked = false;
  });
  await page.getByRole("button", { name: "Retry start", exact: true }).click();
  await page.getByRole("link", { name: "Open server", exact: true }).waitFor();
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  assert.equal(
    calls.filter((call) => call.method === "createServer").length,
    1,
  );
  assert.equal(
    calls.filter((call) => call.method === "createModule").length,
    1,
  );
  assert.deepEqual(
    calls
      .filter((call) => call.method === "getModule")
      .map((call) => call.payload.status),
    [6, 5, 5, 1],
    "the module must be observed preparing, errored, and finally stopped",
  );
  assert.deepEqual(
    calls
      .filter((call) => call.method === "recreateModule")
      .map((call) => call.payload.id),
    ["fixture-module"],
  );
  assert.deepEqual(
    calls
      .filter((call) => call.method === "installMods")
      .map((call) => call.payload.items[0].fileName),
    ["fixture-mod.jar", "required-api.jar", "required-api.jar"],
  );
  assert.equal(calls.at(-1).method, "startServer");
  assert.equal(calls.at(-1).payload.id, "wizard-server");
  const startCalls = calls.filter((call) => call.method === "startServer");
  assert.equal(startCalls.length, 2);
  assert.notEqual(
    startCalls[0].payload.requestId,
    startCalls[1].payload.requestId,
  );
  summaries.push({
    scenario:
      "compatible mod, pinned dependency, failed sidecar and JAR, locked start refusal and journal-authorized retry from ERROR",
    calls,
  });

  await page.reload();
  await waitForFixtureReady(page);
  await goToDatapackStep(page);
  await page.getByRole("button", { name: "Modules", exact: true }).click();
  await page
    .getByRole("button", { name: "Add Resource Pack Host", exact: true })
    .click();
  await page.evaluate(() => {
    globalThis.__wizardModuleMode = "lost";
  });
  await page
    .getByRole("button", { name: "Create Server", exact: true })
    .click();
  await page
    .getByText("Fixture module response lost.", { exact: false })
    .waitFor();
  await page
    .getByRole("button", { name: "Retry failed add-ons", exact: true })
    .click();
  await page.getByRole("link", { name: "Open server", exact: true }).waitFor();
  await page.getByText("Added", { exact: true }).waitFor();
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  assert.deepEqual(
    calls
      .filter((call) =>
        ["createModule", "listModules", "getModule", "recreateModule"].includes(
          call.method,
        ),
      )
      .map((call) => call.method),
    ["createModule", "listModules", "getModule", "recreateModule", "getModule"],
    "retry must inspect and repair the existing error row after a lost create response",
  );
  summaries.push({
    scenario: "lost module response recovers its existing errored record",
    calls,
  });

  // Create-from-archive: a failed preview blocks Create, the same concrete node
  // retry succeeds, a former blueprint image is not submitted, overrides survive
  // and a refused create retries with the same archive submission id.
  await page.reload();
  await waitForFixtureReady(page);
  await page.evaluate(() => {
    globalThis.__archivePreviewMode = "fail-once";
    globalThis.__wizardReset();
  });
  await page.getByPlaceholder("survival-smp-01").fill("archive-retry-fixture");
  await page.getByRole("button", { name: /Fixture Java Seed/ }).click();
  await page.getByTestId("create-from-archive-tile").click();
  await uploadServerArchive(page);
  await page
    .getByText(/fixture archive preview refusal/)
    .waitFor({ timeout: 15_000 });
  await page.getByTestId("archive-preview-error").waitFor();
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  assert.deepEqual(
    calls
      .filter((call) => call.method === "previewServerArchive")
      .map((call) => call.payload.nodeId),
    ["node-1"],
    "archive preview must check the concrete destination selected for the import",
  );
  await showWizardStep(page, "Resources & Network");
  await page.locator("#memory").fill("4096");
  await page.locator("#max_players").fill("42");
  await showWizardStep(page, "Add-ons");
  await page.getByTestId("archive-addons-disabled").waitFor();
  assert.equal(
    await page.getByText("Upload local datapack", { exact: true }).count(),
    0,
    "archive mode must not offer standalone add-on setup",
  );
  assert.equal(
    await page
      .getByRole("button", { name: "Create Server", exact: true })
      .isDisabled(),
    true,
    "a failed preview must leave create disabled",
  );
  await page
    .getByTestId("archive-create-blocked-help")
    .getByText(/Retry the archive preview for this destination/, {
      exact: false,
    })
    .waitFor();
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  assert.equal(
    calls.some((call) => call.method === "createServer"),
    false,
    "a failed preview must block create",
  );
  await showWizardStep(page, "Basic Information");
  await page
    .getByRole("button", { name: "Retry preview", exact: true })
    .click();
  await page.getByTestId("archive-preview").waitFor({ timeout: 15_000 });
  await showWizardStep(page, "Cluster & Runtime");
  assert.equal(
    await page
      .getByRole("radio", { name: /Auto-Placement/ })
      .getAttribute("aria-disabled"),
    "true",
    "archive mode must not use generic automatic placement",
  );
  assert.equal(
    await page.getByTestId("archive-destination-node").textContent(),
    "node-1",
    "the archive destination must name the concrete node",
  );
  await showWizardStep(page, "Game Engine");
  assert.equal(
    await page.getByTestId("archive-runtime-mc").inputValue(),
    "1.20.6",
    "the engine step must show the verified archive Minecraft version",
  );
  assert.equal(
    await page.getByTestId("archive-runtime-loader").inputValue(),
    "fabric",
    "the engine step must lock the verified archive loader",
  );
  await page.evaluate(() => {
    globalThis.__wizardArchiveFailCreateOnce = true;
  });
  await showWizardStep(page, "Add-ons");
  await page
    .getByRole("button", { name: "Create Server", exact: true })
    .click();
  await page
    .getByText(/fixture archive create refusal/)
    .waitFor({ timeout: 15_000 });
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  assert.equal(
    calls.filter((call) => call.method === "createServer").length,
    1,
    "the refused create must not be followed by a hidden retry",
  );
  await page
    .getByRole("button", { name: "Create Server", exact: true })
    .click();
  await page.getByRole("heading", { name: "Server created" }).waitFor();
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  const archiveCreates = calls.filter((call) => call.method === "createServer");
  assert.equal(archiveCreates.length, 2);
  assert.equal(
    archiveCreates[0].payload.requestId,
    archiveCreates[1].payload.requestId,
    "an archive create refusal must retry with the same submission id",
  );
  for (const { payload } of archiveCreates) {
    assert.deepEqual(
      {
        archiveUploadId: payload.archiveUploadId,
        mcVersion: payload.mcVersion,
        modLoader: payload.modLoader,
        loaderVersion: payload.loaderVersion,
        memory: payload.memory,
        maxPlayers: payload.maxPlayers,
        dockerImage: payload.dockerImage,
        modpackId: payload.modpackId,
        nodeId: payload.nodeId,
        placementStrategy: payload.placementStrategy,
      },
      {
        archiveUploadId: "seeded-session",
        mcVersion: "1.20.6",
        modLoader: 3,
        loaderVersion: "0.16.0",
        memory: 4096,
        maxPlayers: 42,
        dockerImage: "",
        modpackId: "",
        nodeId: "node-1",
        placementStrategy: "",
      },
      "archive create must use the verified node and runtime, keep overrides and drop the former blueprint image",
    );
  }
  assert.deepEqual(
    calls
      .filter((call) => call.method === "previewServerArchive")
      .map((call) => call.payload.nodeId),
    ["node-1", "node-1"],
    "the retry must validate the same upload for the same concrete destination",
  );
  summaries.push({
    scenario:
      "archive preview refusal blocks create, same-node retry succeeds, refused create keeps its submission id",
    calls,
  });

  // A replacement upload blocks submission and only the newest generation may
  // replace the completed archive; a late older completion is ignored.
  await page.reload();
  await waitForFixtureReady(page);
  await page.evaluate(() => {
    globalThis.__archivePreviewMode = "success";
    globalThis.__wizardReset();
  });
  await page
    .getByPlaceholder("survival-smp-01")
    .fill("archive-replacement-fixture");
  await page.getByTestId("create-from-archive-tile").click();
  await uploadServerArchive(page);
  await page.getByTestId("archive-preview").waitFor({ timeout: 15_000 });
  await page.evaluate(() => {
    globalThis.__wizardUploadMode = "pending";
  });
  await uploadServerArchive(page);
  await page.waitForFunction(
    () => globalThis.__wizardPendingUploads.length === 1,
  );
  await page.getByRole("button", { name: "Next Step" }).click();
  await page
    .getByText("Wait for the replacement archive upload to finish.", {
      exact: true,
    })
    .waitFor();
  assert.equal(
    await page.getByTestId("archive-preview").count(),
    1,
    "the former archive stays visible but cannot be submitted while its replacement uploads",
  );
  await uploadServerArchive(page);
  await page.waitForFunction(
    () => globalThis.__wizardPendingUploads.length === 2,
  );
  await page.evaluate(() => {
    globalThis.__wizardPendingUploads[1].release("replacement-newer");
  });
  await page.waitForFunction(
    () =>
      globalThis.__wizardCalls.some(
        (call) =>
          call.method === "previewServerArchive" &&
          call.payload.uploadId === "replacement-newer",
      ),
    null,
    { timeout: 15_000 },
  );
  await page.evaluate(() => {
    globalThis.__wizardPendingUploads[0].release("replacement-older");
  });
  await page.waitForTimeout(250);
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  assert.equal(
    calls.some(
      (call) =>
        call.method === "previewServerArchive" &&
        call.payload.uploadId === "replacement-older",
    ),
    false,
    "a late completion for a superseded upload must not replace the newer archive",
  );
  for (let step = 0; step < 4; step++)
    await page.getByRole("button", { name: "Next Step" }).click();
  await page
    .getByRole("button", { name: "Create Server", exact: true })
    .click();
  await page.getByRole("heading", { name: "Server created" }).waitFor();
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  const replacementCreate = calls.find(
    (call) => call.method === "createServer",
  ).payload;
  assert.equal(replacementCreate.archiveUploadId, "replacement-newer");
  assert.equal(replacementCreate.nodeId, "node-1");
  summaries.push({
    scenario:
      "replacement upload blocks create and a late older completion is fenced",
    calls,
  });

  // A stale replacement upload failure must not surface after a newer upload
  // completes and previews successfully.
  await page.reload();
  await waitForFixtureReady(page);
  await page.evaluate(() => {
    globalThis.__archivePreviewMode = "success";
    globalThis.__archivePreviewFormat = 1;
    globalThis.__wizardUploadMode = "pending";
    globalThis.__wizardReset();
  });
  await page
    .getByPlaceholder("survival-smp-01")
    .fill("archive-stale-upload-fixture");
  await page.getByTestId("create-from-archive-tile").click();
  await uploadServerArchive(page);
  await page.waitForFunction(
    () => globalThis.__wizardPendingUploads.length === 1,
  );
  await uploadServerArchive(page);
  await page.waitForFunction(
    () => globalThis.__wizardPendingUploads.length === 2,
  );
  await page.evaluate(() =>
    globalThis.__wizardPendingUploads[1].release("stale-upload-newest"),
  );
  await page.waitForFunction(
    () =>
      globalThis.__wizardCalls.some(
        (call) =>
          call.method === "previewServerArchive" &&
          call.payload.uploadId === "stale-upload-newest",
      ),
    null,
    { timeout: 15_000 },
  );
  await page.getByTestId("archive-preview").waitFor();
  await page.evaluate(() =>
    globalThis.__wizardPendingUploads[0].fail(
      "stale replacement upload failure",
    ),
  );
  await page.waitForTimeout(250);
  assert.equal(
    await page.getByText("stale replacement upload failure").count(),
    0,
    "a failure from a superseded upload must not reach the operator",
  );
  assert.equal(
    await page.getByTestId("archive-preview-error").count(),
    0,
    "a stale upload failure must not replace the current preview",
  );
  assert.equal(
    await page.getByTestId("archive-preview").count(),
    1,
    "the newest completed upload keeps its preview",
  );
  summaries.push({
    scenario: "a stale replacement upload failure is fenced",
    calls: await page.evaluate(() => globalThis.__wizardCalls),
  });

  // An archive whose verified runtime cannot be provisioned stays blocked.
  await page.reload();
  await waitForFixtureReady(page);
  await page.evaluate(() => {
    globalThis.__archivePreviewMode = "unsupported";
    globalThis.__wizardReset();
  });
  await page
    .getByPlaceholder("survival-smp-01")
    .fill("archive-incompatible-fixture");
  await page.getByTestId("create-from-archive-tile").click();
  await uploadServerArchive(page);
  await page
    .getByText(
      "The archive uses a loader that this controller does not support.",
    )
    .waitFor({ timeout: 15_000 });
  for (let step = 0; step < 4; step++)
    await page.getByRole("button", { name: "Next Step" }).click();
  assert.equal(
    await page
      .getByRole("button", { name: "Create Server", exact: true })
      .isDisabled(),
    true,
    "an archive with blocking findings must leave create disabled",
  );
  await page
    .getByTestId("archive-create-blocked-help")
    .getByText("Resolve the blocking archive findings before creating.", {
      exact: true,
    })
    .waitFor();
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  assert.equal(
    calls.some((call) => call.method === "createServer"),
    false,
    "an archive with an incompatible verified runtime must not be submitted",
  );
  summaries.push({
    scenario: "incompatible verified archive runtime stays blocked",
    calls,
  });

  // A settled preflight with zero enabled destinations must render an
  // actionable empty state instead of waiting forever, then recover when a
  // node is enabled, without a new upload.
  await page.addInitScript(() => {
    globalThis.__wizardDenyListNodes = true;
    globalThis.__wizardArchiveDestinations = [];
    globalThis.__archivePreviewMode = "success";
  });
  await page.goto(base);
  await waitForFixtureReady(page);
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-carbon-theme", "dark");
    document.documentElement.classList.add("dark");
    globalThis.__wizardDenyListNodes = true;
    globalThis.__wizardArchiveDestinations = [];
    globalThis.__archivePreviewMode = "success";
    globalThis.__wizardReset();
  });
  await page
    .getByPlaceholder("survival-smp-01")
    .fill("archive-empty-preflight-fixture");
  await page.getByTestId("create-from-archive-tile").click();
  await uploadServerArchive(page);
  await page.waitForFunction(
    () =>
      globalThis.__wizardCalls.some(
        (call) =>
          call.method === "previewServerArchive" && call.payload.nodeId === "",
      ),
    null,
    { timeout: 15_000 },
  );
  for (let step = 0; step < 3; step++)
    await page.getByRole("button", { name: "Next Step" }).click();
  await page
    .getByText(/found no enabled destination nodes/)
    .waitFor({ timeout: 15_000 });
  await page.getByTestId("archive-destination-empty").waitFor();
  assert.equal(
    await page.getByTestId("archive-destination-preflight").count(),
    0,
    "a settled empty preflight must not keep its waiting state",
  );
  assert.equal(
    await page.getByTestId("archive-destination-option").count(),
    0,
    "a zero-destination preflight must not offer a stale cached node",
  );
  await page.screenshot({
    path: path.join(out, "archive-empty-desktop-dark.png"),
    fullPage: true,
  });
  await page.evaluate(() => {
    globalThis.__wizardArchiveDestinations = [
      { id: "node-1", name: "Fixture Node" },
    ];
    globalThis.__wizardHoldArchivePreflight = true;
  });
  const preflightRetry = page.getByTestId("archive-destination-retry");
  await preflightRetry.click();
  await page.waitForFunction(
    () => typeof globalThis.__wizardReleaseArchivePreflight === "function",
  );
  assert.equal(
    await preflightRetry.isEnabled(),
    false,
    "a pending preflight disables retry",
  );
  await page.evaluate(() => {
    globalThis.__wizardHoldArchivePreflight = false;
    globalThis.__wizardReleaseArchivePreflight();
  });
  await page.waitForFunction(
    () =>
      globalThis.__wizardCalls.some(
        (call) =>
          call.method === "previewServerArchive" &&
          call.payload.nodeId === "node-1",
      ),
    null,
    { timeout: 15_000 },
  );
  await page.getByTestId("archive-destination-option").waitFor();
  assert.equal(
    await page.getByTestId("archive-destination-option").count(),
    1,
    "retrying the preflight must surface a newly enabled destination",
  );
  assert.equal(
    await page.getByTestId("archive-destination-node").textContent(),
    "node-1",
    "the recovered destination must be bound for the capacity preview",
  );
  summaries.push({
    scenario:
      "settled empty archive preflight is actionable and recovers after a node is enabled",
    calls: await page.evaluate(() => globalThis.__wizardCalls),
  });

  // A current successful preflight outranks stale cached node inventory:
  // cached-enabled node-1 is gone, cached-disabled node-2 appears, a still
  // eligible selection survives a refresh and a vanished one is replaced.
  await page.addInitScript(() => {
    globalThis.__wizardNodes = [
      { id: "node-1", name: "Fixture Node", enabled: true, status: 1 },
      { id: "node-2", name: "Fixture Node B", enabled: false, status: 1 },
    ];
    globalThis.__wizardArchiveDestinations = [
      { id: "node-2", name: "Fixture Node B" },
      { id: "node-3", name: "Fixture Node C" },
    ];
    globalThis.__archivePreviewMode = "success";
  });
  await page.goto(base);
  await waitForFixtureReady(page);
  await page.evaluate(() => {
    globalThis.__wizardNodes = [
      { id: "node-1", name: "Fixture Node", enabled: true, status: 1 },
      { id: "node-2", name: "Fixture Node B", enabled: false, status: 1 },
    ];
    globalThis.__wizardArchiveDestinations = [
      { id: "node-2", name: "Fixture Node B" },
      { id: "node-3", name: "Fixture Node C" },
    ];
    globalThis.__archivePreviewMode = "success";
    globalThis.__wizardReset();
  });
  await page
    .getByPlaceholder("survival-smp-01")
    .fill("archive-preflight-precedence-fixture");
  await page.getByTestId("create-from-archive-tile").click();
  await uploadServerArchive(page);
  await page.getByTestId("archive-preview").waitFor({ timeout: 15_000 });
  for (let step = 0; step < 3; step++)
    await page.getByRole("button", { name: "Next Step" }).click();
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[data-testid="archive-destination-option"]')
        .length === 2,
  );
  const destinationNames = () =>
    page
      .getByTestId("archive-destination-option")
      .evaluateAll((options) =>
        options.map((option) =>
          option.querySelector("span")?.textContent.trim(),
        ),
      );
  assert.deepEqual(
    await destinationNames(),
    ["Fixture Node B", "Fixture Node C"],
    "the settled preflight must replace cached inventory in both directions",
  );
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
  assert.equal(
    await page.getByTestId("archive-destination-node").textContent(),
    "node-2",
    "the first preflight destination must replace the stale cached selection",
  );
  await page.screenshot({
    path: path.join(out, "archive-precedence-desktop-dark.png"),
    fullPage: true,
  });
  await page.getByTestId("archive-destination-option").nth(1).click();
  await page.waitForFunction(
    () =>
      globalThis.__wizardCalls.some(
        (call) =>
          call.method === "previewServerArchive" &&
          call.payload.nodeId === "node-3",
      ),
    null,
    { timeout: 15_000 },
  );
  await page.waitForFunction(
    () =>
      document.querySelector('[data-testid="archive-destination-node"]')
        ?.textContent === "node-3",
  );
  assert.deepEqual(
    await destinationNames(),
    ["Fixture Node B", "Fixture Node C"],
    "a still-eligible selection must survive the refreshed preflight",
  );

  await page.evaluate(() => {
    globalThis.__wizardArchiveDestinations = [
      { id: "node-2", name: "Fixture Node B" },
    ];
  });
  await showWizardStep(page, "Basic Information");
  await uploadServerArchive(page);
  await page.waitForFunction(
    () =>
      globalThis.__wizardCalls.some(
        (call) =>
          call.method === "previewServerArchive" &&
          call.payload.uploadId === "seeded-session-2" &&
          call.payload.nodeId === "node-2",
      ),
    null,
    { timeout: 15_000 },
  );
  for (let step = 0; step < 3; step++)
    await page.getByRole("button", { name: "Next Step" }).click();
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[data-testid="archive-destination-option"]')
        .length === 1,
  );
  assert.deepEqual(
    await destinationNames(),
    ["Fixture Node B"],
    "a destination dropped by the new preflight must vanish",
  );
  assert.deepEqual(
    await page.evaluate(() =>
      globalThis.__wizardCalls
        .filter(
          (call) =>
            call.method === "previewServerArchive" &&
            call.payload.uploadId === "seeded-session-2",
        )
        .map((call) => call.payload.nodeId),
    ),
    ["node-3", "node-2"],
    "the replacement preflight must drop the vanished selection and verify the new one",
  );
  assert.equal(
    await page.getByTestId("archive-destination-node").textContent(),
    "node-2",
    "the preflight replacement must be the submitted destination",
  );
  await showWizardStep(page, "Add-ons");
  await page
    .getByRole("button", { name: "Create Server", exact: true })
    .click();
  await page.getByRole("heading", { name: "Server created" }).waitFor();
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  const precedenceCreate = calls.find(
    (call) => call.method === "createServer",
  ).payload;
  assert.equal(precedenceCreate.nodeId, "node-2");
  assert.equal(precedenceCreate.placementStrategy, "");
  summaries.push({
    scenario:
      "current archive preflight outranks cached node inventory and keeps an eligible selection",
    calls,
  });

  // Representative desktop/narrow and light/dark captures of the archive
  // destination preflight, blocked/refusal/retry states and keyboard selection.
  await page.addInitScript(() => {
    // A create-only principal: ListNodes stays denied and placement comes from
    // the archive preview's enabled-destination list.
    globalThis.__wizardDenyListNodes = true;
    globalThis.__wizardArchiveDestinations = [
      { id: "node-1", name: "Fixture Node" },
      { id: "node-2", name: "Fixture Node B" },
    ];
    globalThis.__archivePreviewMode = "success";
  });
  const archiveViews = [
    {
      name: "desktop-dark",
      viewport: { width: 1440, height: 1000 },
      theme: "dark",
      format: "zip",
    },
    {
      name: "desktop-light",
      viewport: { width: 1440, height: 1000 },
      theme: "light",
      format: "tar.gz",
    },
    {
      name: "narrow-dark",
      viewport: { width: 390, height: 900 },
      theme: "dark",
      format: "tar.gz",
    },
    {
      name: "narrow-light",
      viewport: { width: 390, height: 900 },
      theme: "light",
      format: "zip",
    },
  ];
  for (const view of archiveViews) {
    await page.setViewportSize(view.viewport);
    await page.goto(base);
    await waitForFixtureReady(page);
    await page.evaluate(({ theme, format }) => {
      document.documentElement.setAttribute("data-carbon-theme", theme);
      document.documentElement.classList.toggle("dark", theme === "dark");
      globalThis.__wizardDenyListNodes = true;
      globalThis.__wizardArchiveDestinations = [
        { id: "node-1", name: "Fixture Node" },
        { id: "node-2", name: "Fixture Node B" },
      ];
      globalThis.__archivePreviewMode = "success";
      globalThis.__archivePreviewFormat = format === "tar.gz" ? 2 : 1;
      globalThis.__wizardReset();
    }, view);
    await page
      .getByPlaceholder("survival-smp-01")
      .fill(`archive-preview-${view.name}`);
    await page.waitForTimeout(250);
    const archiveTile = page.getByTestId("create-from-archive-tile");
    await archiveTile.focus();
    await page.keyboard.press("Space");
    keyboard.push(`${view.name}: Space selected the archive tile`);
    assert.equal(
      await archiveTile.getAttribute("aria-checked"),
      "true",
      "the archive tile must be selectable from the keyboard",
    );
    await page.waitForTimeout(200);
    const archiveDescription = archiveTile.locator("p");
    const descriptionContrast = async () =>
      renderedContrast(archiveDescription);
    const restContrast = await descriptionContrast();
    assert.ok(
      restContrast >= 4.5,
      `selected archive description contrast ${restContrast.toFixed(2)}:1`,
    );
    await archiveTile.hover();
    await page.waitForTimeout(200);
    const hoverContrast = await descriptionContrast();
    assert.ok(
      hoverContrast >= 4.5,
      `hovered archive description contrast ${hoverContrast.toFixed(2)}:1`,
    );
    const archiveBox = await archiveTile.boundingBox();
    assert.ok(archiveBox);
    await page.mouse.move(
      archiveBox.x + archiveBox.width / 2,
      archiveBox.y + archiveBox.height / 2,
    );
    await page.mouse.down();
    await page.waitForTimeout(200);
    const pressedContrast = await descriptionContrast();
    await page.mouse.up();
    await page.mouse.move(0, 0);
    assert.ok(
      pressedContrast >= 4.5,
      `pressed archive description contrast ${pressedContrast.toFixed(2)}:1`,
    );
    await uploadServerArchive(page, view.format);
    await page.getByTestId("archive-preview").waitFor({ timeout: 15_000 });
    await page
      .getByTestId("archive-preview")
      .getByText(view.format === "tar.gz" ? "TAR.GZ (.tar.gz)" : "ZIP (.zip)")
      .waitFor();
    assert.deepEqual(
      await page.getByTestId("archive-destination-option").count(),
      0,
      "destination choices appear on the cluster step, not the upload step",
    );
    await page.screenshot({
      path: path.join(out, `archive-preview-${view.name}.png`),
      fullPage: true,
    });
    for (let step = 0; step < 3; step++)
      await page.getByRole("button", { name: "Next Step" }).click();
    const autoPlacement = page.getByRole("radio", { name: /Auto-Placement/ });
    await autoPlacement.waitFor();
    assert.equal(
      await autoPlacement.getAttribute("aria-disabled"),
      "true",
      "archive mode must not use generic automatic placement",
    );
    assert.equal(
      await autoPlacement.getAttribute("disabled"),
      null,
      "the same-node explanation must stay focusable",
    );
    await autoPlacement.focus();
    assert.match(
      await keyboardFocusLabel(page),
      /Auto-Placement/,
      "the disabled Auto-Placement explanation remains keyboard reachable",
    );
    keyboard.push(
      `${view.name}: focus reached the aria-disabled Auto-Placement card`,
    );
    const autoContrast = await renderedContrast(autoPlacement.locator("p"));
    assert.ok(
      autoContrast >= 4.5,
      `same-node explanation contrast ${autoContrast.toFixed(2)}:1`,
    );
    const options = page.getByTestId("archive-destination-option");
    assert.equal(
      await options.count(),
      2,
      "ListNodes denial must fall back to the preview destination list",
    );
    await options.nth(1).focus();
    await page.screenshot({
      path: path.join(out, `archive-focus-${view.name}.png`),
      fullPage: true,
    });
    await page.keyboard.press("Space");
    keyboard.push(`${view.name}: Space selected the second destination node`);
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
    await page.getByTestId("archive-destination-confirmation").waitFor();
    assert.equal(
      await page.getByTestId("archive-destination-node").textContent(),
      "node-2",
      "keyboard selection must bind the second concrete destination",
    );
    await page.screenshot({
      path: path.join(out, `archive-selected-${view.name}.png`),
      fullPage: true,
    });

    await page.evaluate(() => {
      globalThis.__archivePreviewMode = "unsupported";
    });
    await options.nth(0).click();
    await page
      .getByText("This archive has blocking findings and cannot be created.", {
        exact: true,
      })
      .waitFor({ timeout: 15_000 });
    await page.screenshot({
      path: path.join(out, `archive-blocked-${view.name}.png`),
      fullPage: true,
    });

    await page.evaluate(() => {
      globalThis.__archivePreviewMode = "fail-once";
    });
    await options.nth(1).click();
    await showWizardStep(page, "Basic Information");
    await page
      .getByText(/fixture archive preview refusal/)
      .waitFor({ timeout: 15_000 });
    await page.screenshot({
      path: path.join(out, `archive-refusal-${view.name}.png`),
      fullPage: true,
    });
    await page
      .getByRole("button", { name: "Retry preview", exact: true })
      .click();
    await page.getByTestId("archive-preview").waitFor({ timeout: 15_000 });
    await page.screenshot({
      path: path.join(out, `archive-retry-${view.name}.png`),
      fullPage: true,
    });
    summaries.push({
      scenario: `archive destinations ${view.name}`,
      calls: await page.evaluate(() => globalThis.__wizardCalls),
    });

    if (view.name !== "desktop-dark") continue;
    // Held archive create: back navigation is cancelled and replacement
    // controls are disabled while the captured submission is in flight.
    await showWizardStep(page, "Add-ons");
    await page.evaluate(() => {
      globalThis.__wizardHoldCreate = true;
    });
    await page
      .getByRole("button", { name: "Create Server", exact: true })
      .click();
    await page.locator("form[inert]").waitFor();
    assert.equal(
      await page
        .getByRole("link", { name: "Cancel" })
        .getAttribute("aria-disabled"),
      "true",
      "cancel navigation must be disabled during the archive create",
    );
    assert.equal(
      await page.getByRole("button", { name: "Previous" }).isDisabled(),
      true,
      "step navigation must be disabled during the archive create",
    );
    assert.equal(
      await page.locator('[data-testid="archive-file-input"]').count(),
      0,
      "the upload step is not reachable while the archive create is in flight",
    );
    const cancelled = await page.evaluate(() => {
      let cancelled = false;
      globalThis.__wizardBeforeNavigate?.({
        cancel() {
          cancelled = true;
        },
      });
      return cancelled;
    });
    assert.equal(
      cancelled,
      true,
      "a browser back during the archive create must be cancelled",
    );
    await page.evaluate(() => globalThis.__wizardReleaseCreate());
    await page.getByRole("heading", { name: "Server created" }).waitFor();
    calls = await page.evaluate(() => globalThis.__wizardCalls);
    const heldArchive = calls.find(
      (call) => call.method === "createServer",
    ).payload;
    assert.deepEqual(
      {
        archiveUploadId: heldArchive.archiveUploadId,
        nodeId: heldArchive.nodeId,
        detached: heldArchive.detached,
        dockerImage: heldArchive.dockerImage,
        dockerOverrides: heldArchive.dockerOverrides,
        modpackId: heldArchive.modpackId,
      },
      {
        archiveUploadId: "seeded-session",
        nodeId: "node-2",
        detached: false,
        dockerImage: "",
        dockerOverrides: undefined,
        modpackId: "",
      },
      "the held create must submit the captured clean archive request",
    );
    assert.equal(
      calls.filter((call) => call.method === "createServer").length,
      1,
      "the held archive create must be submitted exactly once",
    );
    summaries.push({
      scenario:
        "held archive create cancels back navigation and disables replacement controls",
    });
  }
  // A tar.gz archive is a first-class create input, not only an export format.
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(base);
  await waitForFixtureReady(page);
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-carbon-theme", "light");
    document.documentElement.classList.remove("dark");
    globalThis.__wizardDenyListNodes = true;
    globalThis.__wizardArchiveDestinations = [
      { id: "node-1", name: "Fixture Node" },
    ];
    globalThis.__archivePreviewMode = "success";
    globalThis.__archivePreviewFormat = 2;
    globalThis.__wizardReset();
  });
  await page
    .getByPlaceholder("survival-smp-01")
    .fill("archive-tar-create-fixture");
  await page.getByTestId("create-from-archive-tile").click();
  await uploadServerArchive(page, "tar.gz");
  await page.getByTestId("archive-preview").waitFor({ timeout: 15_000 });
  await page
    .getByTestId("archive-preview")
    .getByText("TAR.GZ (.tar.gz)")
    .waitFor();
  for (let step = 0; step < 4; step++)
    await page.getByRole("button", { name: "Next Step" }).click();
  await page
    .getByRole("button", { name: "Create Server", exact: true })
    .click();
  await page.getByRole("heading", { name: "Server created" }).waitFor();
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  const tarCreate = calls.find(
    (call) => call.method === "createServer",
  ).payload;
  assert.equal(tarCreate.archiveUploadId, "seeded-session");
  assert.equal(tarCreate.nodeId, "node-1");
  assert.equal(tarCreate.placementStrategy, "");
  summaries.push({
    scenario: "tar.gz archive previews and creates with its verified runtime",
    calls,
  });

  // Upload status/progress and stale destination fencing. A delayed failing
  // preview for an abandoned destination must not overwrite the current one.
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(base);
  await waitForFixtureReady(page);
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-carbon-theme", "light");
    document.documentElement.classList.remove("dark");
    globalThis.__wizardDenyListNodes = true;
    globalThis.__wizardArchiveDestinations = [
      { id: "node-1", name: "Fixture Node" },
      { id: "node-2", name: "Fixture Node B" },
    ];
    globalThis.__archivePreviewMode = "success";
    globalThis.__archivePreviewFormat = 2;
    globalThis.__wizardUploadMode = "pending";
    globalThis.__wizardReset();
  });
  await page
    .getByPlaceholder("survival-smp-01")
    .fill("archive-progress-fixture");
  await page.getByTestId("create-from-archive-tile").click();
  await uploadServerArchive(page, "tar.gz");
  await page.waitForFunction(
    () => globalThis.__wizardPendingUploads.length === 1,
  );
  await page.evaluate(() => globalThis.__wizardPendingUploads[0].progress(42));
  await page.getByText("Uploading… 42%").waitFor({ timeout: 15_000 });
  await page.screenshot({
    path: path.join(out, "archive-uploading-desktop-light.png"),
    fullPage: true,
  });
  await page.evaluate(() =>
    globalThis.__wizardPendingUploads[0].release("seeded-session"),
  );
  await page.getByTestId("archive-preview").waitFor({ timeout: 15_000 });
  await page
    .getByTestId("archive-preview")
    .getByText("TAR.GZ (.tar.gz)")
    .waitFor();
  await page.evaluate(() => {
    globalThis.__wizardUploadMode = "success";
  });
  for (let step = 0; step < 3; step++)
    await page.getByRole("button", { name: "Next Step" }).click();
  const staleOptions = page.getByTestId("archive-destination-option");
  await page.getByTestId("archive-destination-confirmation").waitFor();
  await page.evaluate(() => {
    globalThis.__wizardDelayedArchiveNode = "node-2";
    globalThis.__wizardDelayedArchiveNodeMode = "fail";
    globalThis.__wizardHoldArchiveNodePreflight = true;
    globalThis.__wizardReleaseArchiveNodePreflight = undefined;
  });
  await staleOptions.nth(1).click();
  await page.waitForFunction(
    () => typeof globalThis.__wizardReleaseArchiveNodePreflight === "function",
  );
  await staleOptions.nth(0).click();
  await page.waitForFunction(
    () =>
      globalThis.__wizardCalls.some(
        (call) =>
          call.method === "previewServerArchive" &&
          call.payload.nodeId === "node-1",
      ),
    null,
    { timeout: 15_000 },
  );
  await page.waitForFunction(
    () =>
      document.querySelector('[data-testid="archive-destination-node"]')
        ?.textContent === "node-1",
  );
  await page.evaluate(() => globalThis.__wizardReleaseArchiveNodePreflight());
  await page.waitForTimeout(250);
  assert.equal(
    await page.getByTestId("archive-preview-error").count(),
    0,
    "a delayed failure for an abandoned destination must not surface",
  );
  assert.equal(
    await page.getByTestId("archive-destination-node").textContent(),
    "node-1",
    "the newest destination preflight must remain authoritative",
  );
  await page.screenshot({
    path: path.join(out, "archive-stale-destination-desktop-light.png"),
    fullPage: true,
  });
  summaries.push({
    scenario:
      "archive upload progress renders and a delayed stale destination failure is fenced",
    calls: await page.evaluate(() => globalThis.__wizardCalls),
  });

  // Export dialog at both formats and representative viewports/themes. The
  // preparing state is held so the status is inspectable, then released.
  const exportBase = base.replace(/wizard\.html$/, "export.html");
  const exportViews = [
    {
      name: "desktop-dark",
      viewport: { width: 1440, height: 1000 },
      theme: "dark",
      formats: ["zip", "tar.gz"],
    },
    {
      name: "desktop-light",
      viewport: { width: 1440, height: 1000 },
      theme: "light",
      formats: ["tar.gz"],
    },
    {
      name: "narrow-dark",
      viewport: { width: 390, height: 900 },
      theme: "dark",
      formats: ["zip"],
    },
    {
      name: "narrow-light",
      viewport: { width: 390, height: 900 },
      theme: "light",
      formats: ["tar.gz"],
    },
  ];
  for (const view of exportViews) {
    await page.setViewportSize(view.viewport);
    await page.goto(exportBase);
    await waitForExportReady(page);
    await page.evaluate((theme) => {
      document.documentElement.setAttribute("data-carbon-theme", theme);
      document.documentElement.classList.toggle("dark", theme === "dark");
      globalThis.__exportMode = "success";
      globalThis.__wizardReset();
    }, view.theme);
    for (const format of view.formats) {
      const label = format === "tar.gz" ? "TAR.GZ (.tar.gz)" : "ZIP (.zip)";
      await page.evaluate(() => {
        globalThis.__wizardReset();
      });
      const exportTrigger = page.getByTestId("server-export-archive");
      await exportTrigger.focus();
      await page.keyboard.press("Enter");
      keyboard.push(`${view.name}: Enter opened the export modal`);
      await page.getByTestId("archive-export-panel").waitFor();
      await page.waitForFunction(
        () => document.activeElement?.getAttribute("aria-label") === "Close modal",
      );
      await page.keyboard.press("Tab");
      assert.equal(
        await keyboardFocusLabel(page),
        "ZIP (.zip)",
        "Tab moves from Close into the first format radio",
      );
      if (format === "tar.gz") {
        await page.keyboard.press("Tab");
        assert.equal(
          await keyboardFocusLabel(page),
          "TAR.GZ (.tar.gz)",
          "Tab reaches the tar.gz format radio",
        );
      }
      await page.keyboard.press("Space");
      assert.equal(
        await page.getByRole("radio", { name: label }).getAttribute("aria-checked"),
        "true",
        "Space selects the focused archive format",
      );
      keyboard.push(`${view.name}: Space selected ${label}`);
      await page.evaluate(() => {
        globalThis.__exportMode = "hold";
      });
      await page.getByTestId("archive-prepare").click();
      await page.getByText("Preparing archive…").waitFor({ timeout: 15_000 });
      await page.screenshot({
        path: path.join(
          out,
          `archive-export-preparing-${view.name}-${format}.png`,
        ),
        fullPage: true,
      });
      await page.evaluate(() => globalThis.__wizardReleaseExport());
      await page.getByTestId("archive-export-ready").waitFor({
        timeout: 15_000,
      });
      const expectedName =
        format === "tar.gz" ? "fixture-server.tar.gz" : "fixture-server.zip";
      await page.getByText(expectedName, { exact: true }).waitFor();
      await page
        .getByText(
          "Third-party plugin and mod files are included and can contain credentials. Review the archive before sharing it.",
          { exact: true },
        )
        .waitFor();
      assert.equal(
        await page.getByTestId("archive-download").isDisabled(),
        true,
        "download must stay disabled until the disclosure is acknowledged",
      );
      calls = await page.evaluate(() => globalThis.__wizardCalls);
      const exportCall = calls.find(
        (call) => call.method === "exportServerArchive",
      );
      assert.deepEqual(
        {
          serverId: exportCall.payload.serverId,
          format: exportCall.payload.format,
          consistency: exportCall.payload.consistency,
        },
        {
          serverId: "fixture-server",
          format: format === "tar.gz" ? 2 : 1,
          consistency: 1,
        },
        "export must request the selected container with stopped consistency",
      );
      await page.screenshot({
        path: path.join(out, `archive-export-ready-${view.name}-${format}.png`),
        fullPage: true,
      });
      const acknowledgement = page.getByLabel(
        "I have reviewed this notice before downloading.",
      );
      await acknowledgement.focus();
      await page.keyboard.press("Space");
      keyboard.push(`${view.name}: Space acknowledged the disclosure`);
      assert.equal(
        await acknowledgement.isChecked(),
        true,
        "the disclosure can be acknowledged from the keyboard",
      );
      assert.equal(
        await page.getByTestId("archive-download").isEnabled(),
        true,
        "download becomes available after acknowledgement",
      );

      await page.keyboard.press("Escape");
      await page
        .getByTestId("archive-export-panel")
        .waitFor({ state: "hidden" });
      await page.waitForFunction(
        () =>
          document.activeElement?.getAttribute("data-testid") ===
          "server-export-archive",
      );
      assert.equal(
        await keyboardFocusLabel(page),
        "Export server archive",
        "Escape returns focus to the export trigger",
      );
      keyboard.push(`${view.name}: Escape closed and returned focus`);
      summaries.push({
        scenario: `archive export ${view.name} ${format}`,
        calls: await page.evaluate(() => globalThis.__wizardCalls),
      });
    }
  }

  // Delayed ZIP then tar.gz with close/reopen. Only the current attempt may
  // settle, and the authorized download must use the current session.
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(exportBase);
  await waitForExportReady(page);
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-carbon-theme", "dark");
    document.documentElement.classList.add("dark");
    globalThis.__exportMode = "deferred";
    globalThis.__exportPending = [];
    globalThis.__wizardReset();
  });
  await page.getByTestId("server-export-archive").click();
  await page.getByTestId("archive-export-panel").waitFor();
  await page.getByTestId("archive-prepare").click();
  await page.waitForFunction(() => globalThis.__exportPending?.length === 1);
  assert.equal(
    await page.evaluate(() => globalThis.__exportPending[0].format),
    1,
    "the first delayed preparation is the ZIP request",
  );
  await page.keyboard.press("Escape");
  await page.getByTestId("archive-export-panel").waitFor({ state: "hidden" });
  await page.getByTestId("server-export-archive").click();
  await page.getByTestId("archive-export-panel").waitFor();
  await page.getByRole("radio", { name: "TAR.GZ (.tar.gz)" }).click();
  await page.getByTestId("archive-prepare").click();
  await page.waitForFunction(() => globalThis.__exportPending?.length === 2);
  assert.equal(
    await page.evaluate(() => globalThis.__exportPending[1].format),
    2,
    "the reopened dialog started a tar.gz request",
  );

  await page.evaluate(() => globalThis.__exportPending[0].resolve());
  await page.waitForTimeout(250);
  assert.equal(
    await page.getByTestId("archive-export-ready").count(),
    0,
    "a stale ZIP success must not make the reopened tar.gz attempt ready",
  );
  assert.equal(
    await page.getByText("fixture-server.zip", { exact: true }).count(),
    0,
    "a stale ZIP result must not replace the active filename",
  );
  assert.equal(
    await page.getByText("Preparing archive…").count(),
    1,
    "the current tar.gz attempt still owns the preparing state",
  );

  await page.evaluate(() => globalThis.__exportPending[1].resolve());
  await page.getByTestId("archive-export-ready").waitFor({ timeout: 15_000 });
  await page.getByText("fixture-server.tar.gz", { exact: true }).waitFor();

  assert.equal(
    await page.getByTestId("archive-download").isDisabled(),
    true,
    "the download stays disabled while the notice is unacknowledged",
  );
  assert.equal(
    downloadRequests.length,
    0,
    "no download request exists before acknowledgement",
  );
  await page
    .getByLabel("I have reviewed this notice before downloading.")
    .check();
  const downloadPromise = page.waitForEvent("download", { timeout: 15_000 });
  await page.getByTestId("archive-download").click();
  const download = await downloadPromise;
  assert.equal(
    download.suggestedFilename(),
    "fixture-server.tar.gz",
    "the download filename comes from the current tar.gz session",
  );
  assert.match(
    download.url(),
    /\/api\/v1\/download\/export-session-tar$/,
    "the download must use only the current session",
  );
  assert.equal(downloadRequests.length, 1);
  assert.match(
    downloadRequests[0].url,
    /\/api\/v1\/download\/export-session-tar$/,
    "the browser request must use only the current session",
  );
  assert.equal(downloadRequests[0].status, 200);
  const stream = await download.createReadStream();
  const chunks = [];
  for await (const chunk of stream) chunks.push(chunk);
  assert.equal(
    Buffer.concat(chunks).toString(),
    "current-tar-bytes",
    "the authorized transfer returns the current session bytes",
  );
  await page.getByTestId("archive-export-panel").waitFor({ state: "hidden" });
  summaries.push({
    scenario:
      "delayed ZIP then tar.gz close/reopen fences stale success and downloads only the current session",
    download: {
      url: download.url(),
      status: downloadRequests[0].status,
      filename: download.suggestedFilename(),
      bytes: Buffer.byteLength("current-tar-bytes"),
    },
    calls: await page.evaluate(() => globalThis.__wizardCalls),
  });

  // A stale rejection from the closed ZIP attempt must not surface while the
  // reopened tar.gz attempt can still fail on its own.
  await page.goto(exportBase);
  await waitForExportReady(page);
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-carbon-theme", "dark");
    document.documentElement.classList.add("dark");
    globalThis.__exportMode = "deferred";
    globalThis.__exportPending = [];
    globalThis.__wizardReset();
  });
  await page.getByTestId("server-export-archive").click();
  await page.getByTestId("archive-export-panel").waitFor();
  await page.getByTestId("archive-prepare").click();
  await page.waitForFunction(() => globalThis.__exportPending?.length === 1);
  await page.keyboard.press("Escape");
  await page.getByTestId("archive-export-panel").waitFor({ state: "hidden" });
  await page.getByTestId("server-export-archive").click();
  await page.getByTestId("archive-export-panel").waitFor();
  await page.getByRole("radio", { name: "TAR.GZ (.tar.gz)" }).click();
  await page.getByTestId("archive-prepare").click();
  await page.waitForFunction(() => globalThis.__exportPending?.length === 2);
  await page.evaluate(() =>
    globalThis.__exportPending[0].reject("stale ZIP refusal"),
  );
  await page.waitForTimeout(250);
  assert.equal(
    await page.getByText("stale ZIP refusal", { exact: true }).count(),
    0,
    "a closed attempt must not surface its rejection",
  );
  assert.equal(
    await page.getByText("Preparing archive…").count(),
    1,
    "the reopened attempt stays authoritative after a stale rejection",
  );
  await page.evaluate(() => globalThis.__exportPending[1].resolve());
  await page.getByTestId("archive-export-ready").waitFor({ timeout: 15_000 });
  await page.getByText("fixture-server.tar.gz", { exact: true }).waitFor();
  await page.keyboard.press("Escape");
  await page.getByTestId("archive-export-panel").waitFor({ state: "hidden" });
  summaries.push({
    scenario: "delayed stale ZIP rejection is fenced after close/reopen",
    calls: await page.evaluate(() => globalThis.__wizardCalls),
  });

  // Polling a different server identity releases the attempt; its late result
  // cannot make the new server ready.
  await page.goto(exportBase);
  await waitForExportReady(page);
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-carbon-theme", "dark");
    document.documentElement.classList.add("dark");
    globalThis.__exportMode = "deferred";
    globalThis.__exportPending = [];
    globalThis.__wizardReset();
  });
  await page.getByTestId("server-export-archive").click();
  await page.getByTestId("archive-export-panel").waitFor();
  await page.getByTestId("archive-prepare").click();
  await page.waitForFunction(() => globalThis.__exportPending?.length === 1);
  await page.evaluate(() => globalThis.__exportSwitchServer("fixture-server-b"));
  await page.getByTestId("archive-prepare").waitFor({ timeout: 15_000 });
  await page.getByTestId("archive-prepare").click();
  await page.waitForFunction(() => globalThis.__exportPending?.length === 2);
  await page.evaluate(() => globalThis.__exportPending[0].resolve());
  await page.waitForTimeout(250);
  assert.equal(
    await page.getByTestId("archive-export-ready").count(),
    0,
    "the former server result must not make the new server ready",
  );
  await page.evaluate(() => globalThis.__exportPending[1].resolve());
  await page.getByTestId("archive-export-ready").waitFor({ timeout: 15_000 });
  summaries.push({
    scenario: "server change fences the former in-flight export",
    calls: await page.evaluate(() => globalThis.__wizardCalls),
  });
  await page.keyboard.press("Escape");
  await page.getByTestId("archive-export-panel").waitFor({ state: "hidden" });

  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(exportBase);
  await waitForExportReady(page);
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-carbon-theme", "dark");
    document.documentElement.classList.add("dark");
    globalThis.__exportMode = "fail";
    globalThis.__wizardReset();
  });
  await page.getByTestId("server-export-archive").click();
  await page.getByTestId("archive-export-panel").waitFor();
  await page.getByTestId("archive-prepare").click();
  await page
    .getByTestId("archive-export-panel")
    .getByText("fixture export preparation refusal")
    .waitFor({ timeout: 15_000 });
  await page.screenshot({
    path: path.join(out, "archive-export-error-desktop-dark.png"),
    fullPage: true,
  });
  await page.evaluate(() => {
    globalThis.__exportMode = "success";
  });
  await page.getByRole("button", { name: "Try again", exact: true }).click();
  await page.getByTestId("archive-export-ready").waitFor({
    timeout: 15_000,
  });
  await page.keyboard.press("Escape");
  await page.getByTestId("archive-export-panel").waitFor({ state: "hidden" });
  summaries.push({
    scenario: "archive export refusal retries in place",
    calls: await page.evaluate(() => globalThis.__wizardCalls),
  });

  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(`${exportBase}?running=1`);
  await waitForExportReady(page);
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-carbon-theme", "dark");
    document.documentElement.classList.add("dark");
    globalThis.__wizardReset();
  });
  await page.getByTestId("server-export-archive").click();
  await page.getByTestId("archive-export-panel").waitFor();
  await page.getByTestId("archive-export-running").waitFor();
  await page.waitForTimeout(400);
  assert.equal(
    await page.getByTestId("archive-prepare").isDisabled(),
    true,
    "a live server must disable export preparation",
  );
  assert.equal(
    await page.getByRole("radio", { name: "ZIP (.zip)" }).isDisabled(),
    true,
    "a live server must disable format selection",
  );
  await page.screenshot({
    path: path.join(out, "archive-export-running-desktop-dark.png"),
    fullPage: true,
  });
  await page.keyboard.press("Escape");
  summaries.push({ scenario: "archive export refuses a live server" });

  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-carbon-theme", "dark");
    document.documentElement.classList.add("dark");
  });

  assert.deepEqual(errors, [], "the fixture page must not throw");
  await page.screenshot({ path: path.join(out, "result.png") });
  await fs.writeFile(
    path.join(out, "results.json"),
    JSON.stringify(
      {
        head: {
          sha: verifiedHead,
          source: "git rev-parse HEAD",
          worktreeDirty: Boolean(worktreeStatus),
          worktreeStatus: worktreeStatus ? worktreeStatus.split("\n") : [],
        },
        scope:
          "Deterministic route/component UI fixture. Not browser evidence for hosted live-engine round trips.",
        scenarios: summaries.map(({ scenario }) => scenario),
        keyboard,
        summaries,
        errors,
        browser: launched.metadata,
      },
      null,
      2,
    ),
  );
  console.log(
    "PASS: Create Server library preset, datapack upload, compatible mods and module selection, partial-install retry, archive ZIP/tar.gz preview and create retry, upload progress, stale upload/destination fencing, empty preflight recovery, destination-preflight precedence, archive export attempt fencing, keyboard focus return and ZIP/tar.gz export at desktop/narrow light/dark",
  );
} catch (error) {
  runError = error;
} finally {
  // Every teardown is attempted even when an earlier one fails. The owned Vite
  // cache, loopback server and locked browser are the only resources touched.
  const cleanupErrors = [];
  for (const [name, action] of [
    ["browser", () => browser?.close()],
    ["vite server", () => server?.close()],
    ["owned vite cache", () => fs.rm(cache, { recursive: true, force: true })],
  ]) {
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
if (runError) throw runError;
