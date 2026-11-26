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

async function uploadServerArchive(page) {
  await page.locator('[data-testid="archive-file-input"]').setInputFiles({
    name: "fixture-server.zip",
    mimeType: "application/zip",
    buffer: Buffer.from("PK\x03\x04fixture-server"),
  });
}

async function showWizardStep(page, title) {
  await page.getByRole("button", { name: new RegExp(title) }).click();
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
  await page.getByRole("button", { name: "Browse mods", exact: true }).click();
  await page.getByText("Fixture Optimization", { exact: true }).waitFor();
  assert.equal(await page.getByText("Client-only Project").count(), 0);
  await page.getByRole("button", { name: "Select Version" }).click();
  await page.getByRole("button", { name: "Add to server setup" }).click();
  await page.getByText("Selected", { exact: true }).waitFor();
  await page.keyboard.press("Escape");
  await page.getByText(/Fixture Optimization 1\.0/).waitFor();
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
  await page
    .getByRole("button", { name: "Create Server", exact: true })
    .click();
  await page
    .getByText(
      "Wait for a successful archive preview of this upload and destination before creating.",
      { exact: true },
    )
    .waitFor();
  calls = await page.evaluate(() => globalThis.__wizardCalls);
  assert.equal(
    calls.some((call) => call.method === "createServer"),
    false,
    "a failed preview must block create",
  );
  await page
    .getByRole("button", { name: "Retry preview", exact: true })
    .click();
  await page.getByTestId("archive-preview").waitFor({ timeout: 15_000 });
  await showWizardStep(page, "Cluster & Runtime");
  assert.equal(
    await page.getByRole("radio", { name: /Auto-Placement/ }).isDisabled(),
    true,
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
  await page
    .getByRole("button", { name: "Create Server", exact: true })
    .click();
  await page
    .getByText(
      "This archive has blocking findings. Resolve them before creating.",
      {
        exact: true,
      },
    )
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
    "PASS: Create Server library preset, datapack upload, compatible mods and module selection, partial-install retry, and archive preview/create retry",
  );
} finally {
  await browser?.close();
  await server?.close();
  await fs.rm(cache, { recursive: true, force: true });
}
