# Design system sync for the v2.1.1 quality pass

**Status:** Research / audit complete. Implementation not started.
**Jira:** OSS-466 (child of epic OSS-448, "Sync the design system, its components and its page with the quality pass").
**Baseline:** branch `feat/v2.1.1-improvements`, audited 2026-11-23.
**Audit inputs:** `web/carbon-panel/src/lib/design-system/`, `web/carbon-panel/src/lib/components/carbon/`,
`docs/design-system/`, `scripts/test_design_system_page.py`, the surfaces the quality pass touches
(server mods, mod browser, servers/new, modpacks studio, worldview), and the landing demo harness.

> **PR #729 split note (2026-11-27):** The reviewed mod-update apply/selection
> panel described in this research was removed by the PR #729 split. References
> below to `src/lib/mod-update-ui.ts`, `updateStateTagType()`,
> `updateOutcomeTagType()`, `updateOutcomeLabel()`, `updateStateClass()`,
> `server-mods-update.test.ts`, the per-row "Stage this update" control, the
> staged-outcome list and the automatic-policy accordion are historical and no
> longer describe the tree. The retained surface is the read-only #680
> installed-mod preview in
> `internal/rpc/services/mod_update_preview.go`, summarized by
> `summarizeModUpdateCheck()` in
> `web/carbon-panel/src/lib/utils/mod-update-summary.ts`, and rendered by the
> server-mods preview row with no stage or apply controls. Do not restore the
> removed panel from this plan; #687 owns any future apply work.

## 1. Goal and scope

The quality pass introduces four idioms: a unified mods management UI, a floating save/undo action
bar with a shake indication for blocked state changes, World View live-terrain loading states, and
copy/terminology normalization. This plan catalogs the design system as it exists, identifies what
is missing for each incoming idiom, lists the drift between the system and the touched surfaces,
and sequences the work so implementation agents land code and design-system sync together without
churn.

The contract that forces "together": `scripts/test_design_system_page.py` (run in CI via
`.github/workflows/ci.yml` and `docs.yml`) fails when any export in
`web/carbon-panel/src/lib/components/carbon/index.ts` lacks a documented section with a props
table on `docs/design-system/index.html`. A new component or token cannot land in one commit and
its documentation in another.

## 2. Current state

### 2.1 `tokens.css` (canonical source, 254 lines)

- **Theme sets.** `[data-carbon-theme='light']` (lines 6–82) and the dark default on
  `:where(:root), .dark, [data-carbon-theme='dark']` (lines 88–166). The zero-specificity
  `:where(:root)` is deliberate (dark default, explicit themes always win) and is contract-tested.
- **Semantic color roles** per theme: `--background`, `--foreground`, `--card`, `--popover`,
  `--primary`/`--primary-text`/`--primary-foreground`, `--secondary`, `--muted`,
  `--muted-foreground`, `--accent`, `--destructive`/`--destructive-text`, `--success`/`--success-text`,
  `--warning`/`--warning-text`, `--border`, `--input`, `--ring`; quiet field roles
  (`--field-surface/edge/hover-edge/focus`); button hover/active roles; chart roles 1–5; sidebar roles.
- **Tag palette:** 11 tones × `--tag-{tone}-bg`/`--tag-{tone}-text` per theme — blue, green, red,
  yellow, purple, cyan, teal, magenta, gray, warm-gray, cool-gray. Contrast ≥ 4.5 asserted per pair
  in `tokens.test.ts`.
- **Skeleton tokens:** `--skeleton-from`/`--skeleton-to` per theme, consumed by the `.motion-skeleton`
  utility in `app.css` (lines 671–680) with the shimmer animation.
- **Elevation:** `--carbon-shadow-none`/`-raised`/`-overlay` per theme (overlay is documented for
  "a dialog or similarly prominent floating surface" — `motion-and-elevation.md`).
- **Geometry (`:root` block, theme-independent):** `--radius: 0px` (square corners, enforced
  globally by `border-radius: 0 !important`), spacing steps `--carbon-space-1…7`
  (4/8/12/16/24/32/48 px), control heights `--carbon-control-sm/md/lg/xl` (32/40/48/64 px),
  font stacks Inter Variable + JetBrains Mono Variable.
- **Motion tokens:** durations `--motion-instant` 100 ms, `--motion-page` 140 ms, `--motion-fast`
  150 ms, `--motion-base` 200 ms, `--motion-slow` 300 ms, `--motion-surface` 500 ms,
  `--motion-stagger-step` 40 ms; easings out-quart/out-quint/out-expo/in-out-standard/sheet;
  `--carbon-motion-distance` 6 px, `--carbon-motion-press-scale` 0.96, pulse 1600 ms,
  shimmer 1400 ms; shorthands `--carbon-transition-color`, `--carbon-transition-press`,
  `--carbon-animation-fade-in/rise-in/pulse/shimmer` bound to `carbon-ds-*` keyframes
  (fade-in, rise-in, pulse, shimmer — lines 217–254).
