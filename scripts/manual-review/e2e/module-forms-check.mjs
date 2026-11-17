// SPDX-License-Identifier: MPL-2.0
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";
import { launchReviewBrowser } from "../../review/browser.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const fixture = path.join(here, "module-forms");
const project = path.resolve(here, "../../../web/carbon-panel");
const requireFrontend = createRequire(path.join(project, "package.json"));
const { createServer, build, preview } = await import(
  pathToFileURL(requireFrontend.resolve("vite")).href
);
const { svelte } = await import(
  pathToFileURL(requireFrontend.resolve("@sveltejs/vite-plugin-svelte")).href
);
const { default: tailwind } = await import(
  pathToFileURL(requireFrontend.resolve("@tailwindcss/vite")).href
);
const cache = await fs.mkdtemp(
  path.join(os.tmpdir(), "carbon-module-browser-"),
);
let server;
let browser;

try {
  const viteConfig = {
    configFile: false,
    root: project,
    cacheDir: cache,
    publicDir: path.join(project, "static"),
    resolve: {
      dedupe: ["svelte"],
      alias: [
        {
          find: /^monaco-editor$/,
          replacement: path.join(
            project,
            "node_modules/monaco-editor/esm/vs/editor/editor.main.js",
          ),
        },
        {
          find: /^@bufbuild\/protobuf$/,
          replacement: path.join(
            project,
            "node_modules/@bufbuild/protobuf/dist/esm/index.js",
          ),
        },
        {
          find: "$lib/api/rpc-client",
          replacement: path.join(fixture, "rpc-stub.js"),
        },
        {
          find: /^\$lib\/components\/carbon$/,
          replacement: path.join(here, "editor-dialog", "carbon.js"),
        },
        { find: "$lib", replacement: path.join(project, "src/lib") },
      ],
    },
    plugins: [
      svelte({ configFile: path.join(project, "svelte.config.js") }),
      tailwind(),
      {
        name: "module-forms-regression",
        configureServer(vite) {
          vite.middlewares.use(async (req, res, next) => {
            if (req.url === "/favicon.ico") {
              res.statusCode = 204;
              res.end();
              return;
            }
            if (req.url !== "/module-forms.html") return next();
            try {
              const entry =
                "/@fs/" + path.join(fixture, "main.js").replaceAll("\\", "/");
              res.setHeader("Content-Type", "text/html");
              res.end(
                await vite.transformIndexHtml(
                  "/module-forms.html",
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
    server: {
      host: "127.0.0.1",
      port: 0,
      hmr: false,
      fs: { allow: [project, fixture, cache] },
    },
    optimizeDeps: {
      include: [
        "svelte",
        "monaco-editor",
        "bits-ui",
        "svelte-sonner",
        "@lucide/svelte",
        "@lucide/svelte/icons/x",
        "tailwind-merge",
      ],
    },
  };
  server = await createServer(viteConfig);
  await server.listen();
  ({ browser } = await launchReviewBrowser());
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1000 },
  });
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const base = `http://127.0.0.1:${server.httpServer.address().port}/module-forms.html`;
  await page.goto(base);
  await page.waitForFunction(() => Boolean(globalThis.__moduleUi));
  await page.evaluate(() => __moduleUi.template());
  await page.getByRole("button", { name: "Volumes", exact: true }).click();
  assert.equal(
    await page.getByPlaceholder("/host/path or {{alias}}").inputValue(),
    "/data/fixture",
    "persisted template source must populate its editable host path",
  );
  await page.getByRole("button", { name: "Save Changes", exact: true }).click();
  await page.waitForFunction(() => __moduleCalls.length === 1);
  let calls = await page.evaluate(() => __moduleCalls);
  assert.equal(calls[0].method, "updateModuleTemplate");
  assert.equal(
    calls[0].payload.defaultVolumes,
    '[{"source":"/data/fixture","target":"/mount","read_only":true,"create_dir":false}]',
  );
  assert.equal(calls[0].payload.defaultEnv, '{"A":" value "}');
  await page.evaluate(() => __moduleUi.instance("create"));
  await page.getByText("Fixture Template", { exact: true }).first().click();
  await page
    .getByRole("button", { name: "Create Module", exact: true })
    .waitFor();
  await page
    .getByRole("button", { name: "Create Module", exact: true })
    .click();
  await page.waitForFunction(() => __moduleCalls.length === 2);
  calls = await page.evaluate(() => __moduleCalls);
  assert.equal(calls[1].method, "createModule");
  assert.equal(calls[1].payload.templateId, "template");
  assert.equal(calls[1].payload.ports[0].hostPort, 8123);
  assert.equal(calls[1].payload.uid, "1000");
  assert.equal(calls[1].payload.memory, 512);
  assert.equal(
    calls[1].payload.volumeOverrides,
    calls[0].payload.defaultVolumes,
  );
  await page.evaluate(() => __moduleUi.instance("edit"));
  await page
    .getByRole("button", { name: "Save Changes", exact: true })
    .waitFor();
  await page
    .getByText("Stage into the open batch", { exact: true })
    .locator("..")
    .getByRole("switch")
    .click();
  await page.getByRole("button", { name: "Save Changes", exact: true }).click();
  await page.waitForFunction(() => __moduleCalls.length === 3);
  calls = await page.evaluate(() => __moduleCalls);
  assert.equal(calls[2].method, "updateModule");
  assert.equal(calls[2].payload.stage, true);
  for (const field of [
    "ports",
    "dependencies",
    "eventHooks",
    "metadata",
    "healthCheckInterval",
  ])
    assert.equal(
      field in calls[2].payload,
      false,
      `scalar stage included ${field}`,
    );
  await page.evaluate(() => __moduleUi.instance("edit"));
  await page
    .getByRole("button", { name: "Save Changes", exact: true })
    .waitFor();
  await page
    .getByText("Stage into the open batch", { exact: true })
    .locator("..")
    .getByRole("switch")
    .click();
  await page.getByRole("button", { name: "Save Changes", exact: true }).click();
  await page.waitForFunction(() => __moduleCalls.length === 4);
  calls = await page.evaluate(() => __moduleCalls);
  assert.equal(calls[3].payload.stage, false);
  assert.equal(calls[3].payload.ports.length, 1);
  assert.equal(calls[3].payload.eventHooks.length, 1);
  assert.equal(calls[3].payload.healthCheckInterval, 30);
  assert.deepEqual(errors, []);
  const out = path.join(here, "out", "module-forms");
  await fs.mkdir(out, { recursive: true });
  await page.screenshot({ path: path.join(out, "forms.png") });
  await fs.writeFile(
    path.join(out, "results.json"),
    JSON.stringify(
      {
        scenarios: [
          "stored template volume/env roundtrip",
          "template-to-instance default/port request",
          "scalar staging omits topology",
          "immediate update keeps topology and numeric defaults",
        ],
        errors,
      },
      null,
      2,
    ),
  );
  console.log("PASS: 4 real module form request scenarios; no page errors");
} finally {
  await browser?.close();
  await server?.close();
  await fs.rm(cache, { recursive: true, force: true });
}
