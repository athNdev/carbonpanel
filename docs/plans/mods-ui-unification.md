# Mods UI Unification — Engineering Brief

**Epic:** OSS-448 · **Children:** OSS-460 (unify the mods UI), OSS-461 (centralize state + edge cases), OSS-462 (server-view Mods & Plugin deep pass)
**Worktree:** `feat/v2.1.1-improvements` · **Date:** 2026-11-23 · **Status:** research deliverable (no code changes)

Sources read: `web/carbon-panel/src/lib/components/server-mods.svelte`, `mod-browser-dialog.svelte`, `datapack-browser-dialog.svelte`, `datapack-search.svelte`, `fragments/StudioModTable.svelte`, `server-ui/ServerTabPanel.svelte`, `carbon/*`, `src/routes/servers/[id]/+page.svelte`, `src/routes/servers/new/+page.svelte`, `src/routes/modpacks/studio/[id]/+page.svelte`, `src/lib/mod-update-ui.ts`, `src/lib/features/servers/create-content.ts`, `src/lib/features/modpacks/studio/{mod-actions,dependencies}.ts`, `src/lib/utils/request-sequence.ts`, `src/lib/api/rpc-client.ts`, `src/app.css` + `src/lib/design-system/tokens.css`, `proto/carbonpanel/v1/{mod,mod_browse}.proto`, `internal/rpc/services/mod_browse.go`, `internal/rpc/handlers/mod_search.go`, `internal/rpc/services/mod_plugins.go`, `internal/indexers/hangar/*`.

> **PR #729 split note (2026-11-27):** The reviewed mod-update apply/selection
> scope described in this brief was removed by the PR #729 split. References
> below to `src/lib/mod-update-ui.ts`, `mod-update-ui.test.ts`,
> `updateStateTagType()`, `updateOutcomeTagType()`, `server-mods-update.test.ts`,
> the staged-update review panel, the automatic update policy, the per-row
> "Stage this update" control and the staged-outcome list are historical and no
> longer describe the tree. The retained surface is the read-only #680
> installed-mod preview in
> `internal/rpc/services/mod_update_preview.go`, summarized by
> `summarizeModUpdateCheck()` in
> `web/carbon-panel/src/lib/utils/mod-update-summary.ts`, and rendered by the
> server-mods preview row with no stage, apply or policy controls. Do not
> rebuild that panel from this brief; #687 owns any future apply work.

---

## 1. Instance inventory

There are **six** mods-management surfaces (two more than the epic named): the server Mods tab, the shared mods browser dialog (two modes), the create-server wizard Add-ons step, the Modpack Studio mods tab + Add-Mods modal, the **implicit plugin UI** (same surfaces, no dedicated code), and the adjacent datapack browser/search pair.

### A. Server view — Mods tab

