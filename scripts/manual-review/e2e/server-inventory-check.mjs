// SPDX-License-Identifier: MPL-2.0
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";
import { launchReviewBrowser } from "../../review/browser.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const project = path.resolve(here, "../../../web/carbon-panel");
const require = createRequire(path.join(project, "package.json"));
process.chdir(project);
const { createServer } = await import(
  pathToFileURL(require.resolve("vite")).href
);
let server, browser;
const results = [];
try {
  server = await createServer({
    root: project,
    server: { host: "127.0.0.1", port: 0, strictPort: false },
  });
  await server.listen();
  const base = `http://127.0.0.1:${server.httpServer.address().port}`;
  ({ browser } = await launchReviewBrowser());
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const requests = [];
  let holding = false;
  let held = [];
  let failList = false;
  let empty = false;
  let authEnabled = false;
  let validToken = "fixture-token";
  let tokenCounter = 0;
  let delayedStatus = 0;
  let delayedErrors = [];
  let fleet = [
    {
      id: "ready",
      name: "Ready Server",
      status: "SERVER_STATUS_STOPPED",
      modLoader: "MOD_LOADER_VANILLA",
      port: 25565,
      memory: 1024,
    },
    {
      id: "error",
      name: "Issue Server",
      status: "SERVER_STATUS_ERROR",
      modLoader: "MOD_LOADER_VANILLA",
      statusReason: "Fixture failure",
    },
    {
      id: "warning",
      name: "Warning Server",
      status: "SERVER_STATUS_UNHEALTHY",
      modLoader: "MOD_LOADER_VANILLA",
    },
  ];
  await page.route(/\/carbonpanel\.v1\.[^/]+\/[^/?]+/, async (route) => {
    const url = new URL(route.request().url());
    const method = url.pathname.split("/").at(-1);
    const payload = route.request().postDataJSON() ?? {};
    requests.push({ method, payload });
    const reply = (body) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(body),
      });
    if (method === "GetAuthStatus")
      return reply({ localAuthEnabled: authEnabled, oidcEnabled: false });
    if (method === "Login") {
      validToken = `fixture-token-${++tokenCounter}`;
      return reply({
        token: validToken,
        user: { id: "fixture", username: "Fixture" },
      });
    }
    if (
      method === "GetCurrentUser" &&
      authEnabled &&
      route.request().headers().authorization !== `Bearer ${validToken}`
    )
      return reply({});
    if (method === "GetCurrentUser")
      return reply({
        user: { id: "fixture", username: "Fixture" },
        permissions: [{ resource: "*", action: "*", objectId: "*" }],
      });
    if (method === "ListServers") {
      if (delayedStatus) {
        const status = delayedStatus;
        await new Promise((resolve) => delayedErrors.push(resolve));
        return route.fulfill({
          status,
          contentType: "application/json",
          body: JSON.stringify({
            code: status === 401 ? "unauthenticated" : "invalid_argument",
            message: "Old session failure",
          }),
        });
      }
      if (holding) {
        await new Promise((resolve) => held.push(resolve));
      }
      if (failList)
        return route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({
            code: "unavailable",
            message: "Fixture offline",
          }),
        });
      return reply({ servers: empty ? [] : fleet });
    }
    if (method === "GetServer")
      return reply({ server: fleet.find((s) => s.id === payload.id) });
    if (method === "StartServer") {
      fleet = fleet.map((s) =>
        s.id === payload.id ? { ...s, status: "SERVER_STATUS_RUNNING" } : s,
      );
      return reply({ status: "starting" });
    }
    if (method === "ExportWorld")
      return reply({ sessionId: "fixture-session", filename: "world.zip" });
    return reply({});
  });
  await page.route("**/api/v1/**", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: "{}" }),
  );
  // Warm route dependencies before exercising deferred responses. Vite may reload
  // while discovering a route's dependencies; those reloads are setup, not user actions.
  await page.goto(`${base}/servers/error`);
  await page.waitForLoadState("networkidle");
  await page.goto(base);
  await page.waitForLoadState("networkidle");
  await page.goto(`${base}/servers`);
  await page
    .getByRole("link", { name: "Ready Server", exact: true })
    .last()
    .waitFor();
  assert.ok(await page.getByText("ERROR", { exact: true }).count());
  assert.ok(await page.getByText("UNHEALTHY", { exact: true }).count());
  await page
    .getByRole("button", { name: "Start server Ready Server", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Stop server Ready Server", exact: true })
    .waitFor();
  const start = requests.find((r) => r.method === "StartServer");
  assert.ok(start.payload.requestId);
  assert.equal(start.payload.id, "ready");
  results.push(
    "direct list hydrates; shared status policy and lifecycle request work",
  );
  for (const width of [360, 768, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth > window.innerWidth,
      ),
      false,
      `list overflow at ${width}`,
    );
  }
  results.push("list has no document overflow at 360/768/1440");
  // Resolve a real list request after removal through the same inventory instance.
  holding = true;
  await page.evaluate(() => {
    window.__oldRefresh = import("/src/lib/stores/servers.ts").then(
      ({ serverInventory }) => serverInventory.refresh(true),
    );
  });
  const deadline = Date.now() + 15000;
  while (held.length === 0 && Date.now() < deadline)
    await new Promise((resolve) => setTimeout(resolve, 10));
  assert.ok(held.length, "deferred list request must reach the fixture");
  await page.evaluate(async () => {
    const { serverInventory } = await import("/src/lib/stores/servers.ts");
    serverInventory.remove("ready");
  });
  holding = false;
  held.splice(0).forEach((release) => release());
  await page.evaluate(() => window.__oldRefresh);
  assert.equal(
    await page.getByRole("link", { name: "Ready Server", exact: true }).count(),
    0,
  );
  results.push("late real list completion cannot restore removed server");
  empty = true;
  await page.reload();
  await page
    .getByText("No servers configured", { exact: false })
    .first()
    .waitFor();
  results.push("empty inventory is distinct from loading");
  failList = true;
  await page.reload();
  await page
    .getByRole("heading", { name: "Failed to load servers", exact: true })
    .waitFor();
  results.push("direct load failure is visible");
  failList = false;
  empty = false;
  await page.goto(base);
  await page.getByText("Ready Server", { exact: true }).last().waitFor();
  results.push("dashboard uses the shared inventory");
  await page.goto(`${base}/servers/error`);
  await page.getByText("Issue Server", { exact: true }).last().waitFor();
  await page.getByText("Fixture failure", { exact: false }).first().waitFor();
  results.push("direct detail uses current inventory and failure cause");
  authEnabled = true;
  await page.goto(`${base}/login`);
  await page.getByLabel("Username", { exact: true }).waitFor();
  const beforeLogin = requests.filter((r) => r.method === "ListServers").length;
  await page.getByLabel("Username", { exact: true }).fill("fixture");
  await page.getByLabel("Password", { exact: true }).fill("fixture-password");
  await page.getByRole("button", { name: "Sign In", exact: true }).click();
  await page.getByText("Ready Server", { exact: true }).last().waitFor();
  assert.ok(
    requests.filter((r) => r.method === "ListServers").length > beforeLogin,
  );
  results.push("real login form hydrates a fresh inventory");
  holding = true;
  await page.evaluate(() => {
    window.__oldRefresh = import("/src/lib/stores/servers.ts").then(
      ({ serverInventory }) => serverInventory.refresh(true),
    );
  });
  const logoutDeadline = Date.now() + 15000;
  while (held.length === 0 && Date.now() < logoutDeadline)
    await new Promise((resolve) => setTimeout(resolve, 10));
  assert.ok(held.length);
  await page.evaluate(async () => {
    const { authStore } = await import("/src/lib/stores/auth.ts");
    await authStore.logout();
  });
  await page.waitForURL("**/login");
  await page.getByLabel("Username", { exact: true }).waitFor();
  holding = false;
  held.splice(0).forEach((release) => release());
  await page.evaluate(() => window.__oldRefresh);
  assert.equal(
    await page.evaluate(async () => {
      const { serverInventory } = await import("/src/lib/stores/servers.ts");
      return Boolean(serverInventory.get("ready"));
    }),
    false,
  );
  results.push(
    "logout clears inventory and rejects a late authenticated response",
  );
  for (const status of [401, 400]) {
    await page.getByLabel("Username", { exact: true }).fill("fixture");
    await page.getByLabel("Password", { exact: true }).fill("fixture-password");
    await page.getByRole("button", { name: "Sign In", exact: true }).click();
    await page.getByText("Ready Server", { exact: true }).last().waitFor();
    delayedStatus = status;
    await page.evaluate(() => {
      window.__oldRejected = import("/src/lib/api/rpc-client.ts")
        .then(({ rpcClient }) =>
          rpcClient.server.listServers({ fullStats: true }),
        )
        .catch(() => {});
    });
    const errorDeadline = Date.now() + 15000;
    while (delayedErrors.length === 0 && Date.now() < errorDeadline)
      await new Promise((resolve) => setTimeout(resolve, 10));
    assert.ok(delayedErrors.length);
    delayedStatus = 0;
    await page.evaluate(async () => {
      const { authStore } = await import("/src/lib/stores/auth.ts");
      await authStore.logout();
    });
    await page.waitForURL("**/login");
    await page.getByLabel("Username", { exact: true }).fill("fixture");
    await page.getByLabel("Password", { exact: true }).fill("fixture-password");
    await page.getByRole("button", { name: "Sign In", exact: true }).click();
    await page.getByText("Ready Server", { exact: true }).last().waitFor();
    const logoutCalls = requests.filter(
      (request) => request.method === "Logout",
    ).length;
    delayedErrors.splice(0).forEach((release) => release());
    await page.evaluate(() => window.__oldRejected);
    await page.waitForTimeout(100);
    assert.equal(
      requests.filter((request) => request.method === "Logout").length,
      logoutCalls,
      "old account failure triggered logout",
    );
    assert.equal(
      await page.evaluate(async () => {
        const { authStore } = await import("/src/lib/stores/auth.ts");
        let authenticated = false;
        const off = authStore.subscribe((state) => {
          authenticated = state.isAuthenticated;
        });
        off();
        return authenticated;
      }),
      true,
    );
    assert.equal(
      await page.getByText("Old session failure", { exact: true }).count(),
      0,
    );
    results.push(
      `delayed previous-account HTTP ${status} cannot log out or toast in the new session`,
    );
    await page.evaluate(async () => {
      const { authStore } = await import("/src/lib/stores/auth.ts");
      await authStore.logout();
    });
    await page.waitForURL("**/login");
  }
  assert.deepEqual(errors, []);
  const out = path.join(here, "out", "server-inventory");
  await fs.mkdir(out, { recursive: true });
  await page.screenshot({ path: path.join(out, "inventory.png") });
  await fs.writeFile(
    path.join(out, "results.json"),
    JSON.stringify({ results, errors }, null, 2),
  );
  console.log(
    `PASS: ${results.length} real inventory route scenarios; no page errors`,
  );
} finally {
  await browser?.close();
  await server?.close();
}