- **No z-index/layering tokens exist.** The effective scale is scattered literals: global-loading
  rail `z-[60]` (`global-loading.svelte`), CarbonModal backdrop + CarbonShell header/menus `z-50`,
  shell sidebar `z-40`, mobile scrim `z-30`, in-content sticky headers `z-10`
  (studio tabs, file-tree), `scroll-to-top` `z-50`.
- **No shake animation exists.** A repo-wide grep for shake/wiggle finds nothing (only protobuf
  "Handshake" false positives).
- **Safe-area handling exists in exactly one place:** the sonner toast overrides
  (`ui/sonner/sonner.svelte`) use `max(var(--carbon-space-3), env(safe-area-inset-*))`. The
  `scroll-to-top` fixed button does not handle safe areas.

### 2.2 Carbon components (`src/lib/components/carbon/index.ts`)

**There are 20 exports**, not 19: CarbonButton, CarbonPageHeader, CarbonTile, CarbonTag,
CarbonDataTable, CarbonModal, CarbonConfirm, CarbonTextInput, CarbonSelect, CarbonTabs,
CarbonInlineLoading, CarbonBreadcrumbs, CarbonShell, CarbonSearch, CarbonAccordion,
CarbonAccordionItem, CarbonStructuredList, CarbonErrorState, CarbonEmptyState, **CarbonSpinner**.

Props that matter for the incoming work:

| Component | Key props / behavior |
| --- | --- |
| CarbonButton | `kind` primary/secondary/tertiary/ghost/danger; `size` sm/md/lg/xl on control tokens; `loading` renders an inline CarbonSpinner; `iconOnly` requires `aria-label`; safe disabled links. |
| CarbonTag | `type` = 11 palette tones; `size` sm/md; `filter` + `onclose`; `pulse`; monospace uppercase; colors all tokenized. |
| CarbonDataTable | `title`, `description`, `toolbar`/`header`/`children` snippets. No built-in loading/empty/error — the docs page explicitly says to compose CarbonEmptyState/CarbonInlineLoading/CarbonErrorState around it. |
| CarbonModal | `open` (bindable), `title`/`description` (aria-labelledby/describedby), primary/secondary text, `danger`, `size` sm→full, `bare`, `fullscreen`, awaited-style callbacks, `footer` snippet. Portals to `document.body` (stacking-context rule), scroll lock, focus trap + restoration. |
| CarbonConfirm | Built on CarbonModal size="sm"; awaits `onconfirm` before closing; `confirming` disables buttons. |
| CarbonTextInput / CarbonSelect | `label`/`helperText`/`error` with aria association; bindable value; password reveal. |
| CarbonTabs | APG keyboard model, roving tabindex, measured indicator with ResizeObserver; `badge` per tab. |
| CarbonInlineLoading | `description`, `status` active/finished/error; no live region of its own. |
| CarbonSpinner | `size` sm/md/lg (16/24/40 px), `label`, `description`, `children`; `role="status"`; inherits `currentColor` (visible on primary fills); collapses to pulse under reduced motion. Consumed by CarbonButton and a handful of call sites only. |
| CarbonEmptyState | `title`, `description`, `icon` snippet, single `actionLabel`+`onaction`. |
| CarbonErrorState | `title`, `message`, `retryLabel`, `onretry`; `role="alert"`. |
| CarbonStructuredList | `header`/`children` snippets; div-based (not a table). |
| CarbonShell | Product infrastructure; owns the page gutter; header `z-50`, sidebar `z-40`. |
| CarbonTile | `clickable`/`href`/`onclick`; tokenized surface. |

### 2.3 The documentation page (`docs/design-system/index.html`, standalone)

Sections: hero → 01 Colour tokens (`#color`) → 02 Typography (`#type`) → 03 Space and geometry
(`#space`) → 04 Elevation (`#elevation`) → 05 Motion (`#motion`) → 06 Component inventory
(`#components`, one `<article>` per component with demo + `props-table` + notes) → 07 Composition
rules (`#composition`: one gutter, scroll containers, horizontal strips, nested surfaces, overlays
portal to body, widths; density/breakpoints/tab-order spec blocks) → 08 Interaction patterns
(`#patterns`) → 09 Accessibility (`#accessibility`) → 10 Workflow (`#workflow`). The component nav
quick-links list at the top of section 06 and the footer both say **"19 components"**. `site.js`
implements the theme toggle and motion replay; `site.css` holds the illustrative demo styles.
`README.md` is the prose contract (principles, component inventory table, interaction patterns,
change/propagation workflow); `motion-and-elevation.md` is the motion/elevation reference.

### 2.4 Contract tests

- `scripts/test_design_system_page.py` — every export must have an `id="c-{kebab}"` section that
  names the component and contains a `props-table`; also type faces, button kinds, tokens link,
  theme toggle, HTML lang, social metadata. **Runs in CI (ci.yml, docs.yml).**
