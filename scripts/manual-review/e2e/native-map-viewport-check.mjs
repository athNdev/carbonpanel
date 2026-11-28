// SPDX-License-Identifier: MPL-2.0
// Builds the isolated component fixture and drives its real browser controls.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";
import { setTimeout } from "node:timers/promises";
import { crc32, deflateSync } from "node:zlib";
import { launchReviewBrowser } from "../../review/browser.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const web = path.resolve(here, "../../../web/carbon-panel");
const configFile = path.join(web, "test/a11y-fixture/vite.config.mjs");
const evidence = path.resolve(
  process.env.NATIVE_MAP_EVIDENCE ??
    path.resolve(here, "../../../tmp/review-evidence/native-map"),
);
const require = createRequire(path.join(web, "package.json"));
const { build, preview } = await import(
  pathToFileURL(require.resolve("vite")).href
);
const outDir = path.join(evidence, "fixture-build");
const results = [];
await fs.mkdir(evidence, { recursive: true });

function pngChunk(type, payload) {
  const typeAndData = Buffer.concat([Buffer.from(type, "ascii"), payload]);
  const bytes = Buffer.alloc(payload.length + 12);
  bytes.writeUInt32BE(payload.length);
  typeAndData.copy(bytes, 4);
  bytes.writeUInt32BE(crc32(typeAndData), bytes.length - 4);
  return bytes;
}

function fixtureTile(tileX, tileZ) {
  const size = 128;
  const rowLength = size * 3 + 1;
  const rows = Buffer.alloc(rowLength * size);
  for (let z = 0; z < size; z++) {
    for (let x = 0; x < size; x++) {
      const blockX = tileX * size + x;
      const blockZ = tileZ * size + z;
      const noise = Math.floor((Math.sin(blockX * 73 + blockZ * 21) + 1) * 16);
      const river = Math.abs(blockZ - Math.sin(blockX / 39) * 23 + 40);
      const offset = z * rowLength + x * 3 + 1;
      const color =
        river < 13 ? [26, 74, 122] : river < 21 ? [130, 112, 62] : [37, 84, 33];
      for (let channel = 0; channel < 3; channel++)
        rows[offset + channel] = color[channel] + noise;
    }
  }
  const header = Buffer.alloc(13);
  header.writeUInt32BE(size, 0);
  header.writeUInt32BE(size, 4);
  header[8] = 8;
  header[9] = 2;
  return Buffer.concat([
    Buffer.from("89504e470d0a1a0a", "hex"),
    pngChunk("IHDR", header),
    pngChunk("IDAT", deflateSync(rows)),
    pngChunk("IEND", Buffer.alloc(0)),
  ]);
}

