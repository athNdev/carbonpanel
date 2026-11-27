// SPDX-License-Identifier: MPL-2.0
import assert from "node:assert/strict";

function luminance(cssColor) {
  const channels = cssColor
    .match(/^[^(]+\((\d+), (\d+), (\d+)/)
    .slice(1)
    .map(Number);
  const linear = channels
    .map((c) => c / 255)
    .map((c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
  return linear[0] * 0.2126 + linear[1] * 0.7152 + linear[2] * 0.0722;
}

function contrast(a, b) {
  const values = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (values[0] + 0.05) / (values[1] + 0.05);
}

function appearance(field) {
  return field.evaluate((el) => {
    const style = getComputedStyle(el);
    return {
      border: style.borderTopColor,
      width: style.borderTopWidth,
      line: style.borderTopStyle,
      fill: style.backgroundColor,
      ink: style.color,
      outline: style.outlineColor,
      outlineWidth: style.outlineWidth,
      outlineStyle: style.outlineStyle,
    };
  });
}

// Server settings load their option lists after the first render and keep the
// selects disabled until that request settles. Wait for the real control state
// instead of sampling an intermediate loading style.
async function waitForFieldReady(page, field) {
  await page.waitForFunction(
    ({ id }) => {
      const el = document.getElementById(id);
      return Boolean(
        el &&
          !el.disabled &&
          !el.hasAttribute("data-disabled") &&
          el.getAttribute("aria-disabled") !== "true",
      );
    },
    { id: await field.getAttribute("id") },
    { timeout: 10000 },
  );
}

// Match the final semantic color. The hover event can reach Playwright before
// Chromium paints the new style, especially with reduced-motion transitions.
async function waitForFieldColor(page, field, property, token) {
  const expected = await field.evaluate((el, token) => {
    const probe = document.createElement("span");
    probe.style.color = `var(${token})`;
    el.parentElement.append(probe);
    const color = getComputedStyle(probe).color;
    probe.remove();
    return color;
  }, token);
  await page.waitForFunction(
    ({ id, property, expected }) => {
      const el = document.getElementById(id);
      return el && getComputedStyle(el)[property] === expected;
    },
    { id: await field.getAttribute("id"), property, expected },
    { timeout: 5000 },
  );
}

export async function checkFieldAppearance(page, base) {
  for (const theme of ["light", "dark"]) {
    await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
    await page.goto(base + "/servers/ready");
    const text = page.getByLabel("Server Name", { exact: true });
    const number = page.getByLabel("Memory (MB)", { exact: true });
    // The server detail page's settings section uses the snake_case trigger id;
    // the create-server wizard uses the camelCase one (#modLoader).
    const select = page.locator("#mod_loader");
    await text.waitFor();
    assert.equal(
      await page.locator("html").getAttribute("data-carbon-theme"),
      theme,
    );
    await page.mouse.move(0, 0);
    for (const control of [text, number, select]) {
      await waitForFieldReady(page, control);
      await waitForFieldColor(page, control, "backgroundColor", "--field-surface");
    }
    await waitForFieldColor(page, text, "borderTopColor", "--field-edge");
    const idle = await appearance(text);
    for (const control of [number, select]) {
      const style = await appearance(control);
      assert.equal(style.width, "2px");
      assert.equal(style.fill, idle.fill);
    }
    assert.equal(idle.width, "2px");
    assert.ok(contrast(idle.border, idle.fill) >= 3);
    assert.ok(contrast(idle.ink, idle.fill) >= 4.5);
    await text.hover();
    await waitForFieldColor(page, text, "borderTopColor", "--field-hover-edge");
    assert.notEqual((await appearance(text)).border, idle.border);
    await page.keyboard.press("Tab");
    await text.focus();
    await waitForFieldColor(page, text, "borderTopColor", "--field-focus");
    const focus = await appearance(text);
    assert.equal(focus.outlineStyle, "solid");
    assert.equal(focus.outlineWidth, "2px");
    assert.ok(contrast(focus.outline, focus.fill) >= 3);
    // Exercise validation and read-only/disabled styling on the mounted shared
    // input without mutating the real server or depending on a save failure.
    await text.evaluate((el) => el.setAttribute("aria-invalid", "true"));
    await waitForFieldColor(page, text, "borderTopColor", "--destructive-text");
    const invalid = await appearance(text);
    assert.notEqual(invalid.border, focus.border);
    assert.equal(invalid.outline, focus.outline);
    assert.ok(contrast(invalid.border, invalid.fill) >= 3);
    await text.evaluate((el) => {
      el.removeAttribute("aria-invalid");
      el.readOnly = true;
      el.blur();
    });
    assert.equal((await appearance(text)).line, "dashed");
    await text.evaluate((el) => {
      el.readOnly = false;
      el.disabled = true;
    });
    await waitForFieldColor(page, text, "backgroundColor", "--secondary");
    assert.notEqual((await appearance(text)).fill, idle.fill);
    await text.evaluate((el) => {
      el.disabled = false;
    });
    await page.goto(base + "/servers");
    const search = page.getByPlaceholder(
      "Search by name, description, version, loader, or port…",
    );
    await search.waitFor();
    assert.equal((await appearance(search)).fill, idle.fill);
    assert.equal((await appearance(search)).width, "2px");
    await page.goto(base + "/servers/new");
    await page.locator("#name").fill("Field Appearance Check");
    await page.getByRole("button", { name: "Next Step" }).click();
    const loaderSelect = page.locator("#modLoader");
    await loaderSelect.waitFor();
    const loaderStyle = await appearance(loaderSelect);
    assert.equal(loaderStyle.fill, idle.fill);
    assert.equal(loaderStyle.width, "2px");
    const controls = await page.evaluate(() => {
      const box = document.createElement("div");
      document.body.append(box);
      const styles = {};
      for (const tag of [
        "checkbox",
        "file",
        "range",
        "radio",
        "color",
        "textarea",
      ]) {
        const el = document.createElement(
          tag === "textarea" ? "textarea" : "input",
        );
        if (el instanceof HTMLInputElement) el.type = tag;
        box.append(el);
        styles[tag] = getComputedStyle(el).borderTopWidth;
      }
      box.remove();
      return styles;
    });
    assert.equal(controls.textarea, "2px");
    for (const type of ["checkbox", "file", "range", "radio", "color"])
      assert.notEqual(controls[type], "2px");
  }
  await page.emulateMedia({ colorScheme: null, reducedMotion: null });
}
