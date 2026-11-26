// SPDX-License-Identifier: MPL-2.0
// Seeded Chromium fixture for the create-server wizard datapack upload slice.
// Drives the real /servers/new page through the normal UI seam with only the
// transport and chunked-upload modules replaced by deterministic fixtures.
// No live panel, credentials, worlds, daemon or external infrastructure.
//
//   REVIEW_CHROME=/path/to/chrome.exe \
//     node scripts/review/wizard-datapack-upload-check.mjs

import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";
import { launchReviewBrowser } from "./browser.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
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

const summaries = [];

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
          replacement: path.join(
            project,
            "e2e/oss-245-diagnostics-reconnect/navigation.js",
          ),
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
            if (req.url !== "/wizard.html") return next();
            try {
              const entry =
                "/@fs/" + path.join(fixture, "main.js").replaceAll("\\", "/");
              res.setHeader("Content-Type", "text/html");
              res.end(
                await vite.transformIndexHtml(
                  "/wizard.html",
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

  const out = path.join(here, "../../tmp/review-output/wizard-datapack-upload");
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
  await page.getByText("Fixture previous start still holds its lock.").waitFor();
  assert.equal(
    await page.evaluate(() => globalThis.__wizardCalls.filter((call) => call.method === "startServer").length),
    1,
    "an errored server with an in-flight journal receipt must not start again",
  );
  await page.evaluate(() => { globalThis.__wizardJournalLocked = false; });
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
    calls.filter((call) => call.method === "getModule").map((call) => call.payload.status),
    [6, 5, 5, 1],
    "the module must be observed preparing, errored, and finally stopped",
  );
  assert.deepEqual(
    calls.filter((call) => call.method === "recreateModule").map((call) => call.payload.id),
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
  await page.getByRole("button", { name: "Add Resource Pack Host", exact: true }).click();
  await page.evaluate(() => { globalThis.__wizardModuleMode = "lost"; });
  await page.getByRole("button", { name: "Create Server", exact: true }).click();
  await page.getByText("Fixture module response lost.", { exact: false }).waitFor();
  await page.getByRole("button", { name: "Retry failed add-ons", exact: true }).click();
  await page.getByRole("link", { name: "Open server", exact: true }).waitFor();
  await page.getByText("Added", { exact: true }).waitFor();
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  assert.deepEqual(
    calls.filter((call) => ["createModule", "listModules", "getModule", "recreateModule"].includes(call.method))
      .map((call) => call.method),
    ["createModule", "listModules", "getModule", "recreateModule", "getModule"],
    "retry must inspect and repair the existing error row after a lost create response",
  );
  summaries.push({ scenario: "lost module response recovers its existing errored record", calls });

  assert.deepEqual(errors, [], "the fixture page must not throw");
  await page.screenshot({ path: path.join(out, "result.png") });
  await fs.writeFile(
    path.join(out, "results.json"),
    JSON.stringify(
      {
        scenarios: summaries.map(({ scenario }) => scenario),
        summaries,
        errors,
        browser: launched.metadata,
      },
      null,
      2,
    ),
  );
  console.log(
    "PASS: Create Server library preset, datapack upload, compatible mods and module selection, and partial-install retry",
  );
} finally {
  await browser?.close();
  await server?.close();
  await fs.rm(cache, { recursive: true, force: true });
}
