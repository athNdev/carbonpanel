// SPDX-License-Identifier: MPL-2.0
// Evaluate this async expression in the console/CDP of a built local homepage.
// It exercises the real hydrated islands and restores all fixtures afterwards.
(async () => {
  const checks = [];
  const pause = (ms = 35) => new Promise((resolve) => setTimeout(resolve, ms));
  const assert = (pass, label) => {
    checks.push({ label, pass: !!pass });
    if (!pass) throw new Error(label);
  };
  const wait = async (fn, label) => {
    const end = Date.now() + 10000;
    while (!fn()) {
      if (Date.now() > end) throw new Error(`Timeout: ${label}`);
      await pause();
    }
  };
  const host = (name) => document.querySelector(`[data-fragment="${name}"]`);
  const root = (name) => host(name)?.shadowRoot;
  const text = (name) => (root(name)?.textContent ?? "").replace(/\s+/g, " ");
  const button = (name, label) =>
    [...root(name).querySelectorAll("button")].find(
      (el) =>
        (el.getAttribute("aria-label") || el.textContent.trim()).replace(
          /\s+/g,
          " ",
        ) === label,
    );
  const click = async (name, label) => {
    const el = button(name, label);
    if (!el || el.matches(":disabled"))
      throw new Error(`Missing/disabled ${name}: ${label}`);
    el.click();
    await pause();
  };
  const input = async (el, value) => {
    el.value = value;
    el.dispatchEvent(
      new Event(el.tagName === "SELECT" ? "change" : "input", {
        bubbles: true,
      }),
    );
    await pause();
  };
  const reset = async (name) => {
    root(name)?.querySelector(".demo-reset")?.click();
    await pause();
  };
  const tab = (label) => click("studio", label);
  const radio = async (value) => {
    root("staging")
      .querySelector(`input[type=radio][value="${value}"]`)
      .click();
    await pause();
  };
  const stage = async () => {
    const el = [...root("staging").querySelectorAll("button")].find((el) =>
      /^Stage \d/.test(el.textContent.trim()),
    );
    if (!el || el.matches(":disabled")) throw new Error("Cannot stage");
    el.click();
    await pause();
  };
  const recipe = () =>
    JSON.parse(
      decodeURIComponent(
        root("studio")
          .querySelector("a[download]")
          .href.split(",")
          .slice(1)
          .join(","),
      ),
    );
  try {
    for (const name of ["server", "studio", "staging"]) {
      host(name).scrollIntoView({ behavior: "instant", block: "center" });
      await wait(
        () => host(name).dataset.state === "ready",
        `${name} hydration`,
      );
    }
    await reset("server");
    await click("server", "Stop");
    await wait(() => !!button("server", "Start Server"), "stop");
    assert(
      text("server").includes("0 / 4 GiB") &&
        document.querySelector("[data-world-state]").dataset.worldState ===
          "stopped",
      "stop releases sample RAM and extinguishes entrance",
    );
    await click("server", "Start Server");
    await wait(
      () =>
        !!button("server", "Stop") &&
        !button("server", "Stop").matches(":disabled"),
      "start",
    );
    assert(
      text("server").includes("3 / 20") &&
        document.querySelector("[data-world-state]").dataset.worldState ===
          "running",
      "start restores players, resources and entrance",
    );
    await click("server", "Restart");
    assert(
      document.querySelector("[data-world-state]").dataset.worldState ===
        "stopped",
      "restart dims entrance while busy",
    );
    await reset("server");
    await pause(1200);
    assert(
      text("server").includes("Server ready") && !!button("server", "Stop"),
      "server reset cancels pending action",
    );
    await reset("studio");
    await click("studio", "Add Waystones");
    await tab("Your pack 8");
    assert(
      text("studio").includes("Balm") &&
        document.querySelector("#features").dataset.demoPhase === "curating",
      "dependency addition and Studio light reflect eight mods",
    );
    await click("studio", "Check updates");
    await click("studio", "Apply sample updates");
    assert(
      text("studio").includes("0 updates available"),
      "sample updates complete",
    );
    root("studio").querySelector('[aria-label="Select lithium"]').click();
    await pause();
    await input(
      root("studio").querySelector('[aria-label="Search sample mods"]'),
      "Waystones",
    );
    root("studio").querySelector('[aria-label="Select all mods"]').click();
    await pause();
    await input(
      root("studio").querySelector('[aria-label="Search sample mods"]'),
      "",
    );
    assert(
      root("studio").querySelector('[aria-label="Select lithium"]').checked &&
        root("studio").querySelector('[aria-label="Select waystones"]').checked,
      "filtered select-all preserves hidden selections",
    );
    await click("studio", "Pin selected");
    await tab("Export");
    await input(
      root("studio").querySelector('[aria-label="Export format"]'),
      "packwiz",
    );
    root("studio").querySelector(".studio-client input").click();
    await pause();
    await click("studio", "Build export preview");
    const built = recipe();
    assert(
      built.mods.length === 6 &&
        built.overrides.length === 1 &&
        built.excluded.length === 2 &&
        built.format === "packwiz",
      "server export filters client mods and overrides",
    );
    assert(
      built.mods.find((m) => m.slug === "lithium").version === "sample.1" &&
        built.mods.find((m) => m.slug === "fabric-api").version === "sample.2",
      "download includes updated and pinned versions",
    );
    assert(
      !built.playable &&
        built.files.includes("pack.toml") &&
        document.querySelector("#features").dataset.demoPhase === "ready",
      "manifest is labelled sample and lights ready state",
    );
    await input(
      root("studio").querySelector('[aria-label="Export format"]'),
      "mrpack",
    );
    assert(
      !root("studio").querySelector("a[download]"),
      "option changes invalidate stale downloads",
    );
    await tab("Your pack 8");
    await click("studio", "Remove Balm");
    assert(
      !button("studio", "Remove Waystones") &&
        !!button("studio", "Remove Jade"),
      "removing a library cascades without removing unrelated mods",
    );
    await tab("Overrides 3");
    root("studio").querySelector(".studio-file summary").click();
    await pause();
    assert(
      text("studio").includes("port=24454"),
      "override contents can be inspected",
    );
    await reset("studio");
    await tab("Your pack 6");
    root("studio").querySelector('[aria-label="Select all mods"]').click();
    await pause();
    await click("studio", "Remove selected");
    await tab("Export");
    assert(
      button("studio", "Build export preview").matches(":disabled"),
      "empty packs cannot export",
    );
    await reset("staging");
    for (const label of [
      "Add config change",
      "Add module change",
      "Add modpack change",
      "Add config change",
    ])
      await click("staging", label);
    assert(
      text("staging").includes("8 changes across 6 areas"),
      "eight edits collect across six areas",
    );
    await click("staging", "Remove Lithium");
    await radio("idle");
    await input(root("staging").querySelector("input[type=number]"), "5");
    await stage();
    assert(
      document.querySelector("#updates").dataset.demoPhase === "staged" &&
        text("staging").includes("Build 18 (sample)"),
      "staging leaves live values unchanged",
    );
    await click("staging", "Simulate players leaving");
    await click("staging", "Advance 1 empty minute");
    assert(
      text("staging").includes("1 / 5 empty minutes"),
      "idle progress is visible before the batch can apply",
    );
    await click("staging", "Simulate player joining");
    assert(
      text("staging").includes("0 / 5 empty minutes"),
      "a player joining resets the idle stretch",
    );
    await click("staging", "Simulate players leaving");
    await click("staging", "Advance 5 empty minutes");
    await wait(
      () => !!root("staging").querySelector(".batch-receipt"),
      "idle result",
    );
    assert(
      text("staging").includes("7 changes published") &&
        !root("staging")
          .querySelector(".batch-receipt")
          .textContent.includes("Lithium") &&
        document.querySelector("#updates").dataset.demoPhase === "applied",
      "only reviewed edits publish; completion lights respond",
    );
    await click("staging", "Prepare another batch");
    await click("staging", "Add mod change");
    await radio("next");
    await stage();
    await click("staging", "Simulate restart");
    await wait(
      () => !!root("staging").querySelector(".batch-receipt"),
      "second batch",
    );
    assert(
      text("staging").includes("1 change published"),
      "second batch works against updated live state",
    );
    await reset("staging");
    await radio("time");
    assert(
      [...root("staging").querySelectorAll("button")]
        .find((el) => /^Stage 4/.test(el.textContent.trim()))
        .matches(":disabled"),
      "missing schedule blocks staging",
    );
    await click("staging", "Use one hour from now");
    await stage();
    await click("staging", "Advance to scheduled time");
    await wait(
      () => !!root("staging").querySelector(".batch-receipt"),
      "timed result",
    );
    assert(
      text("staging").includes("4 changes published"),
      "scheduled apply can be completed explicitly",
    );
    await reset("staging");
    await stage();
    await click("staging", "Simulate restart");
    await reset("staging");
    await pause(1300);
    assert(
      !root("staging").querySelector(".batch-receipt") &&
        text("staging").includes("4 changes across 4 areas"),
      "reset cancels an in-flight apply",
    );
    assert(
      document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      "decorative effects and controls stay within the viewport",
    );
    return { pass: true, checks };
  } catch (error) {
    return { pass: false, error: String(error), checks };
  } finally {
    for (const name of ["server", "studio", "staging"]) await reset(name);
  }
})();
