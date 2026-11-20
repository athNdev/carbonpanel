// SPDX-License-Identifier: MPL-2.0
/**
 * Scoped real-browser check for the World View detail route. Mounts the actual
 * SvelteKit route with local Connect fixtures (the normal entry point) and
 * proves that action-scoped write denials do NOT revoke an otherwise readable
 * world view. The read scope stays valid; only a specific write RPC is refused
 * at the network boundary, which is the real regression case. No live server,
 * world, renderer or daemon is used and the sandbox frame is a harmless
 * fixture document. Fixture-only browser check: no live BlueMap claim.
 */
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
const { createServer } = await import(pathToFileURL(require.resolve("vite")).href);

const evidence = path.resolve(
  process.env.WORLDVIEW_DENIAL_EVIDENCE ||
    path.resolve(here, "../../../tmp/review-evidence/worldview-scoped-denial"),
);
await fs.mkdir(evidence, { recursive: true });

const results = [];
const writeCalls = [];
let denyWrite = ""; // "" | SetPlayerVisibility | SetMapOverlay | SendCommand
let readDenied = false;

const worldRef = { kind: "KIND_SERVER_LOCAL", serverId: "srv-1", levelName: "survival" };
const target = {
  serverId: "srv-1",
  worldRef,
  name: "Fixture Overworld",
  dimension: "overworld",
  rendererType: "RENDERER_TYPE_BLUEMAP",
  rendererState: "RENDERER_STATE_READY",
  renderer: {
    type: "RENDERER_TYPE_BLUEMAP",
    version: "fixture",
    capabilities: ["map.2d", "dimensions", "markers", "players"],
    webBasePath: "/maps/fixture",
  },
  nodeId: "local",
};

