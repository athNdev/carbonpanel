// SPDX-License-Identifier: MPL-2.0
// Builds the isolated component fixture and drives its real browser controls.
import assert from "node:assert/strict";
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

  const saved = "Saved terrain available.";
  const partial = "Only part of this area has saved terrain.";
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

  const context = await browser.newContext({
    viewport: { width: 360, height: 900 },
  });
  try {
    const page = await context.newPage();
    await configure(page, "dark");
    const statuses = [
      ["empty", "No saved terrain here. Move or try the world spawn."],
      ["busy", "Map rendering is busy. Retry in a moment."],
      [
        "changed",
        "The world changed while terrain was being read. Retry after the save finishes.",
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
