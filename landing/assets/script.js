// SPDX-License-Identifier: MPL-2.0
const menu = document.querySelector(".menu-toggle");
const navigation = document.querySelector("#navigation");
// Only the fixture server island emits this event. The artwork mirrors its
// completed state; text in the real control remains the status indicator.
document.addEventListener("carbon-demo-server-state", (event) => {
  if (event.target?.dataset?.fragment !== "server") return;
  const world = document.querySelector("[data-world-state]");
  if (world)
    world.dataset.worldState = event.detail.running ? "running" : "stopped";
});
// Each isolated fixture owns its own decorative signal. Payloads carry only
// allowlisted presentation states; they never drive another demo's data.
for (const [name, selector, phases] of [
  ["studio", ".studio-section", ["idle", "curating", "ready"]],
  ["staging", ".staging-section", ["review", "staged", "applying", "applied"]],
]) {
  document.addEventListener(`carbon-demo-${name}-state`, (event) => {
    if (event.target?.dataset?.fragment !== name) return;
    if (!phases.includes(event.detail?.phase)) return;
    const section = document.querySelector(selector);
    if (section) section.dataset.demoPhase = event.detail.phase;
  });
}
function closeMenu(restoreFocus = false) {
  navigation?.classList.remove("is-open");
  menu?.setAttribute("aria-expanded", "false");
  menu?.setAttribute("aria-label", "Open menu");
  if (restoreFocus) menu?.focus();
}
menu?.addEventListener("click", () => {
  const expanded = menu.getAttribute("aria-expanded") === "true";
  navigation?.classList.toggle("is-open", !expanded);
  menu.setAttribute("aria-expanded", String(!expanded));
  menu.setAttribute("aria-label", expanded ? "Open menu" : "Close menu");
});
navigation
  ?.querySelectorAll("a")
  .forEach((link) => link.addEventListener("click", () => closeMenu()));
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && menu?.getAttribute("aria-expanded") === "true")
    closeMenu(true);
});
matchMedia("(min-width: 701px)").addEventListener("change", (event) => {
  if (event.matches) closeMenu();
});
document
  .querySelector("[data-copy]")
  ?.addEventListener("click", async (event) => {
    const button = event.currentTarget;
    const source = document.getElementById(button.dataset.copy);
    const status = document.querySelector(".copy-status");
    try {
      await navigator.clipboard.writeText(source.textContent.trim());
      status.textContent = "Source command copied.";
    } catch {
      status.textContent = "Select the command to copy it manually.";
    }
  });
