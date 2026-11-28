// SPDX-License-Identifier: MPL-2.0
/**
 * Real-browser checks for the World View inventory routes and the map dialog
 * on the server detail page. Both surfaces exist at this head: /worlds lists
 * maps, and the server World tab's "Open map" action opens the same map in a
 * dialog that ?map=<world key> deep-links. The actual SvelteKit app uses
 * local Connect fixtures so the journey can check navigation, recovery, and
 * setup alongside the scoped permissions. It proves that action-scoped write
 * denials do NOT revoke an otherwise readable
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
// false = the account holds no world_view/read grant at all, so the UI gates
// on permissions alone without firing a map RPC.
let worldViewRead = true;
let statusMode = "ready";
let nodeIsLocal = true;
let createModuleCalls = 0;
let scopedPermission = false;
let fleetCalls = 0;
let scopedCalls = 0;
let sessionKey = "fixture-key";
let sessionExpiresAt;
let issueShortSession = false;
let sessionUnavailable = false;
let holdSession;
let holdStatus;

const worldRef = { kind: "KIND_SERVER_LOCAL", serverId: "srv-1", levelName: "survival" };
const survivalMapLink = { name: "Open map for Fixture Overworld on Fixture Survival Server", exact: true };
const creativeMapLink = { name: "Set up map for Fixture Creative on Fixture Creative Server", exact: true };
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

const otherTarget = {
  ...target,
  serverId: "srv-2",
  worldRef: { ...worldRef, serverId: "srv-2", levelName: "creative" },
  name: "Fixture Creative",
  rendererType: "RENDERER_TYPE_UNSPECIFIED",
  rendererState: "RENDERER_STATE_ABSENT",
  renderer: undefined,
};

let viteServer;
let browser;
try {
  let base = process.env.WORLDVIEW_DENIAL_APP_URL;
  if (!base) {
    viteServer = await createServer({
      root: project,
      server: { host: "127.0.0.1", port: 0, strictPort: false },
    });
    await viteServer.listen();
    base = "http://127.0.0.1:" + viteServer.httpServer.address().port;
  }
  base = base.replace(/\/$/, "");
  ({ browser } = await launchReviewBrowser());

  const fixtureSession = "fixture-worldview-session";
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
    reducedMotion: "reduce",
    storageState: {
      cookies: [],
      origins: [{ origin: new URL(base).origin, localStorage: [{ name: "auth_token", value: fixtureSession }] }],
    },
  });
  const page = await context.newPage();
  const errors = [];
  let createModuleMode = "failure";
  const recoveryRequests = [];
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
      return reply({ localAuthEnabled: true, oidcEnabled: false, anonymousAccessEnabled: false });
    if (method === "GetCurrentUser")
      return reply({
        user: { id: "fixture", username: "Fixture" },
        // All action scopes visible; the backend writes are individually denied below.
        permissions: !worldViewRead
          ? [
              { resource: "servers", action: "read", objectId: "*" },
              { resource: "worlds", action: "read", objectId: "*" },
            ]
          : scopedPermission
            ? [
                { resource: "servers", action: "read", objectId: "*" },
                { resource: "worlds", action: "read", objectId: "*" },
                { resource: "world_view", action: "read", objectId: "srv-1" },
              ]
            : [{ resource: "*", action: "*", objectId: "*" }],
      });
    if (method === "ListServers")
      return reply({
        servers: [
          { id: "srv-1", name: "Fixture Survival Server" },
          { id: "srv-2", name: "Fixture Creative Server" },
        ],
      });
    // The map dialog lives on the server detail route, so the fixture must
    // answer the detail page's own reads for both servers.
    if (method === "GetServer")
      return reply({
        server: {
          id: payload.id === "srv-2" ? "srv-2" : "srv-1",
          name: payload.id === "srv-2" ? "Fixture Creative Server" : "Fixture Survival Server",
          status: "SERVER_STATUS_RUNNING",
        },
      });
    // Keeps the World tab's "Open map" action pointed at the script's packed
    // key: levelName "survival", no library binding.
    if (method === "ListBindings")
      return reply({ configuredLevelName: "survival", bindings: [] });
    if (method === "ListWorldViewTargets") {
      fleetCalls += 1;
      return readDenied || scopedPermission ? denied() : reply({ targets: [target, otherTarget] });
    }
    if (method === "GetWorldViewStatus") {
      if (!payload.worldRef) scopedCalls += 1;
      if (holdStatus && payload.serverId === "srv-1") {
        const pending = holdStatus;
        holdStatus = undefined;
        await pending;
        return denied();
      }
      if (readDenied) return denied();
      if (payload.serverId === "srv-2")
        return reply({ target: { ...otherTarget, rendererState: target.rendererState, renderer: target.renderer } });
      if (statusMode === "unknown")
        return reply({ target: { ...target, rendererState: "RENDERER_STATE_UNSPECIFIED" } });
      if (statusMode === "missing") return reply({});
      if (statusMode === "error")
        return reply({ code: "unavailable", message: "Fixture status temporarily unavailable" }, 503);
      if (statusMode === "setup")
        return reply({
          target: {
            ...target,
            rendererType: "RENDERER_TYPE_UNSPECIFIED",
            rendererState: "RENDERER_STATE_ABSENT",
            renderer: undefined,
          },
        });
      return reply({ target });
    }
    if (method === "ListNodes") return reply({ nodes: [{ id: "local", isLocal: nodeIsLocal }] });
    if (method === "ListModules") return reply({ modules: [] });
    if (method === "CreateModule") {
      createModuleCalls += 1;
      if (createModuleMode !== "failure") {
        const reuse = payload.metadata?.bluemap_reuse_config;
        recoveryRequests.push({ reuse: reuse === "true", consent: payload.metadata?.bluemap_consent_accepted });
        if (createModuleMode === "reuse-ready" && reuse === "true") {
          statusMode = "ready";
          return reply({ module: { id: "recovered-bluemap", serverId: "srv-1", templateId: "builtin-bluemap" } });
        }
        return route.fulfill({
          status: 400,
          headers: {
            "Content-Type": "application/json",
            "Carbonpanel-Bluemap-Setup": reuse === "true" ? "download-consent" : "retained-config",
          },
          json: { code: "failed_precondition", message: "Retained BlueMap configuration" },
        });
      }
      return reply({ code: "failed_precondition", message: "Fixture install failure" }, 400);
    }
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
    if (method === "GetWorldViewSession") {
      if (holdSession && payload.serverId === "srv-1") {
        const pending = holdSession;
        holdSession = undefined;
        await pending;
        return reply({ code: "unavailable", message: "Late session failure" }, 503);
      }
      if (sessionUnavailable) return reply({ code: "unavailable", message: "Renewal unavailable" }, 503);
      if (issueShortSession) {
        issueShortSession = false;
        sessionExpiresAt = new Date(Date.now() + 4_000).toISOString();
        sessionUnavailable = true;
      }
      return reply({
        state: "RENDERER_STATE_READY",
        proxyPath: "/api/v1/worldview/map/" + sessionKey + "/",
        expiresAt: sessionExpiresAt,
      });
    }
    return reply({});
  });
  // Same-origin map assets: harmless document so the sandbox iframe has no live dependency.
  await page.route("**/api/v1/worldview/map/**", async (route) => {
    const cookie = (await route.request().headerValue("cookie")) ?? "";
    return route.fulfill({
      status: cookie.split(/;\s*/).includes(`cp_session=${fixtureSession}`) ? 200 : 401,
      contentType: "text/html",
      body: "<!doctype html><html><body>fixture map</body></html>",
    });
  });

  // encodeWorldKey packs [kind, serverId, levelName, bindingId, snapshotId];
  // KIND_SERVER_LOCAL = 2 (worldview_pb.ts).
  const packed = [2, "srv-1", "survival", "", ""];
  const key = "w1." + Buffer.from(JSON.stringify(packed)).toString("base64url");
  // The map dialog on the server detail page is deep-linkable via
  // ?map=<world key>; the /worlds inventory routes are still present and are
  // exercised by the world-inventory passes below.
  const url = `${base}/servers/srv-1?map=${key}&dimension=overworld`;

  // Warm route dependencies; Vite's initial optimization reloads the page.
  await page.goto(base);
  await page.waitForLoadState("networkidle");
  const cookie = (await context.cookies(base)).find((entry) => entry.name === "cp_session");
  assert.equal(cookie?.value, fixtureSession, "restored auth must set the cookie before the viewer loads");
  assert.equal(cookie?.sameSite, "Strict");

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

  // First pass enters through the real control: the World tab's "Open map"
  // button opens the same dialog that the ?map= deep link renders.
  await dumpOnFailure("ready", async () => {
    await page.goto(base + "/servers/srv-1?tab=world");
    await page.getByRole("button", { name: "Open map" }).click();
    await page.getByTestId("showcase-world-map-content").waitFor({ timeout: 60000 });
    await page.getByRole("region", { name: /World map/i }).waitFor({ timeout: 60000 });
    await page.waitForTimeout(500);
  });
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
    // The map itself renders inside a dialog now; scope to the overlay modal
    // by its title so the two dialogs are never confused.
    const modal = page.getByRole("dialog", { name: "Add overlay" });
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
  // (The map dialog is also a role=dialog, so scope by the modal's title.)
  const leftover = page.getByRole("dialog", { name: "Add overlay" });
  if (await leftover.count())
    await leftover.getByRole("button", { name: /Cancel/i }).first().click();
  denyWrite = "SendCommand";
  baseline = writeCalls.length;
  await dumpOnFailure("command-denial", async () => {
    const admin = page.getByRole("button", { name: /^Administration/ });
    if ((await admin.getAttribute("aria-expanded")) !== "true") await admin.click();
    await page.getByRole("button", { name: /Teleport player to marker/ }).click();
    const adminPanelId = await admin.getAttribute("aria-controls");
    const validation = page.locator(`[id="${adminPanelId}"]`).getByRole("alert");
    await validation.filter({ hasText: "Set both map coordinates" }).waitFor();
    assert.equal(writeCalls.length, baseline, "missing coordinates must not send a command");
    const alertBox = await validation.boundingBox();
    const headingBox = await admin.boundingBox();
    assert.ok(alertBox && headingBox && alertBox.y >= headingBox.y + headingBox.height,
      "the validation message belongs below the Administration heading");
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

  readDenied = false;
  // /worlds still exists at this head, so its inventory presentation, server
  // scoping and failed/retry states stay covered alongside the dialog passes
  // that follow.
  await dumpOnFailure("world-inventory", async () => {
    for (const [width, theme] of [[360, "dark"], [768, "light"], [1440, "dark"]]) {
      await page.setViewportSize({ width, height: 900 });
      await page.evaluate((mode) => {
        localStorage.setItem("carbon-theme", mode);
      }, theme);
      await page.goto(base + "/worlds");
      const content = page.getByTestId("showcase-worlds-content");
      const mapLink = content.getByRole("link", survivalMapLink);
      await mapLink.waitFor();
      await mapLink.getByText(/Fixture Survival Server · overworld/).waitFor();
      const setupLink = content.getByRole("link", creativeMapLink);
      await setupLink.getByText("SETUP NEEDED", { exact: true }).waitFor();
      assert.doesNotMatch(await setupLink.innerText(), /Checked |Rendered /);
      assert.equal(await page.locator("html").getAttribute("data-carbon-theme"), theme);
      const overflow = await page.locator("html").evaluate((element) => element.scrollWidth > element.clientWidth);
      assert.equal(overflow, false, "World View fits at width " + width);
      await mapLink.focus();
      assert.equal(await mapLink.evaluate((element) => element === document.activeElement), true);
      await page.screenshot({
        path: path.join(evidence, "inventory-" + width + "-" + theme + ".png"),
        animations: "disabled",
      });
      await page.keyboard.press("Enter");
      await page.getByRole("region", { name: /World map/i }).waitFor();
    }
    await page.goto(base + "/worlds?server=srv-1");
    await page.getByRole("link", survivalMapLink).waitFor();
    assert.equal(await page.getByRole("link", creativeMapLink).count(), 0);
    await page.getByRole("link", { name: "All servers" }).click();
    await page.getByRole("link", creativeMapLink).waitFor();
  });
  results.push("inventory presents names, setup and unknown freshness at 360, 768 and 1440, supports keyboard navigation and preserves server scoping");

  await dumpOnFailure("concrete-server-scope", async () => {
    scopedPermission = true;
    const globalBaseline = fleetCalls;
    const scopedBaseline = scopedCalls;
    const scopedUrl = base + "/worlds?server=srv-1";
    await page.goto(scopedUrl);
    await page.getByRole("link", survivalMapLink).waitFor();
    assert.equal(await page.getByRole("link", { name: "All servers" }).count(), 0);
    assert.equal(fleetCalls, globalBaseline, "concrete server access must not call the fleet RPC");
    assert.ok(scopedCalls > scopedBaseline);

    statusMode = "error";
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await page.getByRole("alert").filter({ hasText: "World inventory refresh failed" }).waitFor();
    assert.equal(await page.getByRole("link", survivalMapLink).count(), 1);
    await page.reload();
    await page.getByRole("heading", { name: "Could not load worlds" }).waitFor();

    statusMode = "ready";
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await page.getByRole("link", survivalMapLink).click();
    await page.getByRole("region", { name: /World map/i }).waitFor();
    assert.equal(await page.getByRole("link", { name: "Server maps", exact: true }).getAttribute("href"), "/worlds?server=srv-1");
    scopedPermission = false;
  });
  results.push("server-scoped reader uses the concrete read RPC and gets separate fresh, stale, failed and navigation states");

  // The dialog passes keep the map's other home covered: the server World tab
  // and its expand dialog.
  await dumpOnFailure("world-tab-read-scope", async () => {
    // Without the world_view/read grant the World tab offers no map action
    // and the dialog deep link renders the lock empty-state without firing
    // any map RPC: the permission alone gates the surface.
    worldViewRead = false;
    const baselineFleet = fleetCalls;
    const baselineScoped = scopedCalls;
    await page.goto(base + "/servers/srv-1?tab=world");
    await page
      .getByText("Opening a map requires World View read access on this server.")
      .waitFor();
    assert.equal(await page.getByRole("button", { name: "Open map" }).count(), 0);
    await page.goto(base + "/servers/srv-1?map=" + key);
    await page.getByRole("dialog", { name: "World map" }).waitFor({ timeout: 60000 });
    await page.getByText("No world-view access", { exact: true }).first().waitFor({ timeout: 30000 });
    assert.equal(await page.getByRole("region", { name: /World map/i }).count(), 0);
    assert.equal(fleetCalls, baselineFleet, "a world_view/read-denied user fires no fleet RPC");
    assert.equal(scopedCalls, baselineScoped, "a world_view/read-denied user fires no status RPC");
    worldViewRead = true;
  });
  results.push("world_view/read-denied users get the lock empty-state on the World tab and dialog with no map RPCs");

  await dumpOnFailure("world-tab-scoped-reader", async () => {
    // A reader scoped to srv-1 opens the dialog from the World tab using
    // concrete per-server reads only.
    scopedPermission = true;
    const globalBaseline = fleetCalls;
    const scopedBaseline = scopedCalls;
    await page.goto(base + "/servers/srv-1?tab=world");
    const openMap = page.getByRole("button", { name: "Open map" });
    await openMap.waitFor();
    await openMap.click();
    await page.getByTestId("showcase-world-map-content").waitFor({ timeout: 60000 });
    await page.getByRole("region", { name: /World map/i }).waitFor({ timeout: 60000 });
    assert.equal(fleetCalls, globalBaseline, "concrete server access must not call the fleet RPC");
    assert.ok(scopedCalls > scopedBaseline, "the World tab uses the concrete per-server status RPC");
    scopedPermission = false;
  });
  results.push("a server-scoped reader opens the dialog from the World tab using concrete reads only");

  await dumpOnFailure("last-known-session", async () => {
    await load();
    const frame = page.getByRole("region", { name: /World map/i }).locator("iframe");
    const initial = await frame.getAttribute("src");
    statusMode = "error";
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await page.getByText("Could not refresh the map. Showing the last known state.", { exact: true }).waitFor();
    assert.equal(await frame.getAttribute("src"), initial);
    statusMode = "ready";
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await page.getByText("Could not refresh the map. Showing the last known state.", { exact: true }).waitFor({ state: "hidden" });

    let release;
    holdSession = new Promise((resolve) => { release = resolve; });
    const oldSessionStarted = page.waitForRequest((request) => request.url().endsWith("/GetWorldViewSession"));
    try {
      await page.getByRole("button", { name: "Refresh", exact: true }).click();
      await oldSessionStarted;
      sessionKey = "new-world-session";
      await page.getByRole("button", { name: "Refresh", exact: true }).click();
      const newerFrame = page.locator('iframe[src$="/new-world-session/"]');
      await newerFrame.waitFor();
      const oldSessionEnded = page.waitForResponse((response) =>
        response.url().endsWith("/GetWorldViewSession") && response.status() === 503
      );
      release();
      await oldSessionEnded;
      await page.evaluate(() => new Promise(requestAnimationFrame));
      assert.equal(await newerFrame.count(), 1);
    } finally {
      release();
      sessionKey = "fixture-key";
    }
    readDenied = true;
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await page.getByText("No world-view access", { exact: true }).waitFor();
    assert.equal(await page.locator("iframe").count(), 0, "read denial removes retained map session");
    readDenied = false;
  });
  results.push("transient status refresh preserves the last-good frame; a late same-world session failure is ignored and a read denial clears it");

  await dumpOnFailure("expired-session-recovery", async () => {
    issueShortSession = true;
    try {
      await load();
      const oldFrame = page.locator('iframe[src$="/fixture-key/"]');
      await page.getByText("Could not refresh the map. Showing the last known state.", { exact: true }).waitFor();
      assert.equal(await oldFrame.count(), 1, "transient renewal retains a usable session");
      await oldFrame.waitFor({ state: "detached" });
      sessionExpiresAt = new Date(Date.now() + 300_000).toISOString();
      sessionKey = "renewed-key";
      sessionUnavailable = false;
      await page.locator('iframe[src$="/renewed-key/"]').waitFor();
    } finally {
      issueShortSession = false;
      sessionUnavailable = false;
      sessionExpiresAt = undefined;
      sessionKey = "fixture-key";
    }
  });
  results.push("transient renewal retains the map until expiry and recovers without a manual refresh");

  await dumpOnFailure("previous-world-denial", async () => {
    await load();
    let release;
    holdStatus = new Promise((resolve) => { release = resolve; });
    const oldStatusStarted = page.waitForRequest((request) => request.url().endsWith("/GetWorldViewStatus"));
    try {
      await page.getByRole("button", { name: "Refresh", exact: true }).click();
      await oldStatusStarted;
      const otherKey = "w1." + Buffer.from(JSON.stringify([2, "srv-2", "creative", "", ""])).toString("base64url");
      // Client-side navigation between two server pages, each carrying its
      // own map dialog deep link, exercises the dialog's cross-server hop.
      await page.evaluate((href) => {
        const link = document.createElement("a");
        link.href = href;
        document.body.append(link);
        link.click();
        link.remove();
      }, "/servers/srv-2?map=" + otherKey);
      await page.getByRole("heading", { name: "Fixture Creative", exact: true }).waitFor();
      await page.getByRole("region", { name: /World map/i }).waitFor();
      const oldStatusEnded = page.waitForResponse((response) =>
        response.url().endsWith("/GetWorldViewStatus") && response.status() === 403
      );
      release();
      await oldStatusEnded;
      await page.evaluate(() => new Promise(requestAnimationFrame));
      assert.equal(await page.getByText("No world-view access", { exact: true }).count(), 0);
      assert.equal(await page.getByRole("region", { name: /World map/i }).count(), 1);
    } finally {
      release();
    }
  });
  results.push("a status denial from the previous world does not revoke the current world on client-side navigation");

  await dumpOnFailure("unavailable-status", async () => {
    await page.setViewportSize({ width: 360, height: 900 });
    for (const mode of ["missing", "error", "unknown"]) {
      statusMode = mode;
      await page.goto(url);
      await page.getByRole("heading", { name: "Map status unavailable" }).waitFor();
      if (mode === "unknown")
        await page.getByText("Carbon Panel received an unrecognized renderer state.", { exact: true }).waitFor();
      if (mode === "missing")
        await page.getByText("Carbon Panel received no map status for this world.", { exact: true }).waitFor();
      assert.equal(await page.getByRole("region", { name: /World map/i }).count(), 0);
      assert.equal(await page.getByRole("button", { name: "View controls" }).count(), 0);
      statusMode = "ready";
      await page.getByRole("button", { name: "Retry", exact: true }).click();
      await page.getByRole("region", { name: /World map/i }).waitFor();
    }
  });
  results.push("missing, unknown, and failed status expose a working retry and keep the map closed until it is ready");

  await dumpOnFailure("renderer-setup", async () => {
    statusMode = "setup";
    nodeIsLocal = false;
    await page.goto(url);
    await page.getByRole("heading", { name: "Set up a map" }).waitFor();
    await page.getByText(/Private renderer discovery is only available on the local node/).waitFor();
    assert.equal(await page.getByRole("button", { name: "Install BlueMap" }).count(), 0);
    assert.equal(await page.getByRole("button", { name: "View controls" }).count(), 0);
    nodeIsLocal = true;
    await page.reload();
    await page.getByRole("button", { name: "Install BlueMap", exact: true }).click();
    const accept = page.getByRole("button", { name: "Accept and install" });
    assert.equal(await accept.isEnabled(), false);
    await page.getByRole("checkbox", { name: /I explicitly accept/ }).check();
    await accept.click();
    await page.getByRole("alert").filter({ hasText: "Fixture install failure" }).waitFor();
    assert.equal(createModuleCalls, 1);
    assert.equal(await page.getByText("Fixture install failure", { exact: false }).count(), 1);
    assert.equal(await page.locator("[data-sonner-toast]").count(), 0);
    await page.screenshot({ path: path.join(evidence, "setup-error-360.png"), animations: "disabled" });
  });
  results.push("remote setup stays guarded and local consented install failure is shown once inline");

  await dumpOnFailure("renderer-recovery", async () => {
    statusMode = "setup";
    createModuleMode = "retained";
    await page.goto(url);
    await page.getByRole("button", { name: "Install BlueMap", exact: true }).click();
    await page.getByRole("checkbox", { name: /I explicitly accept/ }).check();
    await page.getByRole("button", { name: "Accept and install" }).click();
    await page.getByText("BlueMap configuration found", { exact: true }).waitFor();
    await page.getByRole("button", { name: "Use existing configuration" }).click();
    await page.getByText("accept-download: true", { exact: true }).waitFor();
    assert.equal(await page.getByRole("link", { name: "Open server files" }).getAttribute("href"), "/servers/srv-1?tab=files");
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
    await page.screenshot({ path: path.join(evidence, "setup-recovery-360.png"), animations: "disabled" });
    createModuleMode = "reuse-ready";
    await page.getByRole("button", { name: "Check and start" }).click();
    await page.getByRole("region", { name: /World map/i }).waitFor();
    assert.deepEqual(recoveryRequests, [
      { reuse: false, consent: "true" },
      { reuse: true, consent: "true" },
      { reuse: true, consent: "true" },
    ]);
  });
  results.push("retained BlueMap setup shows file guidance, reuses only after consent, and opens the verified renderer");

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