async function runPackagedWorldView(browser) {
  const child = spawn(process.execPath, [path.join(web, "test/a11y-routes/serve.mjs")], {
    stdio: ["ignore", "pipe", "inherit"],
  });
  let context;
  try {
    const origin = await new Promise((resolve, reject) => {
      let output = "";
      const timer = globalThis.setTimeout(() => reject(new Error("packaged app did not start")), 20000);
      child.once("exit", (code) => {
        clearTimeout(timer);
        reject(new Error("packaged app server exited: " + code));
      });
      child.once("error", (error) => {
        clearTimeout(timer);
        reject(error);
      });
      child.stdout.on("data", (chunk) => {
        output += chunk;
        const address = output.match(/A11Y_ROUTES_URL=(http:\/\/127\.0\.0\.1:\d+\/)/)?.[1];
        if (!address) return;
        clearTimeout(timer);
        resolve(address);
      });
    });
    const worldRef = { kind: "KIND_SERVER_LOCAL", serverId: "native-server", levelName: "overworld" };
    const native = {
      serverId: worldRef.serverId,
      name: "Fixture native world",
      worldRef,
      rendererType: "RENDERER_TYPE_NATIVE",
      rendererState: "RENDERER_STATE_NATIVE_AVAILABLE",
      renderer: { type: "RENDERER_TYPE_NATIVE", capabilities: ["map.2d"] },
    };
    const session = "/api/v1/worldview/map/native-route-fixture/";
    const embedSession = "/api/v1/worldview/map/bluemap-route-fixture/";
    const blueMap = {
      ...native,
      rendererType: "RENDERER_TYPE_BLUEMAP",
      rendererState: "RENDERER_STATE_READY",
      renderer: { type: "RENDERER_TYPE_BLUEMAP", capabilities: ["map.2d"] },
    };
    let selectedRenderer = native;
    let libraryReader = false;
    let serverListFailure;
    const runningServer = {id: native.serverId, name: native.name, status: "SERVER_STATUS_RUNNING", modLoader: "MOD_LOADER_VANILLA"};
    let tileRequests = 0;
    let savingTile;
    let savingTileRetried = false;
    const markerReads = [];
    const overworldMarker = { serverId: native.serverId, worldRef, dimension: "overworld", x: 18.5, z: -29.25 };
    const overlays = [
      { ...overworldMarker, id: "waypoint", kind: "waypoint", label: "Village gate" },
      { ...overworldMarker, id: "private", kind: "marker", label: "Private position", sensitive: true },
      { ...overworldMarker, id: "player", kind: "player", label: "Player position" },
      { ...overworldMarker, id: "region", kind: "region", label: "Region boundary" },
      { ...overworldMarker, id: "nether", kind: "poi", dimension: "nether", label: "Nether location" },
    ];
    context = await browser.newContext({
      viewport: { width: 1440, height: 1000 }, reducedMotion: "reduce",
    });
    const page = await context.newPage();
    await page.addInitScript(() => {
      Object.defineProperty(navigator, "clipboard", {
        value: { writeText: async (value) => { window.__copiedMapLink = value; } },
      });
    });
    await page.route(/\/carbonpanel\.v1\.[^/]+\/[^/?]+/, async (route) => {
      const method = new URL(route.request().url()).pathname.split("/").at(-1);
      if (method === "ListServers" && serverListFailure) {
        await route.fulfill({status: serverListFailure === "permission_denied" ? 403 : 503, json:{code: serverListFailure, message: "fixture inventory error"}});
        return;
      }
      if (method === "ListMapOverlays") {
        const request = route.request().postDataJSON();
        markerReads.push(request);
        await route.fulfill({ json: { overlays: request.dimension === "overworld" ? overlays : [overlays.at(-1)] } });
        return;
      }
      const responses = {
        GetAuthStatus: {localAuthEnabled: false, oidcEnabled: false, anonymousAccessEnabled: false},
        GetCurrentUser: { user: {id: "fixture", username: "Fixture"}, permissions: [
          { resource: "world_view", action: "read", objectId: native.serverId },
          ...(libraryReader ? [
            {resource: "servers", action: "read", objectId: "*"},
            {resource: "worlds", action: "read", objectId: "*"},
          ] : []),
        ]},
        GetWorldViewStatus: { target: selectedRenderer },
        GetWorldViewSession: {
          state: selectedRenderer.rendererState,
          proxyPath: selectedRenderer === native ? session : embedSession,
        },
        ListWorldViewTargets: { targets: [native] },
        ListCapabilities: {rendererType: native.rendererType, capabilities: [{name: "map.2d", supported: true}]},
        ListServers: {servers: [runningServer, {...runningServer, id:"stopped", name:"Stopped fixture", status:"SERVER_STATUS_STOPPED"}]},
        ListBindings: {configuredLevelName:"overworld", bindings:[]},
        GetServer: {server: runningServer},
      };
      await route.fulfill({ json: responses[method] ?? {} });
    });
    await page.route("**" + embedSession + "**", (route) =>
      route.fulfill({ contentType: "text/html", body: "<title>BlueMap fixture</title>" })
    );
    await page.route("**" + session + "**", async (route) => {
      if (route.request().url().endsWith("/native/manifest.json")) {
        await route.fulfill({json: {tileBlocks: 128, dimension: "overworld", spawn: {x: 12, z: -34}, dimensions: ["overworld", "nether"]}});
        return;
      }
      const match = route.request().url().match(/\/native\/([a-z0-9_-]+)\/(-?\d+)\/(-?\d+)\.png$/);
      assert.ok(match, "packaged map requests one scoped tile");
      tileRequests += 1;
      if (!savingTile) {
        savingTile = match[0];
        await route.fulfill({ status: 503, headers: { "Retry-After": "1" } });
        return;
      }
      if (match[0] === savingTile) savingTileRetried = true;
      await route.fulfill({
        body: fixtureTile(Number(match[1]), Number(match[2])),
        contentType: "image/png",
        headers: { "X-CarbonPanel-Rendered-Chunks": "64", "X-CarbonPanel-Tile-Saved-At": "2026-11-12T12:32:00Z" },
      });
    });
    const key = "w1." + Buffer.from(JSON.stringify([2, native.serverId, worldRef.levelName, "", ""])).toString("base64url");
    const completedSave = page.waitForResponse((response) =>
      savingTile && response.url().endsWith(savingTile) && response.status() === 200
    );
    // Both the /worlds inventory and the server page's map dialog exist at
    // this head. This packaged pass enters through the dialog's ?map=<world
    // key> deep link; the /worlds row and card open paths are covered below.
    // The world-only grant still renders the map while the World tab behind
    // it stays denied for worlds/read.
    await page.goto(origin + "servers/" + native.serverId + "?tab=world&map=" + key + "&dimension=nether");
    await completedSave;
    assert.ok(savingTileRetried, "a tile interrupted by a save retries under the same map session");
    await page.getByRole("heading", { name: "World terrain" }).waitFor();
    const surface = page.getByRole("region", { name: "Map surface" });
    const tile = surface.locator("img").first();
    await tile.waitFor({ state: "visible" });
    await tile.evaluate((img) => img.decode());
    assert.equal(await tile.evaluate((img) => img.naturalWidth), 128);
    assert.equal(await surface.locator("iframe").count(), 0);
    assert.ok(tileRequests > 0);
    const gate = surface.getByRole("button", { name: "Waypoint: Village gate", exact: true });
    await gate.click();
    await surface.getByRole("heading", { name: "Village gate", exact: true }).waitFor();
    await surface.getByRole("status").getByText("X: 18.5 · Z: -29.25", { exact: true }).waitFor();
    assert.ok(markerReads.some((request) =>
      request.serverId === native.serverId && request.worldRef?.levelName === worldRef.levelName && request.dimension === "overworld"
    ));
    assert.equal(await page.getByRole("button", { name: "Map markers (1)", exact: true }).count(), 1);
    assert.equal(await page.getByRole("button", { name: "Show player markers", exact: true }).count(), 0);
    assert.doesNotMatch(await page.locator("body").innerText(), /Private position|Player position|Region boundary|Nether location/);
    await page.screenshot({path: path.join(evidence, "packaged-world-view-native.png")});
    // The "Copy map link" action is no longer rendered at this head (removed
    // with the map dialog refactor and recorded in src/lib/changelog.md), so
    // main's copy assertion cannot be restored. The dialog's ?map=<world key>
    // URL still carries the x/z camera query, so the deep link remains the
    // way to restore a camera position.
    await page.goto(origin + "servers/" + native.serverId + "?map=" + key + "&x=-128&z=92");
    await page.getByTestId("native-map-coordinates").getByText("X: -128 · Z: 92").waitFor();
    await page.locator('[role=application] img').first().waitFor();
    selectedRenderer = blueMap;
    await surface.locator('iframe[src="' + embedSession + '"]').waitFor();
    assert.equal(await surface.locator('iframe[src="' + session + '"]').count(), 0);
    selectedRenderer = native;
    await page.getByTestId("native-map-coordinates").getByText("X: -128 · Z: 92").waitFor();
    await page.locator('[role=application] img').first().waitFor();
    assert.equal(await surface.locator("iframe").count(), 0);
    results.push("status polling reissues sessions when the rendering mode changes in either direction");
    results.push("the packaged World View dialog renders native tiles and scoped Overworld markers without an iframe or module");
    // The native viewport's own dimension switch serves the requested
    // dimension's tiles under the same map session and keeps the camera;
    // panel-owned markers stay Overworld-scoped.
    const netherTile = page.waitForResponse(
      (response) => response.url().includes("/native/nether/") && response.ok()
    );
    await page.getByRole("combobox", { name: "Dimension", exact: true }).selectOption("nether");
    await netherTile;
    assert.equal(
      await page.getByTestId("native-map-coordinates").innerText(),
      "X: -128 · Z: 92",
      "the dimension switch preserves the camera",
    );
    await surface.getByText("Markers are only placed on the Overworld map.").waitFor();
    assert.equal(
      await surface.getByRole("button", { name: "Waypoint: Village gate", exact: true }).count(),
      0,
    );
    results.push("the native dimension switch serves nether tiles in the same session, keeps the camera, and keeps markers Overworld-scoped");
    // /worlds still exists at this head; keep main's row/card open-path
    // checks alongside the dialog and dimension-switch checks above.
    for (const width of [360, 1440]) {
      await page.setViewportSize({width, height: 900});
      await page.goto(origin + "worlds?server=" + native.serverId);
      const link = page.getByRole("link", {name:/Open map for Fixture native world/});
      const row = link.locator("xpath=ancestor::*[self::tr or self::li][1]");
      const bounds = await row.boundingBox();
      assert.ok(bounds);
      await page.mouse.click(bounds.x + bounds.width - 8, bounds.y + bounds.height / 2);
      await page.waitForURL(url => url.pathname.startsWith("/worlds/" + native.serverId + "/"));
      await page.goBack();
      await link.focus();
      await link.press("Enter");
      await page.waitForURL(url => url.pathname.startsWith("/worlds/" + native.serverId + "/"));
    }
    results.push("both desktop rows and mobile cards open through their far edge and keyboard link");
    libraryReader = true;
    await page.goto(origin + "library");
    const running = page.getByRole("region", {name:"Running server worlds"});
    await running.getByText("overworld", {exact:true}).waitFor();
    await running.getByText("Server-local", {exact:true}).waitFor();
    await running.getByRole("link", {name:native.name, exact:true}).waitFor();
    assert.doesNotMatch(await running.innerText(), /Stopped fixture/);
    await page.getByRole("heading", {name:"No saved library revisions yet"}).waitFor();
    assert.equal(await page.getByRole("link", {name:"Import world"}).count(), 0);
    await page.getByRole("combobox", {name:"Edition"}).selectOption("bedrock");
    await running.getByText("No running worlds match these filters.").waitFor();
    await page.getByRole("button", {name:"Reset filters"}).click();
    await running.getByText("overworld", {exact:true}).waitFor();
    await page.screenshot({path: path.join(evidence, "packaged-library-running-world.png")});
    results.push("running worlds appear alongside an empty revision library, honor filters and hide creation from readers");
    const refreshServers = running.getByRole("button", {name:"Refresh"});
    serverListFailure = "unavailable";
    await refreshServers.click();
    await running.getByText("Could not refresh the running server list.").waitFor();
    await running.getByText("overworld", {exact:true}).waitFor();
    serverListFailure = "permission_denied";
    await refreshServers.click();
    await running.getByText("Running server inventory access was denied.").waitFor();
    assert.equal(await running.getByRole("link", {name:native.name}).count(), 0);
    serverListFailure = undefined;
    await refreshServers.click();
    await running.getByText("overworld", {exact:true}).waitFor();
    results.push("the library keeps cached worlds on transient failure, hides denied inventory, and recovers after reauthorization");
  } finally {
    await context?.close();
    child.kill();
  }
}

