// SPDX-License-Identifier: MPL-2.0
/**
 * Real-browser QA for the server-detail WORLD tab. Mounts the actual SvelteKit
 * route with local Connect fixtures; no live server, world or daemon is used.
 * Covers honest management states, deep links, denied scope, compatibility
 * tuple fencing (delayed A resolving after a switch to B), explicit
 * confirmation and honest queued/skipped/failed/unknown binding outcomes.
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

// Default stays inside the checkout; OSS382_EVIDENCE can point at an
// out-of-tree evidence workspace without baking a workstation path into source.
const evidence = path.resolve(
  process.env.OSS382_EVIDENCE || path.resolve(here, "../../../tmp/review-evidence/oss-382"),
);
await fs.mkdir(evidence, { recursive: true });

const results = [];
const requests = [];
const compatCalls = [];
let holdCompat = null;
let permissions = [{ resource: "*", action: "*", objectId: "*" }];
let binding = {
  id: "bind-1",
  serverId: "srv-1",
  levelName: "world",
  revisionId: "rev-1",
  nodeId: "node-a",
  generation: 7,
  consistency: "verified stopped",
};
let targets = [
  {
    serverId: "srv-1",
    worldRef: {
      kind: "KIND_LIBRARY_BINDING",
      serverId: "srv-1",
      levelName: "world",
      bindingId: "bind-1",
    },
    name: "Bound world",
    rendererState: "RENDERER_STATE_ABSENT",
  },
];
let configCategories = [
  {
    name: "World",
    properties: [
      { key: "level", label: "World Name", value: "world" },
      { key: "levelType", label: "Level Type", value: "minecraft:flat" },
      { key: "seed", label: "Seed", value: "" },
    ],
  },
];
let compatMode = "compatible";
let bindOutcome = "success";

const server = {
  id: "srv-1",
  name: "Fixture Server",
  description: "Browser fixture",
  status: "SERVER_STATUS_STOPPED",
  modLoader: "MOD_LOADER_VANILLA",
  mcVersion: "1.21.1",
  port: 25565,
  memory: 1024,
};
const server2 = {
  ...server,
  id: "srv-2",
  name: "Second Fixture Server",
};
const entryA = {
  id: "wld-a",
  name: "World A",
  edition: "java",
  family: "prebuilt",
  currentRevisionId: "rev-a",
  revisionCount: 1,
};
const entryB = {
  id: "wld-b",
  name: "World B",
  edition: "java",
  family: "prebuilt",
  currentRevisionId: "rev-b",
  revisionCount: 1,
};
const revision = (id) => ({
  id,
  worldEntryId: id === "rev-a" ? "wld-a" : "wld-b",
  mcVersion: "1.21.1",
  modLoader: "MOD_LOADER_VANILLA",
  sizeBytes: "2048",
  entryCount: 12,
  provenance: "fixture",
});
const reportFor = (revisionId) => {
  if (compatMode === "unknown-finding") {
    return {
      revisionId,
      serverId: "srv-1",
      targetMcVersion: "1.21.1",
      overall: "compatible",
      findings: [
        { subject: "data_version", confidence: "unknown", value: "", reason: "not verifiable" },
      ],
      transforms: [],
      unsupported: {},
    };
  }
  return {
    revisionId,
    serverId: "srv-1",
    targetMcVersion: "1.21.1",
    overall: compatMode,
    findings: [{ subject: "edition", confidence: "detected", value: "java", reason: "" }],
    transforms: [],
    unsupported: {},
  };
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
    requests.push({ method, payload });
    const reply = (body) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(body),
      });
    if (method === "GetAuthStatus")
      return reply({ localAuthEnabled: false, oidcEnabled: false, anonymousAccessEnabled: false });
    if (method === "GetCurrentUser")
      return reply({ user: { id: "fixture", username: "Fixture" }, permissions });
    if (method === "ListServers") return reply({ servers: [server, server2] });
    if (method === "GetServer")
      return reply({ server: payload.id === "srv-2" ? server2 : server });
    if (method === "GetProxyStatus") return reply({ enabled: false, baseUrl: "" });
    if (method === "GetServerConfig") return reply({ categories: configCategories });
    if (method === "ListWorldViewTargets") return reply({ targets });
    if (method === "GetWorldViewStatus")
      return reply({ target: targets.find((target) => target.serverId === payload.serverId) });
    if (method === "ListBindings") return reply({ bindings: binding ? [binding] : [] });
    if (method === "ListWorldEntries") return reply({ entries: [entryA, entryB] });
    if (method === "GetRevision") return reply({ revision: revision(payload.revisionId) });
    if (method === "ListRevisions") {
      const entryId = payload.worldEntryId;
      return reply({
        revisions: entryId === "wld-a" ? [revision("rev-a")] : [revision("rev-b")],
      });
    }
    if (method === "CheckCompatibility") {
      const call = { revisionId: payload.revisionId, serverId: payload.serverId };
      compatCalls.push(call);
      if (holdCompat) await new Promise((resolve) => holdCompat.push(resolve));
      return reply({ report: reportFor(payload.revisionId) });
    }
    if (method === "BindWorld")
      return reply({
        binding: {
          id: "bind-new",
          serverId: payload.serverId,
          levelName: payload.levelName,
          revisionId: payload.revisionId,
          nodeId: "node-a",
          generation: 8,
          consistency: "verified stopped",
        },
        operationId: "op-1",
        outcome: bindOutcome,
        consistency: "verified stopped",
      });
    if (method === "ResolveWorldOperation") return reply({ outcome: "resolved" });
    return reply({});
  });
  await page.route("**/api/v1/**", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: "{}" }),
  );

  const shot = async (name) =>
    page.screenshot({ path: path.join(evidence, `${name}.png`), fullPage: true });

  // Warm route dependencies first; Vite's initial optimization reloads the page.
  await page.goto(`${base}/servers/srv-1`);
  await page.waitForLoadState("networkidle");
  await page.goto(base);
  await page.waitForLoadState("networkidle");

  async function gotoWorldTab() {
    await page.goto(`${base}/servers/srv-1`);
    await page.getByTestId("tab-world").waitFor({ timeout: 60000 });
    await page.getByTestId("tab-world").click();
    await page.waitForTimeout(400);
  }
  async function openPicker() {
    await page
      .getByRole("button", { name: /library world/i })
      .last()
      .click();
    await page.getByLabel("Library world", { exact: true }).waitFor();
  }
  async function selectEntry(id) {
    await page.getByLabel("Library world", { exact: true }).selectOption(id);
    await page.waitForTimeout(250);
  }
  async function checkCompat() {
    const before = compatCalls.length;
    await page.getByRole("button", { name: "Check compatibility" }).click();
    const waitUntil = Date.now() + 10000;
    while (compatCalls.length <= before && Date.now() < waitUntil)
      await new Promise((resolve) => setTimeout(resolve, 10));
    await page.waitForTimeout(300);
  }
  async function fullSwap() {
    await openPicker();
    await selectEntry("wld-b");
    await checkCompat();
    await page.getByRole("button", { name: "Review & swap" }).click();
    await page.getByRole("button", { name: "Confirm swap" }).click();
    await page.waitForTimeout(300);
  }
  const worldTab = page.getByRole("tabpanel");
  const openMap = worldTab.getByRole("button", { name: "Open map", exact: true });
  async function checkMapDialog(expectedKey) {
    await openMap.click();
    const dialog = page.getByRole("dialog", { name: "World map", exact: true });
    await dialog.waitFor();
    const url = new URL(page.url());
    assert.equal(url.pathname, "/servers/srv-1");
    assert.match(url.searchParams.get("map") ?? "", expectedKey);
    await page.keyboard.press("Escape");
    await dialog.waitFor({ state: "hidden" });
  }

  // 1. Bound desktop state.
  await gotoWorldTab();
  await page.getByText("LIBRARY BOUND", { exact: true }).first().waitFor();
  assert.ok(await worldTab.getByText("Active level", { exact: true }).count());
  assert.equal(await worldTab.locator("dd").filter({ hasText: /^world$/ }).count(), 1);
  assert.ok(await page.getByText("Binding & revision", { exact: true }).count());
  assert.ok(await page.getByText("VERIFIED STOPPED", { exact: true }).count());
  assert.ok(await page.getByText("minecraft:flat", { exact: true }).count());
  await checkMapDialog(/^w1\./);
  const libHref = await page.getByRole("link", { name: /Open World Library/ }).getAttribute("href");
  assert.ok(libHref?.includes("/library"), "library deep link");
  await shot("01-bound-desktop-1440");
  results.push("bound state groups active level and config, shows binding details and opens the scoped map dialog");

  // 2. Denied scope.
  permissions = [
    { resource: "servers", action: "read", objectId: "*" },
    { resource: "world_view", action: "read", objectId: "*" },
  ];
  await gotoWorldTab();
  await page.getByText("No world access on this server", { exact: true }).waitFor();
  assert.equal(await page.getByRole("button", { name: /library world/i }).count(), 0);
  await checkMapDialog(/^$/);
  await shot("02-denied-scope-1440");
  results.push("without worlds/read the server can still open its map when world_view/read is allowed");
  permissions = [{ resource: "servers", action: "read", objectId: "*" }];
  await gotoWorldTab();
  await page.getByText("No world access on this server", { exact: true }).waitFor();
  assert.equal(await openMap.count(), 0, "Open map needs world_view/read on this server");
  await page.getByText("Opening a map requires World View read access on this server.").waitFor();
  permissions = [{ resource: "*", action: "*", objectId: "*" }];

  // 3. Unbound level (configured, no binding or observation).
  binding = null;
  targets = [];
  await gotoWorldTab();
  await page.getByText("UNBOUND LEVEL", { exact: true }).first().waitFor();
  assert.equal(await worldTab.getByText("Configured level", { exact: true }).count(), 1);
  assert.equal(await worldTab.getByText("Binding & revision", { exact: true }).count(), 0);
  await shot("03-unbound-1440");
  results.push("unbound level is reported without a fabricated binding");

  // 4. Unmanaged server-local world.
  targets = [
    {
      serverId: "srv-1",
      worldRef: { kind: "KIND_SERVER_LOCAL", serverId: "srv-1", levelName: "survival" },
      name: "Local world",
      rendererState: "RENDERER_STATE_ABSENT",
    },
  ];
  await gotoWorldTab();
  await page.getByText("UNMANAGED WORLD", { exact: true }).first().waitFor();
  assert.equal(await worldTab.locator("dd").filter({ hasText: /^survival$/ }).count(), 1);
  assert.equal(await worldTab.getByText("Configured level", { exact: true }).count(), 1);
  assert.equal(await worldTab.getByText("Binding & revision", { exact: true }).count(), 0);
  await shot("04-unmanaged-1440");
  await worldTab.screenshot({ path: path.join(evidence, "04-unmanaged-tab-1440.png") });
  await page.setViewportSize({ width: 390, height: 844 });
  await worldTab.screenshot({ path: path.join(evidence, "04-unmanaged-tab-390.png") });
  await page.setViewportSize({ width: 1440, height: 1000 });
  results.push("observed server-local world is honestly unmanaged, not bound");

  // 5. Required race case: revision A's delayed report must not unlock revision B.
  binding = null;
  targets = [];
  compatMode = "compatible";
  bindOutcome = "success";
  await gotoWorldTab();
  await openPicker();
  await selectEntry("wld-a");
  const hold = [];
  holdCompat = hold;
  const before = compatCalls.length;
  await page.getByRole("button", { name: "Check compatibility" }).click();
  const deadline = Date.now() + 10000;
  while (compatCalls.length <= before && Date.now() < deadline)
    await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(compatCalls.at(-1).revisionId, "rev-a", "A was the checked revision");
  // Switch to library world B (and therefore revision B) while A's RPC is held.
  await page.getByLabel("Library world", { exact: true }).selectOption("wld-b");
  await page.waitForFunction(() => {
    const select = document.querySelector('select[id]:has(option[value="rev-b"])');
    return Boolean(select && select.value === "rev-b");
  });
  hold.splice(0).forEach((resolve) => resolve());
  holdCompat = null;
  await page.waitForTimeout(500);
  assert.equal(
    await page.getByText("COMPATIBLE", { exact: true }).count(),
    0,
    "A's compatible verdict must not be shown for B",
  );
  assert.ok(
    await page.getByText(/The checked revision, level or target changed/i).count(),
    "explicit stale-selection state",
  );
  assert.equal(
    await page.getByRole("button", { name: "Review & swap" }).count(),
    0,
    "no confirmation button for the stale tuple",
  );
  assert.equal(
    await page.getByRole("button", { name: "Re-check compatibility" }).first().isEnabled(),
    true,
    "the stale state must offer an active re-check",
  );
  await shot("05-stale-a-discarded-b-1440");
  results.push("delayed revision-A report discarded after switch to revision B");

  // Re-check B; only B's tuple unlocks confirmation.
  await page.getByRole("button", { name: "Re-check compatibility" }).first().click();
  const deadlineB = Date.now() + 10000;
  while (compatCalls.length < before + 2 && Date.now() < deadlineB)
    await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(compatCalls.at(-1).revisionId, "rev-b");
  await page.getByText("COMPATIBLE", { exact: true }).first().waitFor();
  const swapButton = page.getByRole("button", { name: "Review & swap" });
  assert.equal(await swapButton.isDisabled(), false);
  await shot("06-recheck-b-compatible-1440");
  await swapButton.click();
  await page.getByText("To: rev-b (1.21.1)", { exact: false }).waitFor();
  await page.getByRole("button", { name: "Confirm swap" }).click();
  const bindDeadline = Date.now() + 10000;
  while (
    !requests.some((r) => r.method === "BindWorld" && r.payload.revisionId === "rev-b") &&
    Date.now() < bindDeadline
  )
    await new Promise((resolve) => setTimeout(resolve, 10));
  const bindCall = requests.find((r) => r.method === "BindWorld");
  assert.equal(bindCall.payload.serverId, "srv-1");
  assert.equal(bindCall.payload.revisionId, "rev-b");
  assert.ok(bindCall.payload.requestId, "journaled request id");
  await page.getByText(/SUCCESS:/).first().waitFor();
  await shot("07-confirmed-b-success-1440");
  results.push("B-only compatibility unlocks explicit confirmation and journals the exact tuple");

  // 6. Incompatible and unknown-finding gates.
  compatMode = "blocked";
  await gotoWorldTab();
  await fullSwapWithoutConfirm();
  await page.getByText("BLOCKED", { exact: true }).first().waitFor();
  assert.equal(await page.getByRole("button", { name: "Review & swap" }).isDisabled(), true);
  await shot("08-blocked-1440");
  compatMode = "unknown-finding";
  await gotoWorldTab();
  await fullSwapWithoutConfirm();
  await page.getByText(/Unknown findings gate binding/i).first().waitFor();
  assert.equal(await page.getByRole("button", { name: "Review & swap" }).isDisabled(), true);
  await shot("09-unknown-finding-gated-1440");
  results.push("blocked verdict and unknown finding both refuse confirmation");

  // 7. Honest binding outcomes.
  for (const [outcome, label, copy] of [
    ["queued", "QUEUED", /not started/],
    ["skipped", "SKIPPED", /No mutation/],
    ["failed", "FAILED", /No applied state/],
  ]) {
    compatMode = "compatible";
    bindOutcome = outcome;
    await gotoWorldTab();
    await fullSwap();
    const banner = page.getByTestId("server-world-operation-status");
    await banner.waitFor();
    assert.ok(await banner.getByText(label, { exact: true }).count(), `${outcome} label`);
    assert.match(await banner.innerText(), copy);
    assert.equal(await banner.getByText("Uncertain operation", { exact: true }).count(), 0);
    await shot(`10-outcome-${outcome}-1440`);
    results.push(`${outcome} binding outcome shown honestly, no applied success claimed`);
  }

  bindOutcome = "unknown";
  await gotoWorldTab();
  await fullSwap();
  const uncertain = page.getByTestId("server-world-operation-status");
  await uncertain.getByText("OUTCOME UNKNOWN", { exact: true }).waitFor();
  assert.ok(await uncertain.getByRole("button", { name: "Roll back" }).count());
  await shot("11-outcome-unknown-1440");
  const resolveBefore = requests.filter((r) => r.method === "ResolveWorldOperation").length;
  await uncertain.getByRole("button", { name: "Roll back" }).click();
  const resolveDeadline = Date.now() + 10000;
  while (
    requests.filter((r) => r.method === "ResolveWorldOperation").length <= resolveBefore &&
    Date.now() < resolveDeadline
  )
    await new Promise((resolve) => setTimeout(resolve, 10));
  const resolveCall = requests.filter((r) => r.method === "ResolveWorldOperation").at(-1);
  assert.equal(resolveCall.method, "ResolveWorldOperation");
  assert.equal(resolveCall.payload.action, "rollback");
  await page.getByText(/Operation rollback reported: RESOLVED/i).waitFor();
  await shot("12-rollback-1440");
  results.push("unknown outcome exposes rollback; rollback is journaled, no auto-retry");
  bindOutcome = "success";

  // Helper reused by gate checks (check only, no confirm).
  async function fullSwapWithoutConfirm() {
    await openPicker();
    await selectEntry("wld-b");
    await checkCompat();
  }

  // 8. Responsive and keyboard QA.
  compatMode = "compatible";
  binding = null;
  targets = [];
  await gotoWorldTab();
  for (const width of [1440, 390, 360]) {
    await page.setViewportSize({ width, height: 900 });
    await page.waitForTimeout(200);
    assert.equal(
      await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth),
      false,
      `world tab document overflow at ${width}`,
    );
    await shot(`13-responsive-${width}`);
  }
  results.push("no horizontal document overflow at 1440/390/360");

  await page.setViewportSize({ width: 1440, height: 1000 });
  await gotoWorldTab();
  const tab = page.getByTestId("tab-world");
  await tab.focus();
  await page.keyboard.press("Enter");
  assert.equal(await tab.getAttribute("aria-selected"), "true");
  await page.keyboard.press("ArrowRight");
  assert.equal(await page.getByTestId("tab-modules").getAttribute("aria-selected"), "true");
  await page.keyboard.press("ArrowLeft");
  assert.equal(await tab.getAttribute("aria-selected"), "true");
  results.push("keyboard selection and arrow navigation work on the World tab");
  await shot("14-keyboard-1440");

  // 9. Client-side navigation invalidates an in-flight compatibility check.
  permissions = [{ resource: "*", action: "*", objectId: "*" }];
  compatMode = "compatible";
  binding = null;
  targets = [];
  await gotoWorldTab();
  await openPicker();
  await selectEntry("wld-a");
  const navHold = [];
  holdCompat = navHold;
  const navBefore = compatCalls.length;
  await page.getByRole("button", { name: "Check compatibility" }).click();
  const navDeadline = Date.now() + 10000;
  while (compatCalls.length <= navBefore && Date.now() < navDeadline)
    await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(compatCalls.at(-1).serverId, "srv-1");
  // Prove the in-component guard is exercised: mark this DOM node, then use a
  // client-side navigation that changes the server id but keeps ?tab=world so
  // SvelteKit reuses the page and the ServerWorld instance instead of
  // remounting the World subtree.
  await page.evaluate(() => {
    const panel = document.querySelector("[data-server-tab-panel]");
    if (panel) panel.setAttribute("data-qa-marker", "same-instance");
  });
  // SvelteKit intercepts same-origin anchor clicks through its global handler;
  // an injected link navigates client-side without replacing the page instance,
  // and preserving ?tab=world keeps the World subtree mounted.
  await page.evaluate(() => {
    const link = document.createElement("a");
    link.id = "qa-nav-link";
    link.href = "/servers/srv-2?tab=world";
    link.textContent = "QA navigation";
    document.body.appendChild(link);
    link.click();
  });
  await page.waitForURL("**/servers/srv-2*");
  await page.waitForTimeout(400);
  assert.equal(
    await page.getByTestId("tab-world").getAttribute("aria-selected"),
    "true",
    "the World tab must remain selected after navigation",
  );
  assert.equal(
    await page.locator('[data-qa-marker="same-instance"]').count(),
    1,
    "the World tab instance must survive client-side navigation",
  );
  navHold.splice(0).forEach((resolve) => resolve());
  holdCompat = null;
  await page.waitForTimeout(300);
  assert.equal(
    await page.getByText("COMPATIBLE", { exact: true }).count(),
    0,
    "srv-1 report must not appear on srv-2 after navigation",
  );
  // A fresh check on srv-2 must work; this catches a stuck compatBusy flag.
  await openPicker();
  await selectEntry("wld-a");
  const afterNav = compatCalls.length;
  await page.getByRole("button", { name: "Check compatibility" }).click();
  const afterNavDeadline = Date.now() + 10000;
  while (
    !compatCalls.slice(afterNav).some((call) => call.serverId === "srv-2") &&
    Date.now() < afterNavDeadline
  )
    await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(compatCalls.at(-1).serverId, "srv-2", "the new server can run a fresh check");
  await page.getByText("COMPATIBLE", { exact: true }).first().waitFor();
  assert.equal(await page.getByRole("button", { name: "Review & swap" }).isDisabled(), false);
  await shot("15-navigation-invalidates-1440");
  results.push(
    "param-only server navigation reuses the tab, discards the old report and still allows a fresh check",
  );

  assert.deepEqual(errors, [], `page errors: ${errors.join("; ")}`);
  console.log(`\nOSS-382 browser QA (${results.length} checks):`);
  results.forEach((line, index) => console.log(`  ${index + 1}. ${line}`));
  console.log(`\nEvidence: ${evidence}`);
  console.log(`Browser: ${browser.version()}`);
} finally {
  if (browser) await browser.close();
  if (viteServer)
    await Promise.race([
      viteServer.close(),
      new Promise((resolve) => setTimeout(resolve, 5000)),
    ]);
}
// The Vite dev server can leave a lingering HMR handle on Windows; all checks
// and screenshots are complete by this point.
process.exit(0);
