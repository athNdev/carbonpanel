// SPDX-License-Identifier: MPL-2.0
const menu = document.querySelector(".menu-toggle");
const navigation = document.querySelector("#navigation");
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