let viteServer;
let browser;
try {
  viteServer = await createServer({
    root: project,
    server: { host: "127.0.0.1", port: 0, strictPort: false },
  });
  await viteServer.listen();
  const base = `http://127.0.0.1:${viteServer.httpServer.address().port}`;
  ({ browser } = await launchReviewBrowser());

  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));

  await page.route(/\/carbonpanel\.v1\.[^/]+\/[^/?]+/, async (route) => {
    const url = new URL(route.request().url());
    const method = url.pathname.split("/").at(-1);
    const payload = route.request().postDataJSON() ?? {};
    const reply = (body, status = 200) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(body),
      });
    // Connect maps non-2xx status to an error Code; a 200 body would be
    // accepted as success and would not exercise the client's catch path.
    const denied = () =>
      reply({ code: "permission_denied", message: "insufficient scope for this write" }, 403);

    if (method === "GetAuthStatus")
      return reply({ localAuthEnabled: false, oidcEnabled: false, anonymousAccessEnabled: false });
    if (method === "GetCurrentUser")
      return reply({
        user: { id: "fixture", username: "Fixture" },
        // All action scopes visible; the backend writes are individually denied below.
        permissions: [{ resource: "*", action: "*", objectId: "*" }],
      });
    if (method === "ListWorldViewTargets")
      return readDenied ? denied() : reply({ targets: [target] });
    if (method === "GetWorldViewStatus")
      return readDenied ? denied() : reply({ target });
    if (method === "ListCapabilities")
      return reply({
        rendererType: "RENDERER_TYPE_BLUEMAP",
        capabilities: target.renderer.capabilities.map((name) => ({
          name,
          supported: true,
          detail: "",
        })),
      });
    if (method === "ListMapOverlays") return reply({ overlays: [] });
    if (["SetMapOverlay", "SetPlayerVisibility", "SendCommand"].includes(method)) {
      writeCalls.push({ method, payload });
      if (method === denyWrite) return denied();
      if (method === "SetMapOverlay") return reply({ overlay: payload.overlay ?? {} });
      if (method === "SetPlayerVisibility") return reply({ visible: Boolean(payload.visible) });
      return reply({ success: true });
    }
    if (method === "GetWorldViewSession")
      return reply({
        state: "RENDERER_STATE_READY",
        proxyPath: "/api/v1/worldview/map/fixture-key/",
      });
    return reply({});
  });
  // Same-origin map assets: harmless document so the sandbox iframe has no live dependency.
  await page.route("**/api/v1/**", (route) =>
    route.fulfill({
      status: 200,
      contentType: "text/html",
      body: "<!doctype html><html><body>fixture map</body></html>",
    }),
  );

  // encodeWorldKey packs [kind, serverId, levelName, bindingId, snapshotId];
  // KIND_SERVER_LOCAL = 2 (worldview_pb.ts).
  const packed = [2, "srv-1", "survival", "", ""];
  const key = "w1." + Buffer.from(JSON.stringify(packed)).toString("base64url");
  const url = `${base}/worlds/srv-1/${key}?dimension=overworld`;

  // Warm route dependencies; Vite's initial optimization reloads the page.
  await page.goto(base);
  await page.waitForLoadState("networkidle");

  async function load() {
    await page.goto(url);
    await page.getByTestId("showcase-world-map-content").waitFor({ timeout: 60000 });
    await page.getByRole("region", { name: /World map/i }).waitFor({ timeout: 60000 });
    await page.waitForTimeout(500);
  }
  async function assertReadView(label) {
    assert.equal(
      await page.getByText("No world-view access", { exact: true }).count(),
      0,
      `${label}: page must not escalate to access-denied`,
    );
    assert.ok(await page.getByRole("region", { name: /World map/i }).count(), `${label}: map region gone`);
    assert.match(await page.locator("body").innerText(), /Fixture Overworld/, `${label}: read title gone`);
  }
  async function openOverlays() {
    const btn = page.getByRole("button", { name: /^Overlays/ });
    if ((await btn.getAttribute("aria-expanded")) !== "true") await btn.click();
  }
  async function dumpOnFailure(label, fn) {
    try {
      await fn();
    } catch (error) {
      const body = await page.locator("body").innerText().catch(() => "(no body)");
      await fs.writeFile(path.join(evidence, `FAIL-${label}.txt`), `${error}\n\n${body}`);
      throw error;
    }
  }

  await dumpOnFailure("ready", load);
  await assertReadView("ready");
  await fs.writeFile(path.join(evidence, "01-read-view-ready.txt"), await page.locator("body").innerText());
  results.push("read-only map surface renders with READY target and no access-denied state");

  // 1. observe_players write denial via the real "Show player markers" action.
  denyWrite = "SetPlayerVisibility";
  let baseline = writeCalls.length;
  await dumpOnFailure("observe-denial", async () => {
    await openOverlays();
    await page.getByRole("button", { name: /Show player markers/i }).click();
    await page.waitForTimeout(700);
  });
  assert.ok(
    writeCalls.slice(baseline).some((c) => c.method === "SetPlayerVisibility"),
    "SetPlayerVisibility must actually be attempted",
  );
  await assertReadView("observe-denial");
  await fs.writeFile(
    path.join(evidence, "02-observe-denial-read-view.txt"),
    await page.locator("body").innerText(),
  );
  results.push("observe_players write denial keeps the readable map view (write was attempted)");

  // 2. world_view/update overlay write denial via the real "Add overlay" action.
  denyWrite = "SetMapOverlay";
  baseline = writeCalls.length;
  await dumpOnFailure("update-denial", async () => {
    await openOverlays();
    await page.getByRole("button", { name: /Add overlay/i }).click();
    const modal = page.getByRole("dialog");
    await modal.waitFor();
    await modal.getByLabel("Label").fill("Fixture marker");
    await modal.getByLabel("X").fill("1");
    await modal.getByLabel("Z").fill("2");
    await modal.getByRole("button", { name: /Save overlay/i }).click();
    await page.waitForTimeout(700);
  });
  assert.ok(
    writeCalls.slice(baseline).some((c) => c.method === "SetMapOverlay"),
    "SetMapOverlay must actually be attempted",
  );
  await assertReadView("update-denial");
  await fs.writeFile(
    path.join(evidence, "03-update-denial-read-view.txt"),
    await page.locator("body").innerText(),
  );
  results.push("world_view/update write denial keeps the readable map view (write was attempted)");

  // 3. servers/command admin denial via the real confirmed admin action.
  // The overlay modal intentionally stays open after a denied save; close it.
  const leftover = page.getByRole("dialog");
  if (await leftover.count())
    await leftover.first().getByRole("button", { name: /Cancel/i }).first().click();
  denyWrite = "SendCommand";
  baseline = writeCalls.length;
  await dumpOnFailure("command-denial", async () => {
    const admin = page.getByRole("button", { name: /^Administration/ });
    if ((await admin.getAttribute("aria-expanded")) !== "true") await admin.click();
    await page.getByRole("button", { name: /Save world now/i }).click();
    await page.getByRole("button", { name: /Run command/i }).click();
    await page.waitForTimeout(700);
  });
  assert.ok(
    writeCalls.slice(baseline).some((c) => c.method === "SendCommand"),
    "SendCommand must actually be attempted",
  );
  await assertReadView("command-denial");
  await fs.writeFile(
    path.join(evidence, "04-command-denial-read-view.txt"),
    await page.locator("body").innerText(),
  );
  results.push("servers/command write denial keeps the readable map view (write was attempted)");

  // 4. Genuine read denial still shows the explicit no-access state.
  denyWrite = "";
  readDenied = true;
  await dumpOnFailure("read-denial", async () => {
    await page.reload();
    await page.getByTestId("showcase-world-map-content").waitFor({ timeout: 60000 });
    await page.getByText("No world-view access", { exact: true }).first().waitFor({ timeout: 30000 });
  });
  await fs.writeFile(
    path.join(evidence, "05-read-denial-no-access.txt"),
    await page.locator("body").innerText(),
  );
  results.push("world_view/read denial still produces the explicit no-access state");

  assert.deepEqual(errors, [], `page errors: ${errors.join("; ")}`);
  await fs.writeFile(
    path.join(evidence, "report.json"),
    JSON.stringify({ results, endPoint: "real-world-detail-route", writeCalls, errors }, null, 2),
  );
  console.log(`PASS: ${results.length} scoped browser checks; 0 page errors; evidence in ${evidence}`);
} finally {
  if (browser) await browser.close().catch(() => {});
  if (viteServer) await viteServer.close().catch(() => {});
}