- `design-system/tokens.test.ts` — WCAG contrast for text roles on `--background`/`--card`, 3:1 for
  `--input`/`--ring`, all 11 tag pairs ≥ 4.5, button hover/active contrast, dark-default-yields-to-
  light specificity contract, `:root` equals the dark palette.
- `design-system/fonts.test.ts` — Inter + JetBrains Mono token names; every `@font-face` resolves to
  a self-hosted woff2 under `static/fonts`.
- `layout-motion-policy.test.ts` — route view-transitions use `--motion-page`, both reduced-motion
  paths (media query + stored `data-carbon-motion`), interruptible transitions, Svelte JS
  transitions go through `motionDuration()` (`lib/motion.ts`), tab hover animates color only.
- `scrollbar-policy.test.ts` — thin theme-colored scrollbars, `scrollbar-gutter: stable`,
  `.scroll-strip`, nested horizontal pane policy, `.scrollbar-none`, webkit fallback.
- `components/carbon/a11y-dom.test.ts` — real-DOM keyboard tests for CarbonModal (trap, Escape,
  focus restore), CarbonTabs (APG), CarbonButton (icon-only name, disabled link), CarbonErrorState
  (role=alert + retry), CarbonTextInput, CarbonBreadcrumbs, and the UpdateBatch fragment.
- Per-component tests: `CarbonButton.test.ts`, `CarbonTag.test.ts`, `CarbonTabs.test.ts`,
  `CarbonSpinner.test.ts`.

### 2.5 Landing demo harness (`web/carbon-panel/demo/`)

`demo/build.mjs` SSRs three fragments — `ServerLifecycleActions`, `StudioModTable`, `UpdateBatch`
(`src/lib/components/fragments/`) — into `landing/index.html`, and rewrites tokens
`:root` → `:host` so typography/density/motion roles inherit in the fragment context. New tokens
must keep working through that rewrite (define theme-independent tokens in the `:root` block; the
rewrite replaces both `:where(:root)` and `:root`). `source.json` records the rendered components
and `fixtureVersion`. UpdateBatch is the closest existing analog to the review/action-bar idiom: a
token-clean, a11y-tested review panel with apply-window semantics and CarbonButton actions.

## 3. Pre-existing failure to fix first: CarbonSpinner is undocumented

`index.ts` exports 20 components; `docs/design-system/index.html` documents 19. There is no
`c-spinner` section and no nav link, and the page claims "All nineteen exports" (section 06 intro)
and "19 components" (footer). Consequently `test_every_exported_component_has_a_documented_section`
**fails right now** (`missing section for CarbonSpinner`), which makes the design-system page test
red in CI independent of the quality pass. CarbonSpinner is one of the better-documented candidates:
tokenized motion, `role="status"`, reduced-motion collapse, and it is the designated replacement for
the 90+ ad-hoc spinners (see §5). Fix: add section `20 / FEEDBACK CarbonSpinner` (demo, props table
for `size`/`label`/`description`/`children`, notes on currentColor inheritance, button usage and
reduced motion), add the nav link, update both count claims to "20 components", and keep the test's
`assertGreaterEqual(len(exports), 19)` as-is.

## 4. Gap analysis by incoming idiom

### 4.1 Floating save/undo action bar over server view sections

What exists: an "unsaved changes" counter inside `server-configuration.svelte`; the UpdateBatch
review panel (inline, not floating); `scroll-to-top` (fixed, bottom-right, `z-50`, no safe area);
sonner toasts (fixed, safe-area aware); sticky top headers in studio/file-tree (`z-10`,
`bg-background/95 backdrop-blur`); the composition rule "anything fixed goes through a portal to
`document.body`".

What is missing:

1. **Layering tokens.** No z-index scale is tokenized or documented. The bar needs a defined layer:
   above page content and in-content sticky headers (`z-10`), below the shell header/sidebars
   (`z-40`/`z-50`), below modals (`z-50` portaled) and below the global-loading rail (`z-[60]`).
   Proposed: a documented layer scale in the `:root` block — `--carbon-z-sticky: 10`,
   `--carbon-z-action-bar: 30`, `--carbon-z-scrim: 30`, `--carbon-z-sidebar: 40`,
   `--carbon-z-header: 50`, `--carbon-z-overlay: 50`, `--carbon-z-progress-rail: 60` — plus a
   "Layers" spec block in the Elevation section mapping each existing literal to its token. The
   values match today's reality; nothing visual changes, the literals become tokens.
2. **Safe-area positioning pattern.** Reuse the sonner `max(var(--carbon-space-N),
   env(safe-area-inset-bottom))` approach; document it as part of the fixed-surface rule so the bar
   and `scroll-to-top` do not collide with mobile browser chrome.