**File:** `web/carbon-panel/src/lib/components/server-mods.svelte` (1,091 lines) + `src/lib/mod-update-ui.ts` (pure helpers, unit-tested). *(The `mod-update-ui.ts` helper file was removed by the PR #729 split; the retained read-only preview summary lives in `src/lib/utils/mod-update-summary.ts`.)*

- **Props:** `{ server: Server; active?: boolean }`.
- **Shell:** `ServerTabPanel` (`title="Mod & Plugin Management"`, description "Manage extensions in the container `mods|plugins` directory", `chip` snippet = `CarbonTag teal` with the directory, `actions` + `toolbar` snippets).
- **Components used:** Carbon — `CarbonTag`, `CarbonInlineLoading`, `CarbonButton`, `CarbonConfirm`, `CarbonSpinner`. Ad-hoc — everything else: raw `<input type="search">` with a lucide `Search` icon, raw filter `<button>`s (All/Enabled/Disabled, `font-mono text-xs uppercase`), raw `<select>`/`<input type=datetime-local|number>` for the apply window, native `<details>` for the automatic-update policy, hand-rolled upload progress bar (`div` + inline `style="width: %"`), and hand-rolled list rows (`divide-y` divs, *not* `CarbonDataTable`).
- **Icons:** `Upload, Download, Trash2, Package, FileText, X, Boxes, Search, Check, Ban` (lucide).
- **Header actions:** `Check for updates` (secondary sm, disabled while `loading || uploading || !mods.length`), `Browse Mods` (primary sm, opens dialog), `Upload .jar` (secondary sm, spinner while uploading; hidden `file` input `accept=".jar,.zip"`).
- **Toolbar:** search (client-side over displayName/fileName/description, Escape clears) + three filter buttons with counts; upload progress row (filename, %, cancel X, byte counts via `formatBytes`).
- **Status line:** `<p role="status">` with spinner + `updateStatus` text ("Checking installed files…", "Checked N installed files. Compatible updates available for M.", staging summary, or failure text).
- **Sections stacked below toolbar:**
  1. **Staged-update review panel** (historical; removed by the PR #729 split) (only when `availableUpdates.length > 0`): apply-window select (next restart / chosen time / idle), conditional datetime/idle inputs, `Stage N selected update(s)` primary button, per-outcome `<ul>` with `CarbonTag` tone per `updateOutcomeTagType()`.
  2. **Automatic update policy** (historical; removed by the PR #729 split) in a native `<details>`: opt-in checkbox, cron, timezone, excluded projects, max per run, window, idle minutes, skip-while-running, `Save policy`, retry-when-unloaded, and a 4-col `dl` of schedule/next run/last run/outcome.
  3. **Installed rows** (`divide-y`): enable/disable toggle button (Check/Ban + Active/Disabled, green vs gray), strikethrough title when disabled, version in a bordered mono chip, metadata line (FileText + file name, size, uploaded date), read-only update-preview text (`provider · installed X · candidate Y`) + reason, description `line-clamp-1`, and a bordered action cluster (Download, Trash2 icon-only buttons). The per-row "Stage this update" checkbox and staging controls were removed by the PR #729 split; #680's read-only preview row is retained.
- **States:** loading = centered `CarbonInlineLoading "Reading mods/ directory..."`; incompatible loader = ad-hoc Package-icon panel; zero mods = ad-hoc Package-icon panel; filter miss = one-line muted text. **No error state** (see edge case E1).
- **Modal:** `ModBrowserDialog bind:open onInstalled={loadMods}`; `CarbonConfirm` for delete.
- **Load model:** manual refresh only (no polling); loads once per activation via `$effect (active && !hasLoaded)`; full re-`loadMods()` after every mutation (toggle/delete/upload/stage).

### B. Mods browser dialog (shared, two modes)

**File:** `src/lib/components/mod-browser-dialog.svelte` (1,038 lines). Used in server mode (`onInstalled`) and wizard-queue mode (`onQueue` + `queuedMods`).

- **Props:** `{ open = $bindable(false); server: Pick<Server,'id'|'mcVersion'|'modLoader'|'modpackLoader'>; onInstalled?; onQueue?; queuedMods? }`.
- **Components used:** a **mixture** — bits-ui `DialogPrimitive` + shadcn `ui/dialog`, `ui/input`, `ui/badge`, `ui/tabs`, `ui/card`, `ui/alert` **plus** Carbon `CarbonButton`/`CarbonTag`, plus bespoke chips and dropdowns. This is the least Carbon-consistent surface.
- **Search view:** header (ghost back-button when in detail view, `Boxes` icon + title, loader/MC description, right side: session-count chip (`CarbonTag green` inside a button), shadcn `Badge outline` loader, MC-version toggle pill with `✓/(any)`); platform shadcn `Tabs` (Modrinth/CurseForge); shadcn `Input` with search icon; `CarbonButton` Search with `Loader2`; interactive filter bar (removable `key:value` chips + a suggestion dropdown with static `FILTER_VOCABULARY`, hardcoded MC versions 1.21.4→1.16.5); collapsible "Added this session" summary box; results as shadcn `Card`s (icon `rounded-lg`, title/author, `line-clamp-2`, `Badge secondary` downloads, `Badge outline` server/client-side + categories (max 3), `CarbonTag green` Installed / "Added just now" / `teal` Selected), action `CarbonButton "Select Version"`.
- **Version view:** summary box (`rounded-lg border bg-muted/20`), scrollable version rows (role=button, selected = `border-primary/40 bg-primary/15`, release/secondary `Badge`, file name + size mono), dependencies panel (`border-primary/30 bg-primary/5`) with checkboxes pre-selecting `required`, `incompatible` rows non-selectable, footer `Cancel` (tertiary) + install `CarbonButton` (`Loader2` + "Installing to /mods…" — **hardcoded `/mods`**, wrong for plugin servers).
- **Empty/loading/error:** spinner panel while searching; shadcn `Alert destructive` for search error; Package-icon panel "No matching mods found" (text varies by mode); `Alert destructive "No Compatible Versions"` in version view.
- **Contracts out:** `onInstalled()` after a successful server install; `onQueue(QueuedServerMod[])` in wizard mode after `resolveWizardMods` planning.

### C. Create-server wizard — Add-ons › Mods

**File:** `src/routes/servers/new/+page.svelte` (step 4 of 5, lines ~2067–2319 + 2380–2394).

- **Shell:** wizard `nav` of `CarbonButton` tabs (`Datapacks | Mods | Modules`, `aria-pressed`, count suffixes).
- **Mods pane:** explanatory paragraph + `CarbonButton "Browse mods"` (opens the dialog in queue mode); disabled-state explanations for modpack selection / Docker runtime overrides / unsupported loader, each with a tertiary button jumping to the relevant wizard step.
- **Selection list:** plain `ul.divide-y` rows — name, `versionLabel`, "N required dependencies", `Remove` (tertiary sm).
- **Dialog mounting:** `{#key modTarget}<ModBrowserDialog server={{ id:'', modLoader, mcVersion, modpackLoader: UNSPECIFIED }} onQueue queuedMods/>{/key}` — remounts on target change.
- **Clear-on-target-change:** `$effect` compares `modSelectionTarget` to the live `modTarget` string (`loader:mcVersion:modpackId:overridden`); mismatch clears selections with an info toast. Same pattern for datapacks. This is the best-behaved stale-state guard in the codebase.
- **Install:** post-creation setup tasks — one `modBrowse.installMods` per merged file (`mergeWizardModFiles` throws on conflicting versions/duplicate filenames before any task is built). No direct RPC from the step itself.

### D. Modpack Studio — Mods tab + Add Mods modal

**File:** `src/routes/modpacks/studio/[id]/+page.svelte` (1,759 lines) + `src/lib/components/fragments/StudioModTable.svelte` + `src/lib/features/modpacks/studio/{mod-actions,dependencies}.ts` (REST adapters over `/api/v1/packwiz/...` and the legacy `/api/v1/servers/none/mods/*` routes).

- **Shell:** `CarbonTabs` workspace; mods tab is a `CarbonTile` with header (title + `CarbonTag cyan` count, actions: `CarbonSearch sm` filter, `Check Updates` tertiary with `Sparkles` **`text-amber-500`** — the only non-token color in the mods surfaces, `Add Mods` primary with `Plus`).
- **Filter bar:** `CarbonButton primary/secondary` pills with counts (All/Both/Client Only/Server Only/Pinned/Updates).
- **Batch bar** (when `selectedSlugs`): blue-tag-tinted strip (`var(--tag-blue-*)`) with Set Side ×3, Pin/Unpin, `Remove` (**danger, no confirmation**), Clear.
- **Table:** `StudioModTable` — bespoke `<table>` (not `CarbonDataTable`) with container-query responsive collapse (hides File/Platform/Status < 800px; card-grid layout < 420px). Columns: select-all checkbox, name (Package icon), file name (mono, truncate), platform `CarbonTag cyan`, side toggle (`CarbonTag` blue/green/purple inside a ghost button, both→server→client rotation), pin (`Pin`/`PinOff`), status (`CarbonTag magenta "Update Ready"` / mono "Up to date"), remove (ghost iconOnly danger).
- **Updates:** manual `Check Updates` → `fetchPackUpdates` → `updatesMap` keyed by slug; no auto refresh; not invalidated on MC-version change (a save triggers the migration tab flow instead).
- **Add Mods modal:** `CarbonModal size="4xl" hasFooter={false}`; `CarbonTabs` for source (Modrinth/CurseForge/Direct URL); `CarbonSearch` + Search button; results as `CarbonTile` rows (icon `h-10 w-10 rounded-none`, title/author, desc `line-clamp-1`, `CarbonTag cyan` downloads, `CarbonTag gray` categories (max 2)) with **three** add actions: `+ Client`, `+ Server` (tertiary) and `Add (Both)` (primary). **No version picker** — `findModVersions(...)[0]` is chosen automatically. URL tab: name/file/URL/side/pin inputs + `Add Mod from URL` ("Fetching & Hashing…").
- **States:** error = bespoke `border-l-4 border-destructive` box; loading = `CarbonInlineLoading`; empty = `CarbonEmptyState` ("Enter a search query" initially, filter-miss variant with "Browse & Add Mods" action on the tab itself).
- **Dependencies:** `installRequiredDependencies` — depth-bounded (3), visited-set dedupe, skips existing pack mods, best-effort (failures warned and swallowed), count toasted.

### E. Plugin (Hangar) support — implicit, no dedicated UI

Paper-family servers reuse every surface above; the backend (`internal/rpc/handlers/mod_search.go`) branches on `pluginLoader()` (loader content directory == `plugins`) to merge **Hangar** (`internal/indexers/hangar`) with Modrinth's `plugin` facet, and `GetModVersions` tries the Hangar index first for plugin loaders (`platform == "hangar" || pluginLoader(loader)`). The frontend consequence:

- `server-mods.svelte` already copies correctly (`plugins/` chip + description) and `getModsDirectory()` returns `plugins/` for downloads.
- **The browser dialog does not:** install button says "Installing to /mods…" unconditionally; the platform tabs offer only Modrinth/CurseForge so Hangar hits arrive unlabeled (their `platform` value renders only in the version view's `Badge`); the `side:` filter vocabulary is mod-centric; upload validation for `plugin.yml` is server-side only (no client hint).
- Upload path is shared (`ImportUploadedMod` → same handler validates jars as Bukkit/Paper plugins, per `mod_plugins.go`).

### F. Adjacent pattern — datapack browser/search

**Files:** `datapack-browser-dialog.svelte` (79 lines), `datapack-search.svelte` (155 lines). The dialog is thin: bits-ui + `ui/dialog`, injected `install(datapackId, gameVersion, indexer)` callback, per-row `installing` map. `DatapackSearch` owns search state (`query/results/searching/searched/error`) and renders shadcn `Card` rows with a caller-supplied `actions` snippet — reused verbatim by the wizard (Add/Added toggle) and the studio (via the dialog). It has **no request-sequence guard and no abort** — the same gap as the studio search.

### Props/events contracts today

| Surface | In | Out |
| --- | --- | --- |
| ServerMods | `server: Server`, `active: boolean` | renders ModBrowserDialog; `onInstalled={loadMods}` |
| ModBrowserDialog | `open` (bindable), `server` pick, `onInstalled?`, `onQueue?`, `queuedMods?` | `onInstalled()`; `onQueue(QueuedServerMod[])`; internal toasts |
| Wizard usage | binds `open`, passes pseudo-server (`id:''`), `onQueue`, `queuedMods` | `queueMods()` sets `selectedMods` + `modSelectionTarget` |
| Studio modal | `open` (bindable `searchDrawerOpen`) | direct REST calls + `loadPack()`; no events |
| DatapackBrowserDialog | `open`, `gameVersion`, `description`, `install()`, `onInstalled?` | `onInstalled()` |

---

## 2. Diff matrix

Rows = aspect; columns: **A** server tab, **B** browser dialog, **C** wizard step, **D** studio tab/modal, **E** plugin deltas (as rendered today), **F** datapack pattern.

| Aspect | A server tab | B dialog | C wizard | D studio | E plugins | F datapack |
| --- | --- | --- | --- | --- | --- | --- |
| Container | `ServerTabPanel` | bits-ui + shadcn `ui/dialog` | wizard pane | `CarbonTile` / `CarbonModal` | same as A/B | bits-ui + `ui/dialog` |
| Result container | hand-rolled `divide-y` rows | shadcn `Card` | — | bespoke `<table>` / `CarbonTile` | inherits A/B | shadcn `Card` |
| Search input | raw `<input type=search>` + icon | shadcn `Input` + icon | — | `CarbonSearch` | inherits B | shadcn `Input` + icon |
| Search trigger | client-side, instant | Enter / button | — | Enter / button | inherits B | Enter / button |
| Platform switcher | — | shadcn `Tabs` | — | `CarbonTabs` (+ URL tab) | Hangar results unlabeled | — |
| Filters | 3 count `button`s (custom) | `key:value` chips + suggestion dropdown | — | 6 `CarbonButton` pills | `side:` vocab mod-centric | — |
| Pagination | none (client filter) | none (single page) | — | none | inherits B | none (`limit:20,offset:0`) |
| Result badges | `CarbonTag` only | `Badge` (shadcn) + `CarbonTag` mixed | — | `CarbonTag` only | inherits B | `Badge` |
| Version choice | update preview (server-side) | full picker + dependency checkboxes | resolved by `resolveWizardMods` | **auto `versions[0]`** | inherits B | n/a |
| Install action | stage via `updateMod` | `installMods` (direct) | queued → setup tasks | `POST /mods` (+ auto deps) | inherits B | injected `install()` |
| Install feedback | staged-outcome list + `updateStatus` + toast | toast + session summary + row tag | setup-task list | toast chain (+dep count) | inherits B | toast + per-row spinner |
| Duplicate protection | none on toggle/delete; `installing` in dialog | `installing` flag | solver dedupe + `mergeWizardModFiles` | **none** (double-click = double POST) | inherits B | per-row `installing` map |
| Race guard | manual `requestNumber` + server-id capture | `createRequestSequence` ×2 | `{#key modTarget}` remount + target-diff effect | **none** | inherits B | **none** |
| Error UI | toast only (empty state shown on load failure) | shadcn `Alert destructive` | step text / setup task failure | `border-l-4` box / toast | inherits A/B | inline destructive box |
| Empty UI | ad-hoc Package panels | ad-hoc Package panel | explanatory text | `CarbonEmptyState` | inherits A/B | muted text lines |
| Loading UI | `CarbonInlineLoading` + `CarbonSpinner` | `Loader2` spinners | `CarbonInlineLoading` (modules) | `CarbonInlineLoading` / `Loader2` | inherits A/B | `Loader2` |
| Destructive confirm | `CarbonConfirm` | — | — | batch remove = **none** | inherits A | `CarbonConfirm` (files) |
| Icons | lucide throughout | lucide throughout | lucide | lucide + `text-amber-500` | inherits A/B | lucide |
| Tokens | Tailwind token classes only | token classes; `rounded-md/lg` normalized to 0 by `@theme` | token classes | `text-amber-500`, raw `var(--tag-blue-*)` | inherits | token classes |
| Copy for directory | correct (`mods/` vs `plugins/`) | "Installing to /mods…" hardcoded | "required dependencies" | packwiz vocabulary | dialog copy wrong | datapacks/ |

**Headline divergences:** three different result-row idioms (rows/cards/table) and four search-input idioms; Carbon/shadcn mixing only in B and F; error/empty states inconsistent (only D uses `CarbonEmptyState`/`CarbonErrorState`); guards exist in B but not D/F; per-row pending state exists in F but not A/D.

---

## 3. State management audit

### 3.1 State ownership per instance

| State | A server tab | B dialog | C wizard | D studio | F datapack |
| --- | --- | --- | --- | --- | --- |
| Storage | ~40 local `$state` runes in one file | ~20 local `$state` runes | page-level `$state` | page-level `$state` | component `$state` |
| Stores | none (tabs are prop-driven) | none | none | none | none |
| Transport | ConnectRPC (non-silent → global loading + auto-toast) | ConnectRPC (`silentCallOptions`) | ConnectRPC (silent for browse) | REST via `apiFetch` | ConnectRPC |
| Sequence guard | manual `requestNumber` (list); `policyGeneration` (policy) | `createRequestSequence()` for search + versions | none (delegated to B) | none | none |
| Stale handling | drop non-current responses; server-id capture | drop non-current; wizard path re-checks `open`/selection identity | remount + `modTarget` diff | none | none |
| Duplicate-install protection | n/a (staging is idempotent per batch) | `installing` flag | solver + file-merge | none | per-row map |
| Open/close reset | n/a | resets detail + session; **keeps** query/platform/filters/error | remount resets all | modal close keeps search state | dialog keeps `DatapackSearch` state |
| Polling | none | none | none | none | none |

### 3.2 Edge-case register

Severity: 🔴 user-visible data/misleading state, 🟡 friction/confusing, 🟢 handled correctly today.

| # | Surface | Case | Today | Sev |
| --- | --- | --- | --- | --- |
| E1 | A | **Load failure renders as success** — `loadMods` catch only toasts; `mods` stays `[]`, so the "No mods installed" empty state shows after an RPC failure. No retry affordance. | 🔴 |
| E2 | A | **Double toast on RPC error** — interceptor toasts the formatted message and marks `__rpcToasted`; `loadMods`' catch ignores `wasRpcErrorToasted` (unlike `loadPolicy`/`stageSelectedUpdates`) and toasts a second message. | 🟡 |
| E3 | A | **Toggle/delete have no per-row pending state** — `toggleMod` double-click issues two `updateMod` calls (second reverts the first); delete confirm button is not disabled during the await. | 🟡 |
| E4 | A | **Server switch mid-upload imports into the wrong server** — the server-change reset block clears flags but never calls `uploadAbortController.abort()`; the in-flight chunked upload finishes and `importUploadedMod` runs with the *new* `server.id` and the *old* file. | 🔴 |
| E5 | A | **Check-updates vs staging not mutually exclusive** — a concurrent `listMods(checkUpdates)` completing after `updateMod` staging overwrites `mods` and clobbers the `updateStatus` staging summary (and vice versa). Guarded only by last-write-wins. | 🟡 |
| E6 | A | `updateWindow === 'time'` past timestamps / timezone confusion unvalidated beyond `Number.isFinite`. (historical; apply-window UI removed by the PR #729 split) | 🟡 |
| E7 | B | **Dialog closed mid-install** — install continues (correct), toast fires, parent refresh runs, session entry is pushed after the close-reset so it re-appears on reopen. Mostly benign; the `installing` flag is *not* reset on close, so a reopen during an in-flight install shows a disabled button with no visible reason. | 🟡 |
| E8 | B | **Transitive deps pre-selected during install are invisible** — `resolveDependencyInstallItems` mutates `selectedDependencies` after install begins; the checkbox UI never reflects the additions. | 🟡 |
| E9 | B | **Provider outage mid dependency resolution silently drops deps** — unresolvable deps are `console.warn`ed and skipped; the mod installs without its required dependencies and nothing surfaces beyond a missing dep count. | 🔴 |
| E10 | B | **Partial install failure under-communicated** — `success=false` joins `errors[]` into one toast; installed items are not listed per-item (no parity with A's staged-outcome list). | 🟡 |
| E11 | B | Filter vocabulary hardcodes MC versions (1.21.4→1.16.5) that can diverge from the node's supported list. | 🟡 |
| E12 | B | Search query/platform/filters persist across close/open while results are cleared — reopening runs a fresh search with a query the user may have considered abandoned. | 🟢 (intentional?) |
| E13 | C | `{#key modTarget}` remount while the dialog is open silently re-opens a fresh dialog against the new target (open state persists through the key block). | 🟢 |
| E14 | C | Setup-task installs run one `installMods` per file; a mid-batch failure leaves earlier files installed with per-task retry, but no aggregate "N of M installed" outcome view in the step UI. | 🟡 |
| E15 | D | **No request-sequence guard in `searchMods`** — platform-tab switch or rapid Enter can let a slow earlier response overwrite newer results. | 🔴 |
| E16 | D | **No per-row adding flag** — double-clicking `Add (Both)`/`+ Server` double-POSTs the mod. | 🟡 |
| E17 | D | **Batch `Remove` is immediate and unconfirmed** (single-file deletes elsewhere use `CarbonConfirm`). | 🔴 |
| E18 | D | `updatesMap` survives MC-version/loader changes and pack reloads; the "Updates" filter and "Update Ready" badges can be stale until the next manual check. | 🟡 |
| E19 | D | Auto-picking `versions[0]` means the operator never chooses release/beta or dependency set — divergence from B and a compat foot-gun. | 🟡 |
| E20 | D | Studio search still uses the legacy REST route `/api/v1/servers/none/mods/search` (with the documented parse-before-ok quirk) while B uses the generated RPC — two transports for one operation during the deprecation window. | 🟡 |
| E21 | F | `DatapackSearch` has no sequence/abort guard (same race as E15) and fixed `limit:20, offset:0`. | 🟡 |
| E22 | A/B | Dialog open across a server prop change (server switched behind an open dialog): dialog state is not reset by prop change; `installed` flags and the target loader/MC copy go stale until a new search. | 🟡 |
| E23 | A | Policy form: cron/timezone strings sent unvalidated; save failure mid `toggleTask` leaves `loadedPolicyOptIn` out of sync until next load. | 🟡 |
| E24 | Global | Two dialogs could stack (browser dialog + `CarbonConfirm` in A, or dialog + modpack modal in C); `CarbonModal` scroll-lock refcounts, bits-ui locks independently — untested combination, focus order not verified. | 🟢 |

Handled well (keep): B's `createRequestSequence` usage; A's server-id capture + `policyGeneration`; C's `modTarget` diff-and-clear with info toast; wizard solver's cycle/version guards; `mergeWizardModFiles` duplicate guards.

---

## 4. Target state machine (OSS-461)

### 4.1 Scope split

**Shared (one store, keyed by target):** catalog search (query, platform, filters, results, sequence), result detail (versions, selected version, dependency selection), install transaction, session-install log, stale-response policy, open/close reset. Target identity = `{ kind: 'server'|'wizard'|'pack', id, loader, mcVersion }` — changing identity is a hard reset event.

**Per-instance (stays local):** installed-list + upload + update-preview/staging + policy (server tab only); wizard queue + `modTarget` reconciliation (wizard only); side/pin/batch + `updatesMap` (studio only); datapack install callback (dialog seam).

### 4.2 Proposed module

`src/lib/features/mods/mods-browser-store.svelte.ts` — a `$state` class factory instantiated per mount (not a global singleton) so server tab, wizard, and studio each own a instance; pure logic (`resolveDependencyInstallItems`, filter math) moves beside the retained `mod-update-summary.ts` as testable modules. (The removed update-panel helper file `mod-update-ui.ts` is historical; see the PR #729 split note.)

### 4.3 Machine

States (orthogonal regions):

```
Region catalog          Region detail           Region install        Region session
──────────────          ─────────────           ──────────────        ──────────────
idle                    closed                  idle                  empty
  │ OPEN                  │ SELECT_RESULT         │ INSTALL_CLICKED     │ INSTALL_OK
  ▼                       ▼                       ▼                     ▼
searching               versionsLoading         resolvingDeps          logging
  │ OK / ERR              │ OK / ERR              │ plan built          │ (append + badge)
  ▼                       ▼                       ▼
resultsReady            versionSelected         installing
  │ QUERY_CHANGED ─┐      │ VERSION_SELECTED      │ OK(partial?) / ERR
  └─► searching    │      ▼                       ▼
  │ FILTER_CHANGED │    depsReady ──────────► installing
  │ PLATFORM_CHANGED      │  INSTALL_CLICKED
  │ ERR → searchError     │  BACK/CLOSE → closed
  ▼
resultsReady
```

Flattened: `closed → searching → resultsReady ⇄ versionDetail{loading, ready} → installing → resultsReady|versionDetail` with terminal `searchError` recoverable by any new search event and `installError` recoverable by re-`INSTALL_CLICKED` (selection retained).

Events and guaranteed transitions:

| Event | Guard | Effect |
| --- | --- | --- |
| `TARGET_CHANGED` | always | full reset; abort/invalidates all sequences; flushes pending toasts of stale target |
| `OPEN` | — | `searching` unless a fresh result set for the current target exists |
| `CLOSE` | — | reset detail + install; **reset `installing`** (fixes E7); per product decision keep or clear query (E12 — recommend keep, but re-run search on open) |
| `SUBMIT_SEARCH` / `PLATFORM_CHANGED` / `FILTER_ADDED` / `FILTER_REMOVED` | — | `searchSeq.next()`; clear results; drop non-current responses (existing pattern) |
| `SELECT_RESULT` | not `installing` | `versionsSeq.next()`; reset version + dependency selection |
| `SELECT_VERSION` | — | reset deps; pre-select `required` |
| `TOGGLE_DEPENDENCY` | not `required`-locked | update selection map |
| `INSTALL_CLICKED` | mode dispatch: `server` → `installMods` per-item outcome list; `wizard` → `resolveWizardMods` then `onQueue` (identity re-check before callback); `pack` → out of scope for this store | 
| `INSTALL_RESOLVED` | — | append session entries incl. **skipped deps** (fixes E9); emit `onInstalled`; render per-item outcomes |
| `DEPENDENCY_UNRESOLVABLE` | — | collect into outcome list, never silently drop (fixes E9) |

Behaviors intentionally **not** in the shared store: check-updates/staging (A), upload (A), policy (A), queue reconciliation (C), side/pin/batch (D). The A-side list/preview/staging flow keeps `requestNumber` but moves into its own `installed-mods` store with the same event style so E5 becomes explicit: `checking` and `staging` are mutually exclusive states (a stage request forces `checking → idle` or is queued behind it).

### 4.4 Edge-case coverage map

E1 → `listError` region + `CarbonErrorState`; E2 → single toast site in store; E3 → per-row `pending` set; E4 → `TARGET_CHANGED` aborts upload controller; E5 → exclusive checking/staging; E7 → `CLOSE` resets `installing`; E8/E9 → install outcome list incl. skipped deps; E10 → same outcome list; E15/E21 → shared `createRequestSequence` + optional `AbortSignal`; E16 → per-row `adding` map; E18 → `updatesMap` invalidated on `TARGET_CHANGED`/save; E22 → store keyed by target identity resets on prop change.

Test seams: extend the existing patterns — `mod-update-summary.test.ts` (retained read-only preview summary) for new pure modules; a store test instantiating the machine with an injected `VersionLookup` (as `create-content.ts` already does) exercising E5/E7/E9/E15. The removed `mod-update-ui.test.ts` is not a seam.

---

## 5. Design pass brief — server view "Mods & Plugin management" (OSS-462)

### 5.1 Information architecture (top → bottom inside `ServerTabPanel`)

1. **Header** (unchanged idiom): title, description, `chip` = `CarbonTag teal` directory (`mods/` or `plugins/`), actions = `Check for updates` (secondary), `Browse & Install` (primary, opens dialog), `Upload .jar` (secondary). Keep exact `ServerTabPanel` snippets so spacing/motion match Backups/Tasks.
2. **Status strip** (always rendered, `role="status"`): spinner + one-line outcome of the last operation (checked N/M, staged X refused Y, upload progress inline here). Replaces today's floating `<p>` and the separate upload bar — one place the eye checks after any action.
3. **Pending updates card** (historical; removed by the PR #729 split; the read-only preview row is retained instead) (conditional, `availableUpdates > 0`): `CarbonDataTable`-style header "Compatible updates (N)" + selection checkboxes + apply-window controls + `Stage selected` + outcome list. Isolating it above the list stops the update UI from interleaving with rows.
4. **Installed content table** — the main change: replace hand-rolled rows with **`CarbonDataTable`** (matches `PlayerRoster`, `TemplateManagement`, library/modpacks lists). Columns: *Name* (display name + disabled strikethrough + version chip), *Status* (enable/disable as a `CarbonTag`-style toggle or `CarbonButton ghost` with Check/Ban — keep the existing tag visual), *File* (mono, truncate), *Size*, *Updated*, *Update* (read-only preview state and reason; the removed `updateStateTagType` mapping was deleted by the PR #729 split), *Actions* (Download, Delete iconOnly with ≥32px hit areas). Use the container-query collapse pattern from `StudioModTable` for <800px (hide File/Size/Updated) and the card-grid fallback <420px.
5. **Automatic update policy** — swap native `<details>` for `CarbonAccordion`/`CarbonAccordionItem` (already in the design system) with the same fields and the `dl` summary moved into the collapsed header (schedule · next run · last outcome).
6. **Empty/error/loading** — `CarbonEmptyState` for incompatible loader ("This server type doesn't load mods or plugins") and for zero mods (action = open browser); `CarbonErrorState` for load failure with retry (fixes E1); `CarbonInlineLoading` for first load; one-line muted text for filter misses.

Density: keep current `text-xs`/mono metadata styling (it matches Backups); row padding `py-2.5–3`; do not introduce zebra stripes (divide-y is the app idiom).

### 5.2 Mods vs plugins differentiation

No separate tab (backend treats them as one directory). Differentiate by copy and badges only: directory chip and description already switch on loader; add a `Plugin`/`Mod` kind badge on rows once `InspectModSide` classification is surfaced by `ListMods` (proto addition, backend already classifies uploads); dialog copy switches to "Installing to plugins/…" for plugin loaders (fixes the hardcoded string); hide `side:` client-only suggestions for plugin searches (Hangar has no sides) and surface `platform: hangar` as a first-class badge.

### 5.3 Browser dialog relationship

The dialog stays the acquisition surface; the tab stays the management surface. Contract changes: `onInstalled` becomes `onCompleted(outcome)` carrying the per-item outcome list so the tab can render the same outcome rows as staging (one feedback idiom); the dialog adopts the tab's `CarbonTag` badge vocabulary (drop shadcn `Badge`/`Card`/`Alert` inside it) per §6.

### 5.4 Mobile behavior

`ServerTabPanel` is already fluid. Specifics: actions row wraps (`flex-wrap`); table columns collapse per §5.1.4; the dialog becomes effectively fullscreen at <640px (`max-h-[90vh]` already holds) with the filter bar collapsing to a single removable-chip row + "+ Add filter" disclosure; upload progress stays in the status strip.

### 5.5 Token & copy audit (OSS-460 feed-in)

- Replace `text-amber-500` (studio Check Updates sparkles) with a token (`--warning`/`--chart-3`).
- No raw hexes found in any mods surface — good.
- `rounded-md/lg` instances are visually neutralized by `@theme { --radius-*: 0px }`, but the classes should still read `rounded-none` in mods surfaces for greppable consistency.
- Kill the mixed shadcn/Carbon imports inside B and F in favor of the shared components in §6.
- Copy: unify "Browse Mods"/"Browse mods"/"Browse & Add Mods" → one label per context ("Browse & install"); unify "No mods installed"/"No matching mods found"/"No mods match current filter" via `CarbonEmptyState` props.

---

## 6. Shared component extraction plan

New home: `web/carbon-panel/src/lib/components/mods/` (+ store in `src/lib/features/mods/`).

| Component | Replaces | Props (draft) |
| --- | --- | --- |
| `ModsCatalogSearchBar.svelte` | B's tabs+input+button, D's modal search bar, F's `DatapackSearch` bar | `{ platforms: {id,label}[]; platform (bindable); query (bindable); searching; onsubmit(); size? }` |
| `ModsCatalogResultCard.svelte` | B's `Card` rows, D's `CarbonTile` rows | `{ result: CatalogModResult; state: 'idle'\|'installed'\|'queued'\|'added'; badges?: Snippet; children (actions) }` — Carbon only (`CarbonTile` + `CarbonTag`), icon fallback included |
| `ModsFilterChips.svelte` | B's activeFilters chips + suggestion input | `{ filters: ActiveFilter[]; vocabulary: FilterVocab[]; onadd(label); onremove(key) }` |
| `ModsVersionPicker.svelte` | B's version list + dependency panel | `{ versions: CatalogVersion[]; selectedId (bindable); dependencySelection (bindable); disabledDeps?: Set<string>; busy? }` |
| `ModsBrowserDialog.svelte` | current `mod-browser-dialog.svelte` | same external contract (`open`, `server`, `onCompleted(outcome: InstallOutcome[])`, `onQueue?`, `queuedMods?`) so A/C call-sites barely move |
| `ModsInstallOutcomeList.svelte` | A's staged-outcome `ul` (historical; removed by the PR #729 split); reused for dialog install outcomes | `{ rows: {label, tone, detail}[] }` (historical `updateOutcomeTagType`; removed) |
| `InstalledModsTable.svelte` | A's hand-rolled rows | `{ mods; searchQuery (bindable); filter (bindable); busyIds; ontoggle; ondelete; ondownload; snippet updateCell? }` — `CarbonDataTable` + container queries |
| `ModUploadProgress.svelte` | A's ad-hoc progress bar | `{ progress: UploadProgress; filename; oncancel }` |
| `ModUpdatePolicyCard.svelte` | A's `<details>` policy | `{ state (bindable fields or one object), onLoad, onSave, ready }` — wraps `CarbonAccordionItem` |

Keep the retained `mod-update-summary.ts` as the read-only preview summary source. (The original brief's `mod-update-ui.ts` tone/label source of truth was removed by the PR #729 split.) New pure modules (`filter-vocabulary.ts` fed by server data instead of hardcoded versions, `dependency-plan.ts` for E9 outcome capture) sit beside the retained helper with mirrored tests.

---

## 7. Migration sequencing (proposal)

1. **Pure groundwork (no visual change):** extract `resolveDependencyInstallItems` + outcome capture (E8/E9) and the search/versions sequences into `features/mods/`; store tests for the machine (OSS-461 core).
2. **Shared leaf components:** `ModsCatalogSearchBar`, `ModsCatalogResultCard`, `ModsFilterChips`, `ModsVersionPicker`, `ModsInstallOutcomeList`; re-skin B onto them (Carbon-only), fix the `/mods…` copy and per-item outcomes.
3. **Server tab deep pass (OSS-462):** `InstalledModsTable`, `ModUpdatePolicyCard` (historical; policy UI removed by the PR #729 split), `ModUploadProgress`, status strip, `CarbonErrorState`/`CarbonEmptyState`, upload abort on server change (E4), row pending states (E3), `wasRpcErrorToasted` (E2).
4. **Convergence:** point D's Add-Mods modal at the shared search bar/result card, add sequence guard + per-row adding (E15/E16), confirm-on-batch-remove (E17), consider switching its transport to the RPC client (E20) — flag as a separate decision with the deprecation window.
5. **Adjacent:** give `DatapackSearch` the shared bar + sequence guard (E21).

Verification per repo norms: `bun run check && bun run test` for the web surface, plus `go test ./...` untouched-but-green; capture before/after screenshots of the server tab and dialog for the PR; record head + results in the merge-gate receipt.

---

## Appendix: exact touch points

| Concern | File |
| --- | --- |
| Server tab rows/states | `web/carbon-panel/src/lib/components/server-mods.svelte` |
| Update/policy helpers + tones | `web/carbon-panel/src/lib/utils/mod-update-summary.ts` (retained read-only preview summary; `mod-update-ui.ts`, its test and the apply/policy helpers were removed by the PR #729 split) |
| Browser dialog | `web/carbon-panel/src/lib/components/mod-browser-dialog.svelte` |
| Wizard step + queue | `web/carbon-panel/src/routes/servers/new/+page.svelte` |
| Solver + merge guards | `web/carbon-panel/src/lib/features/servers/create-content.ts` |
| Studio tab/modal | `web/carbon-panel/src/routes/modpacks/studio/[id]/+page.svelte` |
| Studio table | `web/carbon-panel/src/lib/components/fragments/StudioModTable.svelte` |
| Studio REST adapters | `web/carbon-panel/src/lib/features/modpacks/studio/{mod-actions,dependencies,pack-maintenance}.ts` |
| Datapack pattern | `web/carbon-panel/src/lib/components/{datapack-browser-dialog,datapack-search}.svelte` |
| Sequence utility | `web/carbon-panel/src/lib/utils/request-sequence.ts` |
| Toast/silent contract | `web/carbon-panel/src/lib/api/rpc-client.ts` |
| Contracts | `proto/carbonpanel/v1/mod.proto`, `proto/carbonpanel/v1/mod_browse.proto` |
| Backend plugin/Hangar merge | `internal/rpc/handlers/mod_search.go`, `internal/rpc/services/mod_browse.go`, `internal/indexers/hangar/` |
| Design tokens | `web/carbon-panel/src/lib/design-system/tokens.css`, `web/carbon-panel/src/app.css` (`@theme`) |