const storage = new Map();
let server;
let browser;
try {
  await build({
    configFile,
    build: { outDir, emptyOutDir: false, reportCompressedSize: false },
  });
  server = await preview({
    configFile,
    build: { outDir },
    preview: { host: "127.0.0.1", port: 0, strictPort: true },
  });
  const base =
    "http://127.0.0.1:" + server.httpServer.address().port + "/?native-map";
  ({ browser } = await launchReviewBrowser());
  let scenario = "partial";
  let manifestStatus = 200;
  let outstanding = 0;
  let maxOutstanding = 0;
  let lateTile;
  let heldTiles;
  let releaseTile;
  const errors = [];

  function savedTile(tileX, tileZ) {
    const key = tileX + ":" + tileZ;
    if (!storage.has(key)) storage.set(key, fixtureTile(tileX, tileZ));
    return storage.get(key);
  }

  async function configure(page, theme) {
    page.on("pageerror", (error) => errors.push(error.message));
    await page.addInitScript(() => {
      Object.defineProperty(navigator, "clipboard", {
        value: {
          async writeText(value) {
            window.__copiedCoords = value;
          },
        },
      });
    });
    await page.route(base, async (route) => {
      const response = await route.fetch();
      const html = (await response.text()).replace(
        'data-carbon-theme="dark"',
        'data-carbon-theme="' + theme + '"',
      );
      await route.fulfill({
        body: html,
        contentType: "text/html",
        headers: { "Content-Security-Policy": "img-src 'self' data: https:" },
      });
    });
    await page.route("**/api/v1/worldview/map/**", async (route) => {
      const pathname = new URL(route.request().url()).pathname;
      const second = pathname.includes("/fixture-two/");
      if (pathname.endsWith("/manifest.json")) {
        await route.fulfill({
          status: manifestStatus,
          json: {
            tileBlocks: 128,
            dimension: "overworld",
            spawn: second ? { x: 2000, z: -3000 } : { x: 12, z: -34 },
          },
        });
        return;
      }
      const tile = pathname.match(
        /\/native\/overworld\/(-?\d+)\/(-?\d+)\.png$/,
      );
      assert.ok(tile, "request must name one scoped Overworld tile");
      const [x, z] = [Number(tile[1]), Number(tile[2])];

      outstanding += 1;
      maxOutstanding = Math.max(maxOutstanding, outstanding);
      try {
        await setTimeout(25);
        if (scenario === "hold-scope" && !second) {
          lateTile?.();
          await heldTiles;
        }
        const status = {
          busy: 429,
          changed: 409,
          unsupported: 422,
          unavailable: 503,
          access: 403,
        }[scenario];
        if (
          status ||
          scenario === "empty" ||
          second ||
          (scenario === "partial" && (x + z) % 2 === 0)
        ) {
          await route.fulfill({ status: status ?? 404 });
          return;
        }
        await route.fulfill({
          contentType: "image/png",
          headers: {
            "X-CarbonPanel-Rendered-Chunks":
              scenario === "partial" ? "31" : "64",
            "X-CarbonPanel-Provisional-Chunks": scenario === "partial" ? "12" : "0",
            "X-CarbonPanel-Tile-Saved-At": "2026-11-12T12:32:00Z",
          },
          body: savedTile(x, z),
        });
      } catch (cause) {
        if (!second && scenario === "hold-scope") return;
        throw cause;
      } finally {
        outstanding -= 1;
      }
    });
  }

  const saved = "All visible chunks have terrain.";
  const partial = "Blank areas have no readable terrain yet. The map checks for new saves automatically.";
  const empty = "Explore this area in Minecraft. The map checks for new saves automatically.";
  const fromMap = (page) =>
    page.getByRole("application", { name: "Survival map" });
  const coordinates = (page) => page.getByTestId("native-map-coordinates");
  const text = (page, value) => page.getByText(value, { exact: true });
  for (const width of [360, 768, 1440]) {
    for (const theme of ["light", "dark"]) {
      const context = await browser.newContext({
        viewport: { width, height: 900 },
        reducedMotion: "reduce",
      });
      try {
        const page = await context.newPage();
        await configure(page, theme);
        scenario = "partial";
        maxOutstanding = 0;
        await page.goto(base);
        await text(page, partial).waitFor();
        assert.equal(
          await page.locator("html").getAttribute("data-carbon-theme"),
          theme,
        );
        assert.equal(
          await page
            .locator("html")
            .evaluate((el) => el.scrollWidth > el.clientWidth),
          false,
        );
        assert.equal(await coordinates(page).innerText(), "X: 12 · Z: -34");
        assert.ok(
          maxOutstanding > 0 && maxOutstanding <= 2,
          "at most two tile fetches",
        );
        const map = fromMap(page);
        const saveTime = page.locator("p").filter({hasText: "Latest chunk save in this view"}).locator("time");
        assert.equal(await saveTime.getAttribute("datetime"), "2026-11-12T12:32:00Z");
        await text(page, "Faded terrain shows saved heights from chunks still generating.").waitFor();
        const span = page.getByRole("combobox", { name: "Map span" });
        await span.selectOption("1024");
        await text(page, partial).waitFor();
        assert.ok(await map.locator(".tile").count() > 8);
        assert.ok(await map.locator(".tile").count() <= 81);
        await span.selectOption("auto");
        assert.equal(
          await map.evaluate((element) => getComputedStyle(element).position),
          "relative",
        );
        assert.equal(
          await map.locator(".tile").first().evaluate((element) => getComputedStyle(element).position),
          "absolute",
          "compiled design-system utilities must position terrain tiles",
        );
        await map.locator("img").first().waitFor();
        await page.waitForFunction(() =>
          [...document.querySelectorAll("[role=application] img")].some(
            (image) => image.naturalWidth === 128,
          ),
        );
        await page.screenshot({
          path: path.join(evidence, "terrain-" + width + "-" + theme + ".png"),
          animations: "disabled",
        });

        await map.focus();
        await map.press("ArrowRight");
        assert.notEqual(await coordinates(page).innerText(), "X: 12 · Z: -34");
        const box = await map.boundingBox();
        assert.ok(box);
        const beforeDrag = await coordinates(page).innerText();
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        await page.mouse.down();
        await page.mouse.move(
          box.x + box.width / 2 + 55,
          box.y + box.height / 2 + 34,
          { steps: 4 },
        );
        await page.mouse.up();
        assert.notEqual(await coordinates(page).innerText(), beforeDrag);

        await map.press("Equal");
        assert.equal(await span.inputValue(), "128");
        await span.selectOption("64");
        await page
          .getByRole("button", { name: "Zoom in", exact: true })
          .click();
        assert.equal(
          await page
            .getByRole("button", { name: "Zoom in", exact: true })
            .isDisabled(),
          true,
        );
        await page
          .getByRole("button", { name: "Zoom out", exact: true })
          .click();
        assert.equal(
          await page
            .getByRole("button", { name: "Zoom in", exact: true })
            .isEnabled(),
          true,
        );
        await page
          .getByRole("textbox", { name: "X coordinate", exact: true })
          .fill("1e3");
        await page
          .getByRole("textbox", { name: "Z coordinate", exact: true })
          .fill("-52");
        await page.getByRole("button", { name: "Jump", exact: true }).click();
        assert.equal(
          await page
            .getByRole("textbox", { name: "X coordinate", exact: true })
            .getAttribute("aria-invalid"),
          "true",
        );
        await page
          .getByRole("textbox", { name: "X coordinate", exact: true })
          .fill("-142");
        await page.getByRole("button", { name: "Jump", exact: true }).click();
        assert.equal(await coordinates(page).innerText(), "X: -142 · Z: -52");
        await page
          .getByRole("button", { name: "Copy coordinates", exact: true })
          .click();
        await text(page, "Coordinates copied.").waitFor();
        assert.equal(
          await page.evaluate(() => window.__copiedCoords),
          "-142, -52",
        );
        await page
          .getByRole("button", { name: "World spawn", exact: true })
          .click();
        assert.equal(await coordinates(page).innerText(), "X: 12 · Z: -34");
        results.push(
          "viewport " +
            width +
            " " +
            theme +
            " fits and supports keyboard, pointer, zoom, exact jump, copy and spawn",
        );
      } finally {
        await context.close();
      }
    }
  }

  const markerContext = await browser.newContext({
    viewport: { width: 360, height: 900 },
    hasTouch: true,
  });
  try {
    const page = await markerContext.newPage();
    await configure(page, "dark");
    scenario = "partial";
    await page.goto(base);
    await text(page, partial).waitFor();

    const map = fromMap(page);
    const camp = map.getByRole("button", {
      name: "Marker: River camp",
      exact: true,
    });
    await camp.waitFor();
    await map
      .getByRole("button", {
        name: "Point of interest: Old aqueduct",
        exact: true,
      })
      .waitFor();
    const bounds = await map.boundingBox();
    const pin = await camp.boundingBox();
    assert.ok(bounds && pin);
    const scale = Math.max(bounds.width, bounds.height) / 256;
    assert.ok(
      Math.abs(
        pin.x + pin.width / 2 - (bounds.x + bounds.width / 2 + 24.5 * scale),
      ) < 1,
    );
    assert.ok(
      Math.abs(
        pin.y + pin.height / 2 - (bounds.y + bounds.height / 2 + 19.75 * scale),
      ) < 1,
    );

    await camp.tap();
    await page
      .getByRole("heading", { name: "River camp", exact: true })
      .waitFor();
    await page
      .getByRole("status")
      .getByText("X: 36.5 · Z: -14.25", { exact: true })
      .waitFor();
    await page.screenshot({
      path: path.join(evidence, "markers-360-dark.png"),
      animations: "disabled",
    });
    await page.getByRole("button", { name: "Close marker details" }).click();
    assert.equal(
      await camp.evaluate((element) => element === document.activeElement),
      true,
    );

    await camp.press("Enter");
    await page
      .getByRole("heading", { name: "River camp", exact: true })
      .waitFor();
    await page
      .getByRole("button", { name: "Map markers (3)", exact: true })
      .click();
    assert.doesNotMatch(
      await page.locator("main").innerText(),
      /Private location|Player location|Unsupported region|Nether ruin/,
    );
    const visibility = page.getByRole("checkbox", {
      name: "Show markers on map",
    });
    await visibility.uncheck();
    assert.equal(await map.getByRole("button").count(), 0);

    await page
      .getByRole("button", { name: "Center map on Waypoint: North trail" })
      .click();
    await map
      .getByRole("button", { name: "Waypoint: North trail", exact: true })
      .waitFor();
    assert.equal(await coordinates(page).innerText(), "X: -300 · Z: 270");
    assert.equal(
      await map.evaluate((element) => element === document.activeElement),
      true,
    );
    assert.equal(await visibility.isChecked(), true);
    results.push(
      "native markers project accurately, retain kind and precision, support keyboard and touch, and surface offscreen points",
    );
  } finally {
    await markerContext.close();
  }

  const context = await browser.newContext({
    viewport: { width: 360, height: 900 },
  });
  try {
    const travelPage = await context.newPage();
    await configure(travelPage, "dark");
    await travelPage.clock.install();
    scenario = "empty";
    await travelPage.goto(base);
    await text(travelPage, empty).waitFor();
    const distantTile = travelPage.waitForRequest((request) =>
      request.url().endsWith("/native/overworld/193/-146.png"),
    );
    await travelPage
      .getByRole("textbox", { name: "X coordinate", exact: true })
      .fill("24800");
    await travelPage
      .getByRole("textbox", { name: "Z coordinate", exact: true })
      .fill("-18600");
    await travelPage.getByRole("button", { name: "Jump", exact: true }).click();
    await distantTile;
    await text(travelPage, empty).waitFor();
    assert.equal(await fromMap(travelPage).locator("img").count(), 0);
    await travelPage.getByRole("link", { name: /pregenerate this area from the server console/ }).waitFor();
    await travelPage.screenshot({ path: path.join(evidence, "distant-blank-360.png"), animations: "disabled" });
    scenario = "saved";
    await travelPage.clock.fastForward(31_000);
    await fromMap(travelPage).locator("img").first().waitFor({ timeout: 5_000 });
    assert.equal(await coordinates(travelPage).innerText(), "X: 24800 · Z: -18600");
    results.push("a distant empty viewport rechecks saved terrain without resetting the camera");
    await travelPage.close();

    const page = await context.newPage();
    await configure(page, "dark");
    const statuses = [
      ["empty", empty],
      ["busy", "Map rendering is busy. Retry in a moment."],
      [
        "changed",
        "The active world changed. Reopen the map to view the current world.",
      ],
      ["unsupported", "Carbon Panel cannot read saved terrain for this world."],
      [
        "unavailable",
        "Carbon Panel could not read the map. Retry in a moment.",
      ],
      [
        "access",
        "The map session is no longer authorized. Refresh the World View page.",
      ],
    ];
    for (const [mode, expected] of statuses) {
      scenario = mode;
      await page.goto(base);
      await text(page, expected).waitFor();
      assert.equal(await fromMap(page).locator("img").count(), 0);
      if (mode === "busy" || mode === "changed" || mode === "unavailable") {
        scenario = "saved";
        await page
          .getByRole("button", { name: "Retry terrain", exact: true })
          .click();
        await text(page, saved).waitFor();
      }
      results.push("tile " + mode + " state");
    }
    manifestStatus = 503;
    await page.reload();
    await page.getByRole("heading", { name: "Map unavailable" }).waitFor();
    manifestStatus = 200;
    scenario = "saved";
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await text(page, saved).waitFor();
    results.push("manifest failure retries cleanly");

    scenario = "hold-scope";
    const started = new Promise((resolve) => {
      lateTile = resolve;
    });
    heldTiles = new Promise((resolve) => {
      releaseTile = resolve;
    });
    await page.reload();
    await started;
    await page
      .locator("#switch-native-session")
      .evaluate((button) => button.click());
    await text(page, statuses[0][1]).waitFor();
    assert.equal(await coordinates(page).innerText(), "X: 2000 · Z: -3000");
    releaseTile?.();
    await setTimeout(50);
    assert.equal(await fromMap(page).locator("img").count(), 0);
    results.push(
      "switching session discards a late response from the old world",
    );
  } finally {
    releaseTile?.();
    await context.close();
  }

  assert.deepEqual(errors, []);
  await runPackagedWorldView(browser);
  await fs.writeFile(
    path.join(evidence, "report.json"),
    JSON.stringify({ results }, null, 2),
  );
  console.log(
    "PASS: " + results.length + " native map browser checks in " + evidence,
  );
} finally {
  await browser?.close();
  await new Promise((resolve) =>
    server ? server.httpServer.close(resolve) : resolve(),
  );
}