3. **A component or a codified pattern.** Four-plus surfaces will repeat this. Proposed component:
   **`CarbonActionBar`** (export #21) — fixed bottom bar, `role` region with `aria-label`
   ("Pending changes"), portaled to `body`, safe-area padding, `--carbon-shadow-overlay` + border.
   Draft props: `open`/`visible` (bindable), `changeCount?: number`, `summary?: string`
   (e.g. "3 unsaved changes"), `saving?: boolean`, `blocked?: string` (reason text; when set, Save
   is refused), `onsave`, `ondiscard`, `saveLabel = 'Save changes'`, `discardLabel = 'Discard'`,
   `children` snippet (extra context, e.g. apply-window), `onblocked?` hook that drives the shake.
   Behavior contract: announced via a polite live region when it appears or its summary changes;
   Save click while `blocked` triggers the shake (§4.2), announces the reason, and performs no
   request; disabled while `saving`; keyboard: actions are standard buttons, Escape does not discard
   (destructive) — discard is an explicit control.
4. **A composition rule.** New rule under `#composition`: "Review and action bars" — when a section
   has unsaved state, the bar is the single save/discard surface; it is fixed to the viewport,
   portaled, sits at `--carbon-z-action-bar`, uses safe-area insets, and pages must not also render
   their own inline save rows for the same state (the existing per-tile "Save Settings" button in
   studio collapses into the bar).

### 4.2 Shake feedback for blocked state changes

Nothing exists. Proposed additions:

- `tokens.css` (`:root` block): `--carbon-motion-shake-distance: 4px` and
  `--carbon-motion-shake-duration: 400ms`; `--carbon-animation-shake: carbon-ds-shake
  var(--carbon-motion-shake-duration) var(--ease-in-out-standard) 1;` plus the
  `@keyframes carbon-ds-shake` (translateX oscillation bounded by
  `±var(--carbon-motion-shake-distance)`, 3–4 cycles, ending at rest).
- `app.css`: a `.motion-shake` utility that applies the animation and is removed on animationend
  (so it can retrigger), mirroring how `.motion-pulse`/`.motion-press` work.
- **Reduced-motion policy (mandatory):** under `prefers-reduced-motion: reduce` **and**
  `[data-carbon-motion='reduced']`, the shake must be replaced by a non-motion cue — a persistent
  border/text change (e.g. the bar's border and reason text switch to `--destructive-text`) plus
  the live-region announcement. This follows the existing pattern where CarbonSpinner swaps rotation
  for a pulse and route transitions snap. Assert both paths in the motion policy test.
- Documentation: a new row in the animation contracts table in `motion-and-elevation.md` and in the
  `#motion` section of the page ("`--carbon-animation-shake` | Horizontal ±4 px oscillation over
  400 ms, once | A refused save or blocked state change; always paired with a text reason; never
  loops; disabled under reduced motion"). Shake is feedback for a *specific* failure, not a
  decoration: document "do not shake on validation errors the user has not yet triggered".

### 4.3 Unified mods result card (4+ surfaces)

Current state per surface:

- `server-mods.svelte`: bespoke `div` rows (a comment says "Carbon DataTable rows" but it is not a
  table), each row with a bespoke enable/disable chip (`border-success bg-success/20` + icon +
  uppercase label — not a CarbonTag), a bespoke version chip (`border border-border bg-background
  px-1.5`), off-scale spacing (`p-3.5`, `gap-3.5` — 14 px is not in the 4/8/12/16/24/32/48 scale),
  icon-only Download/Delete buttons carrying only `title=` (no `aria-label` — a11y drift), and
  hand-rolled empty/no-match/loading panels instead of CarbonEmptyState/CarbonInlineLoading.
- `mod-browser-dialog.svelte`: bits-UI `Card`/`CardContent` with dead `rounded-lg` classes (the
  global radius kill-switch flattens them, but they are drift), a `h-12 w-12` icon thumb,
  **two badge systems in one card** (shadcn `Badge` for downloads/server-side/client-side/categories
  vs `CarbonTag` for Installed/Selected), shadcn `Alert` for errors, `Loader2` spinners, and a
  hand-rolled empty state.
- `datapack-browser-dialog.svelte` and `pack-content-panel.svelte`: same bits-UI card/badge idiom.
- `routes/modpacks/studio/[id]/+page.svelte`: CarbonTile + CarbonTabs + CarbonSearch + filter pills
  (CarbonButton primary/secondary toggles) — the cleanest of the four; its mod rows are the model
  for the unified card.

Gap: no shared list-item/result-card primitive exists. `CarbonStructuredList` is too rigid (header +
identical grid rows), `CarbonDataTable` is a real table (the mod browser results are not tabular).
Proposed component: **`CarbonResultCard`** (export #22) — a horizontal card row:
`leading` snippet (icon or 48 px thumbnail), `title`, `meta` snippet (mono metadata line:
filename · size · date), `tags` snippet (CarbonTag row), `description` (truncating), `actions`
snippet (trailing controls), `selected?: boolean` (accent border, space reserved per the
selection rule), `interactive?: boolean`, `class`. Composition contract: status always via
CarbonTag (one badge idiom), metadata always mono `text-xs text-muted-foreground`, one primary
action, icon-only actions must carry `aria-label`. Adopt in server-mods rows, mod-browser results,
studio mods table, and datapack browser. The alternative (document-only pattern, no component) was
considered and rejected: four surfaces re-implementing the same anatomy is exactly the
"recurring need … without a local workaround" bar from the README principles.

### 4.4 Badge/tag semantics for mod states

The palette is fully tokenized (11 tones, contrast-tested). The semantic mapping already exists in
`lib/mod-update-ui.ts` (historical; removed by the PR #729 split, see the split note above): update
states → `green` (available), `gray` (current, unknown_source), `yellow` (review_required), `red`
(unavailable); staged outcomes → `green` (staged), `yellow` (refused_*), `red` (error). Labels always
spell the state ("Refused: checksum mismatch"), so color is never the sole carrier. The retained
read-only preview renders its state and reason as text and has no staged outcomes.

Gaps: (1) the mapping lives in a mods-specific module while other surfaces (datapacks, deploy
dialog, module states) re-derive tones ad hoc; (2) `updateStateClass()` in the same file emits a
*second*, divergent chip idiom (raw `border-success bg-success/10` classes) that should be deleted
in favor of `updateStateTagType()`; (3) the docs page never states the semantic tone assignments.
Plan: add a "Status tags" spec block to the `#components` section (or under `#patterns`):
green = available/healthy/staged, yellow = attention/refused-review, red = failed/error,
gray = neutral/current/unknown, blue = informational, cyan/teal = count/selection accents, purple/
magenta reserved for branding accents. Keep `mod-update-ui.ts` as the executable mapping for mods
and reference it from the docs. Enable/Disabled chips move to CarbonTag gray/green with the state
in the label ("Active"/"Disabled" already do this). *(The `mod-update-ui.ts` mapping was removed by
the PR #729 split; use the retained `summarizeModUpdateCheck()` summary instead.)*

### 4.5 Empty, error and loading states

The components exist and are sufficient: CarbonEmptyState (zero results vs truly empty distinction
documented), CarbonErrorState (`role="alert"`, async retry), CarbonInlineLoading (active/finished/
error), CarbonSpinner (with `description` for "Reading mods/ directory..."-style copy). The gap is
adoption: the touched surfaces hand-roll Package-icon empty panels (server-mods, mod-browser) and
use `Loader2` spinners everywhere (92 `animate-spin` occurrences across ~30 files). The docs
already prescribe composition ("Compose CarbonEmptyState, CarbonInlineLoading or CarbonErrorState
around it rather than rendering a table with no rows"); the sync work is to make the mods surfaces
comply and to add the rule "use CarbonSpinner (or a loading prop), never a raw lucide Loader2 with
animate-spin" to the component notes and interaction patterns.

### 4.6 World View live-terrain loading states

`WorldMapLoading.svelte` is already a good citizen: token colors, `role="status"`, persistent text
label, `compact` variant, and reduced-motion handled for both the media query and the stored
`data-carbon-motion` attribute. `WorldViewWorkspace.svelte` drives it from a complete view state
machine (`loading`, `installing`, `rendering`, `ready`, `stale`, `saved`, `onboarding`, `unknown`,
`error`, `unsupported`, `stopped`) with per-view labels ("Starting the map renderer…", "Rendering
terrain…"). Gaps for the quality pass: (1) if live-terrain loading gains progressive/preview states,
the skeleton idiom to use is `.motion-skeleton` (tokenized, shimmer, reduced-motion-safe) —
document a "progressive content loading" note in `#patterns` ("skeleton for known layout,
CarbonSpinner/WorldMapLoading for unknown duration, label always present"); (2) WorldMapLoading is
not part of the Carbon inventory (app-level component) — record it in the README interaction
patterns as the canonical "map/terrain loading" pattern rather than exporting it, to keep the
component inventory product-agnostic; (3) the view→label mapping deserves the same copy
normalization pass as mods (sentence case, no trailing ellipsis inconsistency — the current labels
use "…" consistently; keep it).

### 4.7 Copy and terminology normalization

Existing anchors: the README "Content" pattern (direct sentence-case labels), the CarbonConfirm
consequence-naming rules, UpdateBatch's apply-window terminology ("Apply window", "Next restart",
"When empty", "Pick a time", "Staged"), and `updateOutcomeLabel()` (historical; removed by the PR #729
split). Gap: there is no single
terminology table for the idioms the quality pass unifies. Plan: add a "Terminology" spec block to
the docs page under `#patterns`: mod update states (Available / Current / Review required /
Unavailable), staged outcomes (Staged / Refused: … / Error), batch terms (Staged changes, Discard,
Apply window, Apply now), action-bar terms (Unsaved changes, Save changes, Discard). Each label
maps to a tag tone and a live-region announcement. Normalize touched-surface copy to these exact
strings (e.g. server-mods "No mods installed" vs mod-browser "No matching mods found" stay distinct
— empty-collection vs filtered-empty — but both should follow the empty-state copy rules).

## 5. Drift inventory (surfaces the quality pass touches)

| # | Location | Drift | Fix |
| --- | --- | --- | --- |
| 1 | `docs/design-system/index.html` | CarbonSpinner undocumented; "nineteen exports"/"19 components" claims; nav link missing; CI test failing | Add section + nav; update counts (§3) |
| 2 | `src/lib/components/fragments/ServerLifecycleActions.svelte` | Hardcoded hexes: `bg-[#8a3ffc]`/`hover:bg-[#6929c4]` (purple accent), `hover:bg-[#ba1b23]` (danger hover), `bg-[#198038]`/`hover:bg-[#0e6027]` (success), `hover:bg-[#d2a106]` (warning hover); raw bespoke buttons instead of CarbonButton; `Loader2` spinners | Rebuild on CarbonButton kinds + tokens (add a purple "deepsleep" accent decision to the tag/attention palette or use `--chart-4` token); re-run `bun run build:homepage` |
| 3 | `src/lib/components/server-mods.svelte` | Bespoke rows + chips; `p-3.5`/`gap-3.5` off-scale; icon-only actions without `aria-label`; hand-rolled empty/loading; dual idiom with `updateStateClass` | Adopt CarbonResultCard + CarbonTag mapping + CarbonEmptyState + token spacing (§4.3–4.5) |
| 4 | `src/lib/components/mod-browser-dialog.svelte` | bits-UI Dialog/Input/Badge/Tabs/Card/Alert alongside CarbonButton/CarbonTag; dead `rounded-lg`; `Loader2` ×4; hand-rolled empty/error | Migrate shell to CarbonModal, badges to CarbonTag, states to Carbon* (§4.3); keep bits-UI only where Carbon has no equivalent until a dedicated migration item exists |
| 5 | `src/lib/components/server-configuration.svelte` | bits-UI Input/Label/Switch + Carbon mix; inline unsaved-changes counter | Unify onto the action bar; field migration can ride the quality pass only where the file is already touched |
| 6 | `src/lib/components/server-ui/ServerModules.svelte` | `Loader2` ×5 | CarbonSpinner / loading props |
| 7 | `routes/modpacks/studio/[id]/+page.svelte` | `Loader2` ×8 (one inside a CarbonButton — use the `loading` prop); `text-amber-500` icon (non-token color) | `loading` props; token color (e.g. `text-warning-text` or accent) |
| 8 | `routes/modpacks/studio/+page.svelte`, `export-dialog.svelte` | `Loader2` ×3 | Same |
| 9 | `routes/servers/new/+page.svelte` | `Loader2` ×1 (otherwise clean — no hex, no rounded) | Same |
| 10 | `src/lib/mod-update-ui.ts` | `updateStateClass()` second chip idiom | Delete in favor of `updateStateTagType()` (historical: file and helpers removed by the PR #729 split; no executable mapping remains) |
| 11 | `src/lib/components/scroll-to-top.svelte` | Fixed surface without safe-area insets; raw `z-50` | Apply the documented safe-area pattern + `--carbon-z-action-bar`-adjacent token |
| 12 | `src/lib/components/server-console.svelte`, `server-ui/ModuleLogsDialog.svelte` | Literal terminal fg/bg hexes (`#161616/#ffffff/#f4f4f4`, `#e8e8e8/#000000`) | Map to `--background`/`--foreground` (+documented terminal exception only if contrast requires) |
| 13 | Systemic | 92 `Loader2`/`RefreshCw animate-spin` occurrences vs CarbonSpinner | Migrate at least every touched surface; add the "no raw Loader2" rule to docs |

Drift deliberately **out of scope** here: the full bits-UI → Carbon migration (~40+ files import
`$lib/components/ui`), shadcn `Alert`/`Switch`/`Checkbox`/`Progress` replacements without Carbon
equivalents, and non-mod surfaces' spacing cleanups. The quality pass should not rename the world;
it unifies the four incoming idioms and fixes drift in the files it already touches. Record the
rest in the README's standing "audit and migrate each surface" caveat.

## 6. Proposed design-system changes

### 6.1 Tokens and keyframes (`tokens.css`, all in the `:root` block so the demo `:host` rewrite keeps working)

```css
--carbon-z-sticky: 10;          /* in-content sticky headers (studio tabs, file-tree) */
--carbon-z-action-bar: 30;      /* floating save/undo bar; below header/sidebar/modal */
--carbon-z-scrim: 30;           /* shell mobile scrim */
--carbon-z-sidebar: 40;
--carbon-z-header: 50;          /* shell top bar, menus */
--carbon-z-overlay: 50;         /* portaled dialogs */
--carbon-z-progress-rail: 60;   /* global loading rail */

--carbon-motion-shake-distance: 4px;
--carbon-motion-shake-duration: 400ms;
--carbon-animation-shake: carbon-ds-shake var(--carbon-motion-shake-duration)
  var(--ease-in-out-standard) 1;

@keyframes carbon-ds-shake {
  0%, 100% { transform: translateX(0); }
  20% { transform: translateX(calc(var(--carbon-motion-shake-distance) * -1)); }
  40% { transform: translateX(var(--carbon-motion-shake-distance)); }
  60% { transform: translateX(calc(var(--carbon-motion-shake-distance) * -0.5)); }
  80% { transform: translateX(calc(var(--carbon-motion-shake-distance) * 0.5)); }
}
```

No new colors: the bar and shake reuse `--border`, `--card`, `--destructive-text`,
`--carbon-shadow-overlay`. `tokens.test.ts` needs no new contrast pairs for that reason; add only a
"keyframes exist for every `--carbon-animation-*` shorthand" assertion (currently implicit).

### 6.2 New components (each = one commit with tests + docs section + README row)

**CarbonActionBar** (export #21, "REVIEW / OVERLAY"): props `open` (bindable), `summary`,
`changeCount?`, `saving?`, `blocked?` (string reason), `saveLabel`, `discardLabel`, `onsave`,
`ondiscard`, `children` (context snippet, e.g. apply window), `class`. Fixed bottom, portaled to
`document.body`, safe-area bottom padding (`max(var(--carbon-space-4),
env(safe-area-inset-bottom))`), `z-index: var(--carbon-z-action-bar)`, `--carbon-shadow-overlay`,
top border. Save while `blocked` → applies `.motion-shake` to the bar, announces the reason via a
polite live region, makes no request; `.motion-shake` is suppressed by both reduced-motion paths
and replaced by a persistent `--destructive-text` border/reason treatment. Tests: component test +
a11y-dom (announce on appear, blocked save shakes but does not call `onsave`, discard works,
focus/keyboard sanity).

**CarbonResultCard** (export #22, "DATA"): props `leading` snippet, `title` (string or snippet),
`meta` snippet, `tags` snippet, `description` snippet, `actions` snippet, `selected?`,
`interactive?`, `class`. Anatomy contract: 48 px leading slot, one mono metadata line, tags via
CarbonTag only, trailing actions right-aligned, selected state reserves the accent border (no
layout shift), min target sizes for icon-only actions. Tests mirroring CarbonTag.test.ts +
a11y-dom for the selected/interactive states.

### 6.3 Existing component adjustments (small, contract-safe)

- CarbonDataTable: no change (keep compose-externally contract).
- CarbonButton: none needed — `loading` already exists; the migration is call-site work.
- No CarbonCheckbox this round: the policy UI's native checkboxes are acceptable; note it in the
  README audit caveat instead of growing the inventory mid-pass.

### 6.4 Documentation changes (same commits as §6.1/§6.2)

`docs/design-system/index.html`:
- Add `c-spinner` section (20 / FEEDBACK) + nav link + fix the two "19/nineteen" claims (§3).
- Add `c-action-bar` (21 / REVIEW) and `c-result-card` (22 / DATA) sections with demos, props
  tables and notes; update the component nav and footer count.
- `#elevation`: add a "Layers" spec block documenting the `--carbon-z-*` scale, the portal rule,
  and the rule that a fixed bar never sits above header/overlay/progress-rail.
- `#motion`: add the shake row to the animation contracts and the reduced-motion replacement rule.
- `#space`: no new steps; add a note that `p-3.5`-style half steps are off-scale (14 px is not in
  the system) after the mods rows are normalized.
- `#components`: add the "Status tags" tone→semantic table (§4.4).
- `#composition`: add the "Review and action bars" rule and the "Fixed surfaces use safe-area
  insets" rule (sonner pattern as the reference implementation).
- `#patterns`: add "Progressive content loading" (skeleton vs spinner vs labeled status) and the
  "Terminology" block (§4.7).
- `site.css`: demo hooks for the new sections (`.action-bar-demo`, `.result-card-demo`,
  `.shake-demo`), following the existing `.button.tertiary`-style hook naming.

`docs/design-system/README.md`: component inventory table — Feedback row gains CarbonSpinner,
a new "Review / action bar" row for CarbonActionBar, "Data" row gains CarbonResultCard; interaction
patterns gain the blocked-change feedback paragraph (shake + reason + reduced-motion cue).
`docs/design-system/motion-and-elevation.md`: shake row in the animation contracts table; z-scale
note under Elevation.

### 6.5 Contract test changes

- `scripts/test_design_system_page.py`: keep the existing assertions; optionally add a footer-count
  consistency check (`"N components"` string matches the export count) so the count claims cannot
  rot again.
- `design-system/tokens.test.ts`: assert every `--carbon-animation-*` shorthand references a
  `carbon-ds-*` keyframe defined in the file; assert the `--carbon-z-*` scale is strictly ordered
  (sticky < action-bar ≤ scrim < sidebar < header = overlay < progress-rail).
- `layout-motion-policy.test.ts`: assert `carbon-ds-shake` exists, is referenced only via the
  animation shorthand, and that both reduced-motion paths disable it (media query block + the
  `[data-carbon-motion='reduced']` rule), with the non-motion replacement (border/reason color)
  asserted in the component test; assert no new hardcoded duration > `--motion-slow` outside the
  sanctioned pulse/shimmer/spinner exceptions.
- `a11y-dom.test.ts`: CarbonActionBar and CarbonResultCard cases (§6.2).
- Component tests: `CarbonActionBar.test.ts`, `CarbonResultCard.test.ts` following
  `CarbonTag.test.ts`/`CarbonButton.test.ts` patterns.

## 7. Order of operations

Each numbered step is a "land together" unit — the repo's every-export contract and the README's
change workflow (propose against the contract → update Panel source and this page in the same
change → verify surfaces) make splitting them churn-prone. Sequencing keeps later steps unblocked:

1. **Phase 0 — fix the standing CI red (independent, land first).** Add the CarbonSpinner docs
   section + nav link + count fixes (§3). Run `python -m unittest scripts.test_design_system_page`.
   No tokens, no components. Unblocks everything else by making the page test green.
2. **Phase 1 — foundations.** tokens.css (z-scale + shake token/keyframes) + `app.css` (`.motion-shake`
   utility) + tokens.test.ts/layout-motion-policy extensions + page `#elevation` Layers block and
   `#motion` shake row + `motion-and-elevation.md`. Nothing consumes the new tokens yet; the docs
   land with them per the workflow. Run `bun run test` + the Python page test.
3. **Phase 2 — components.** CarbonActionBar + CarbonResultCard (+ component tests, a11y-dom cases)
   + page sections + README inventory rows + site.css demo hooks. The every-export test forces the
   docs into the same commit as the exports.
4. **Phase 3 — surface adoption and drift fixes on touched files.** Unified mods UI (server-mods →
   ResultCard/ActionBar/tag mapping; mod-browser → CarbonModal/CarbonTag/states; studio + datapack
   browser follow), `updateStateClass` deletion (historical; removed by the PR #729 split), server
   view sections get the action bar, worldview
   loading-pattern notes, Loader2→CarbonSpinner/`loading` props on touched surfaces (§5 rows 2–9,
   10, 12, 13-touched-only), ServerLifecycleActions token rebuild + `bun run build:homepage`.
   This is where implementation agents for the other OSS-448 children consume the system; design-system
   artifacts must already be merged (phases 0–2) so they do not fork the idiom.
5. **Phase 4 — copy and terminology.** Terminology table, label normalization across the unified
   surfaces, empty-state copy rules, final docs pass.
6. **Phase 5 — verification gate.** Full check suite (§8), then Jira → In Review with the summary
   comment. Cloud token-snapshot propagation is a separate Cloud-repo step per the README workflow —
   record the digest change in the PR, do not attempt it here.

Rationale for the order: tokens before components before call sites mirrors dependency direction;
the docs land with each layer (the contract test enforces components; the README workflow enforces
tokens); and phase 3 starts only after the idioms exist so parallel implementation agents reuse
rather than reinvent.

## 8. Verification

- `cd web/carbon-panel && bun install --frozen-lockfile && bun run check && bun run test`
- `python -m unittest scripts.test_design_system_page` (and `scripts.test_landing_page` after the
  fragment rebuild)
- `bun run build:homepage` after any fragment/token change; confirm `landing` renders the updated
  fragments with the `:host`-rewritten tokens
- `bash scripts/web-build.sh` for the production frontend build
- Manual: both themes, both densities, reduced motion via media query **and** the stored
  `data-carbon-motion` attribute, keyboard-only pass over the action bar and mods lists, 320 px and
  200% zoom for the fixed bar's safe-area behavior

## 9. Notes and risks

- **Cloud snapshot:** `tokens.css` is consumed by Carbon Cloud through a content-pinned snapshot
  with a digest lock. Token changes here (phase 1) change the digest; Cloud sync is that repo's
  documented script, not this one. Flag it in the PR description.
- **Demo `:host` rewrite:** new theme-independent tokens must live in the `:root` block so
  `demo/build.mjs`'s `:root → :host` rewrite keeps fragments inheriting correctly; theme-scoped
  tokens stay in the theme selectors as today.
- **Do not silently widen the inventory:** only CarbonActionBar and CarbonResultCard are proposed.
  Every additional export costs a docs section, tests, and Cloud-facing contract surface — the
  README's "earn complexity" principle applies.
- **Shake restraint:** shake is for refused/blocked saves only. Using it for validation errors or
  polling failures would make it noise; the docs notes say so explicitly.
- **Landing demo purple:** `#8a3ffc` is `--chart-4` in both themes; if the deepsleep action keeps a
  purple identity, reference the token (or add a documented accent role) rather than the literal —
  decided during phase 3 with the brand ADR in mind.
