# Research: live terrain in World View (OSS-463) and floating save/undo bar (OSS-464)

Status: research brief for epic OSS-448 children. No code changed to produce this
document. Facts below were read from the worktree at
`feat/v2.1.1-improvements` (Nov 2026); file paths are cited inline.

---

## Part A — Load live world terrain in World View across modloaders and versions (OSS-463)

### A1. How the native terrain renderer works today

The console has a **Go-side, self-contained Anvil reader**. There is no Java-side
exporter on the native path; BlueMap (the optional managed renderer) is the
separate Java-renderer path (ADR 0015/0040/0048, `docs/world-viewing.md`,
`docs/world-terrain.md`).

**Pipeline (backend):**

1. `internal/worldview/service.go` — `WorldViewService` (Connect RPCs in
   `proto/carbonpanel/v1/worldview.proto`). `GetWorldViewStatus` /
   `ListWorldViewTargets` project a `WorldViewTarget`; with no configured
   renderer endpoint (`ErrRendererUnavailable`) they fall back to the native
   map and report `RENDERER_TYPE_NATIVE` + `RENDERER_STATE_NATIVE_AVAILABLE`.
   Capabilities: `map.2d` and `markers` supported; `dimensions`, `map.3d`,
   `players`, etc. explicitly unsupported (`nativeCapabilities()`).
2. `internal/worldview/native_scope.go` — `resolveNativeScope` builds a
   `nativeScope{serverID, dataRoot, level, node, nodeGeneration, bindingID,
   revisionID, bindingStamp, restoreID}` from persisted server/node/config data
   (never from the map URL). The level is `cfg.ActualLevelName()`
   (`internal/db/server_level.go`: `level-name` from custom
   `server.properties`, else panel Level, else `"world"`; single path
   component only). Lifecycle operations (restore/swap) mark the scope busy and
   refuse reads; restore IDs invalidate old sessions.
3. `internal/worldview/native_map.go` — `NativeMap` serves two asset kinds on
   the same-origin proxy (issued by `GetWorldViewSession` as
   `/api/v1/worldview/map/{key}/`):
   - `native/manifest.json` → `{tileBlocks: 128, dimension: "overworld", spawn}`
     derived from `level.dat` via `worldlib.LevelDataFacts` (DataVersion check
     + SpawnX/Z or modern `spawn.pos`).
   - `native/overworld/{x}/{z}.png` → one 128×128-block top-down tile
     (`parseNativeTileAsset` accepts only canonical decimal tile coordinates).
   Concurrency: **2 worker slots** (`nativeWorkSlots`), **5 s deadline** per
   request; 429 + `Retry-After: 1` when busy. Scope re-resolved per asset
   request; mismatch ⇒ HTTP 409 ("world changed; reopen the map").
4. `internal/worldterrain/` — the actual Anvil reader:
   - `tile.go` `RenderOverworldTile(ctx, store, level, coord)` opens
     `<level>/region/r.<rx>.<rz>.mca` through the server's **confined,
     read-only node storage accessor** (`nodestorage.Resolver.ForNode`,
     `internal/nodestorage/accessor.go` — `os.Root`-confined local accessor;
     `Stat`/`ReadDir` exist but the native path currently only uses `OpenRead`).
   - Reads the 2-sector header (1024 packed locations + 1024 timestamps),
     then only the 64 sectors needed for the tile's 8×8 chunks
     (`tilesPerRegion = 4`, i.e. tiles align inside a 32×32-chunk region).
   - Re-reads the header after decoding; if the tile's entries changed during
     the read it fails with `ErrRegionChanged` (surfaced as 503 +
     `Retry-After: 1`, retried by the client in place).
   - Budgets: 512 KiB compressed / 2 MiB decompressed per chunk; 8 MiB
     compressed / 24 MiB decompressed per tile; context-cancellable reads.
   - `region.go` decodes gzip (1), zlib (2) and uncompressed (3) chunk records;
     rejects compression byte ≥ 4 and the 0x80 external-payload flag (`.mcc`).
5. `internal/worldlib/chunk_surface.go` `ParseJavaChunkSurface` — bounded NBT
   decode of one chunk:
   - Requires `DataVersion` ∈ **[2860 (1.18), 4554 (1.21.9)]**
     (`SupportsJavaOverworldDataVersion`); outside ⇒ `ErrJavaChunkUnsupported`.
   - Requires `yPos == -4` (standard vanilla-height Overworld layout).
   - `Status` must be `full` (rendered) or one of
     `surface|carvers|liquid_carvers|features|initialize_light|light|spawn|heightmaps`
     (rendered **provisional**, drawn faded). Earlier stages ⇒
     `ErrJavaChunkUnfinished` (counted as missing).
   - Reads `Heightmaps.WORLD_SURFACE` (falls back to `WORLD_SURFACE_WG` for
     provisional chunks), 9-bit packed, 7 values per long, no long straddling.
   - Walks `sections[].block_states` palettes to resolve the surface block per
     column (plus up to 10 blocks down for water depth).
6. `tile.go` shading: authored palette keyed on block names (`minecraft:`
   prefix only — modded blocks fall back to elevation colors), slope shading,
   water depth tinting, provisional alpha 176. Output is a PNG
   (`png.BestSpeed`), ~1 px per block.

**Transport + caching (frontend):**

- `web/carbon-panel/src/lib/components/worldview/NativeMapViewport.svelte` —
  canvas-free `<img>` tile grid; pan/zoom/coordinate jump; status line
  ("Latest chunk save in this view", provisional note, empty-save hint,
  Chunky pregeneration link); **Refresh terrain** button.
- `src/lib/features/worldview/native-map.ts` — manifest/tile fetch over the
  session root with `cache: 'no-store'`; failure taxonomy
  `busy|changed|unsupported|unavailable|access` mapped from HTTP status;
  tile headers `X-CarbonPanel-Rendered-Chunks`,
  `X-CarbonPanel-Provisional-Chunks`, `X-CarbonPanel-Tile-Saved-At`.
- `src/lib/features/worldview/native-map-loader.ts` — client tile cache:
  `MAX_ACTIVE_REQUESTS = 2`, `MAX_CACHED_TILES = 96`,
  `RECHECK_DELAY_MS = 30_000`. Incomplete/empty/failed tiles recheck every
  30 s; **fully-rendered tiles are cached with `recheckAt = Infinity`** — they
  never refresh until the user presses Refresh terrain or the viewport
  remounts. The proxy itself sets `private, no-store` on everything
  (`internal/worldview/proxy.go`), so there is **no server-side tile cache** —
  every tile request re-reads and re-renders from disk.
- Preview tile sampling around spawn: `native-map-preview.ts`; workspace state
  machine: `features/worldview/state.ts` (`NATIVE_AVAILABLE` → `'saved'`).

**What is rendered today, in one sentence:** saved-to-disk Java **Overworld**
terrain from the server's *live* world directory (the panel's storage path),
read fresh per tile request through the confined accessor, rendered to
top-down palette PNGs, cached only in the browser with a 30 s recheck for
gaps and no automatic refresh for complete tiles.

### A2. What "live terrain" actually requires

Important framing: the renderer **already reads the live world directory**.
"Liveness" today is limited by (a) which saves/dimensions it can parse and
(b) the refresh model. Closing OSS-463 therefore decomposes into:

1. **Freshness model** (small, high value): let complete visible tiles
   re-validate on a cadence and after world saves, instead of `Infinity`.
   - Cheap server signal: the region *timestamp sector* already yields per-tile
     `NewestSavedAt`; `Accessor.Stat` gives region file mtime/size. Add a
     custom `X-CarbonPanel-Region-Modified` (mtime+size) header on tile
     responses (proxy strips standard validators but not custom X- headers),
     or a tiny `native/freshness.json` manifest extension that Stats the
     visible regions. Client: on TTL expiry, refetch and keep the old image
     until the new one arrives; skip re-render server-side by comparing the
     header snapshot (optionally with a small server-side PNG LRU keyed by
     `(scope, tile, headerHash)` if re-render cost matters).
   - Minecraft save cadence: vanilla autosave every 5 min, chunk unloads save
     continuously, `save-all flush` forces. A 60–120 s live TTL is within one
     save cycle; the 30 s gap-recheck already exists.
2. **Version widening** (the "as many versions as the region format allows"
   ask): raise the DataVersion ceiling from 4554 (1.21.9) to cover
   1.21.10/1.21.11 and the 2026 releases (26.1 = 4786, 26.2 = 4903,
   26.3 = 5023; Java moved to year-based naming after 1.21.11). The fields
   this parser reads (yPos, Status, WORLD_SURFACE 9-bit packing, section
   block_states palettes) have been stable since 1.18; each new release must
   be **verified against game-generated saves** using the existing harness
   (`docs/world-terrain.md` §Qualification: `CARBON_TERRAIN_FIXTURE_ROOT` /
   `CARBON_TERRAIN_FIXTURE_LEVEL`,
   `go test ./internal/worldterrain -run TestRenderOverworldTileGameSave`).
   The ceiling is itself the fail-closed guard: unknown future formats stay
   rejected rather than misrendered.
3. **LZ4 chunk compression** (1.20.5+ optional `region-file-compression=lz4`,
   compression byte 4): currently rejected per-chunk ⇒ tiles with only LZ4
   chunks report `unsupported`. Needs an lz4-java **block-stream** decoder
   (LZ4BlockOutputStream framing with XXH32 checksums — not the LZ4 frame
   format), matching the existing budget/cancel machinery. Note the panel's
   own world config schema only offers `DEFLATE`/`NONE`
   (`internal/worldlib/config.go` `region-file-compression`), so panel-managed
   servers are safe today; imported/custom servers may have LZ4 on.
4. **Dimensions** (Nether `DIM-1`, End `DIM1`, modded
   `dimensions/<namespace>/<path>`):
   - Requires parameterizing the fixed Overworld geometry
     (`JavaOverworldMinY/MaxY`, `yPos == -4`, 9-bit 37-long WORLD_SURFACE)
     into per-dimension spans: Nether 0–127, End 0–255 in modern versions;
     **modded dimension types may declare any `min_y`/`height`** (multiples
     of 16, up to 4064), so the parser must read the dimension type's height
     from `level.dat`/dimension registries or refuse non-vanilla spans with
     the existing `unsupported` taxonomy. Roof layers (Nether bedrock roof),
     no-sky lighting and a second palette are new rendering concerns.
   - Discovery is feasible today: `Accessor.ReadDir` + `level.dat` presence
     identifies candidate level roots (sibling Bukkit worlds, `DIM-1`,
     `DIM1`, `dimensions/*/*`). The manifest would grow a `dimensions` list
     (id, kind, span, tile root); the frontend gets a dimension picker gated
     on the `dimensions` capability (currently advertised unsupported — flip
     per-map instead of per-renderer or keep it native-internal).
5. **Multi-world (Bukkit/Spigot/Paper/Folia)**: `level-name` resolves only the
   main world. Sibling worlds (`world_nether`, `world_the_end`, custom) are
   separate top-level folders; mapping them to dimensions is by Bukkit
   convention only (`bukkit.yml`, folder suffixes). Phase-later: list sibling
   folders that contain `level.dat` + `region/` and expose them as
   server-local worlds in the WorldRef chooser, labelled with the folder name
   (identity stays server-local kind + level name; the existing WorldRef
   rules in `native_scope.go` already support explicit level names).

### A3. Format constraint details (what breaks outside support)

| Constraint | Today's behavior | Breaks when |
|---|---|---|
| DataVersion window 2860–4554 | `readNativeLevel` fails the whole map with `ErrTerrainUnsupported` → status `UNSUPPORTED`, honest message | 1.13–1.17 worlds (below floor), 1.21.10+ / 26.x worlds (above ceiling) |
| Overworld-only geometry (`yPos == -4`, 384-block span) | per-chunk `ErrJavaChunkUnsupported` | Nether/End/modded dimensions with other spans |
| Chunk Status `full` or ≥ `surface` | rendered or faded-provisional | pre-`surface` stages (e.g. freshly started noise) count as missing; `empty` chunks transparent |
| Compression 1/2/3 | decoded | LZ4 (byte 4, 1.20.5+ option) → chunk unsupported; `.mcc` external chunks (0x80 flag) unsupported |
| WORLD_SURFACE 9-bit × 37-long packing | heightmap decode | none within 1.18+ (stable); pre-1.16 packing variants differ (below floor anyway) |
| `minecraft:` block palette | colored; unknown → elevation fallback | modded blocks render, just palette-neutral (acceptable degradation, not a failure) |
| Region header churn | `ErrRegionChanged` → 503 Retry-After, client retries | live saves during reads (expected, handled) |

Save-version bands (Anvil `.mca` lineage): 1.2–1.12 Anvil with pre-flattening
chunk NBT (`Level` compound, byte arrays, no palettes) — fundamentally
different decoder; 1.13–1.17 paletted sections but 256-height, pre-1.18
section indexing and pre-1.16 heightmap packing variants — needs a legacy
decode branch; 1.18+ modern flat chunk struct — the one supported today.

### A4. Where the pipeline plugs; minimal-change architecture

The renderer already parses Anvil in Go; **no new transport is needed**. The
minimal-change shape:

- **Keep** the same-origin proxy assets (`native/manifest.json`,
  `native/overworld/{x}/{z}.png`); extend the manifest
  (`newestSavedAt`, optional `dimensions` list) and add at most one cheap
  freshness asset or header. RPC surface (`worldview.proto`) needs **no
  breaking change** — sessions, scopes and capability gating all already
  exist; a `dimension` field on tile paths (`native/{dim}/{x}/{z}.png`) is
  additive and back-compatible (`overworld` keeps the current prefix or gains
  an alias).
- **File-watch vs on-demand**: on-demand reads win. The panel is
  multi-node; remote nodes have no watch channel and `Accessor` is
  read-only-confined. Region-header timestamps already provide exact
  invalidation per tile; world-save cadence (5 min autosave + unload saves)
  makes a 60–120 s TTL + manual refresh feel live without a daemon.
  `WorldViewOperationState` already suspends reads during lifecycle ops.
- **Worker budget**: keep 2 slots/5 s deadline per controller for v1; a
  server-side PNG LRU (header-hash keyed) is the first lever if real worlds
  show re-render pressure; only then consider raising slots.
- **UI needs**: a **Live** toggle (default on when the server is running;
  when off, behaves exactly like today), a staleness indicator (the existing
  "Latest chunk save in this view" line gains an age, e.g. "saved 3 min ago"),
  and the existing Refresh terrain button remains the manual escape hatch.
  No new failure states — reuse `busy|changed|unsupported|unavailable|access`.

### A5. Compatibility matrix

"Supported" = the native renderer can serve this world's Overworld today.
"Conditional" = reachable with identified work. Reason column is the binding
constraint, not the loader itself — the loader only matters through the save
format and folder layout it produces.

| Modloader family | 1.12− (DV < 1519) | 1.13–1.17 (1519–2730) | 1.18–1.20.4 (2860–3700) | 1.20.5–1.21.9 (3839–4554) | 1.21.10–1.21.11 + 26.1–26.3 (4555–5023) |
|---|---|---|---|---|---|
| Vanilla | Unsupported — pre-flattening chunk NBT | Unsupported — 256-height, legacy heightmap packing; needs legacy decoder | **Supported** | **Supported** (DEFLATE/NONE) · Conditional — LZ4 worlds need lz4 decode | Conditional — rejected by DV ceiling; format likely unchanged, verify with game-save fixtures then raise ceiling |
| Fabric / Quilt | same | same | **Supported** — vanilla Anvil | **Supported**; modded `dimensions/*/*` dirs appear (vanilla-height dims renderable once dimension support lands) | Conditional (ceiling) |
| Forge / NeoForge | same | same | **Supported**; custom dims with non-vanilla spans stay unsupported per-tile | **Supported** + same dimension caveat | Conditional (ceiling) |
| Modpacks (any loader) | same | same | **Supported** unless the pack changes `level-name` or adds dimension types (then: resolved level only; non-vanilla spans per-tile unsupported) | **Supported** | Conditional (ceiling) |
| Paper / Purpur / Folia | same | same | **Supported** — Anvil retained (Paper's SectorFile is a proposal, not shipped) | **Supported** | Conditional (ceiling) |
| Spigot / Bukkit | same | same | **Supported** for the `level-name` world; multi-world siblings (`world_nether`, …) not listed today — needs sibling-folder discovery | **Supported** + same multi-world gap | Conditional (ceiling) |
| Bedrock | Unsupported — not an Anvil save (all bands) | — | — | — | — |

Blanket fallback for every unsupported case already exists and must be kept:
the world shows the honest `unsupported` state
("The active world is not in a supported Java Overworld save format."),
with BlueMap/static adapters remaining the alternative routes.

### A6. Staged implementation plan (riskiest first) and fallback story

1. **Stage 1 — verify + widen the version ceiling (riskiest: silent
   misrender risk).** Collect game-generated saves per release (1.21.10,
   1.21.11, 26.1, 26.2, 26.3) via the `CARBON_TERRAIN_FIXTURE_*` harness;
   assert surface heightmap + palette parsing; raise `lastSupportedJava`
   release-by-release behind fixture evidence. Regression coverage at the
   tile seam (`RenderOverworldTile`) per release. *Fallback:* keep the
   ceiling at the last verified release; unsupported message unchanged.
2. **Stage 2 — live freshness loop (low risk, high value).** Tile
   `X-CarbonPanel-Region-Modified` header (or freshness manifest) + client
   TTL revalidation for complete tiles + Live toggle + saved-age indicator.
   Server behavior for legacy clients is unchanged (extra header only).
   *Fallback:* toggle off = today's behavior.
3. **Stage 3 — LZ4 chunk decode.** lz4-java block framing decoder with the
   existing budgets/cancellation; per-chunk feature detection means mixed
   regions still render. *Fallback:* LZ4 chunks keep counting as
   `unsupported` (tile renders if any zlib/gzip chunks exist).
4. **Stage 4 — dimensions (Nether/End, then vanilla-height modded dims).**
   Parameterize spans, add per-dimension manifest + tile path, dimension
   picker, per-dimension palettes (roof/lava sea). *Fallback:* Overworld-only
   map with the `dimensions` capability still reported unsupported.
5. **Stage 5 — multi-world discovery (Bukkit siblings) and modded dimension
   spans**; requires the read-only confined accessor's `ReadDir`/`Stat` only.
   *Fallback:* single `level-name` world as today.

Non-goals (explicit): Bedrock saves; writing to worlds; following `.mcc`
external chunks (may revisit if real modded saves require it); entity/POI
layers; live player positions (out of scope per ADR 0015 honesty rules).

---

## Part B — Float save/undo actions and gate state changes on unsaved edits (OSS-464)

### B1. Inventory: server-view sections with editable config + save action

All inside `routes/servers/[id]/+page.svelte` tabs; header lifecycle actions
live in the same page.

| Section (component) | Form model | Save RPC | Dirty tracking today | Notes |
|---|---|---|---|---|
| Overview → Server Settings (`server-settings.svelte`) | `UpdateServerRequest` in `$state` (`formData`) | `server.updateServer` | **Yes** — `isDirty` derived, field-by-field vs the live `server` store value | Save button disabled unless dirty; toast "Server settings updated. Restart the server to apply changes." No undo button. Migration is a separate confirm flow |
| Configuration (`server-configuration.svelte`) | `originalValues/currentValues` maps + enabled sets | `config.updateServerConfig` with `stage:true` + apply window (`NEXT_RESTART`/`AT_TIME`/`IDLE`), or `stage:false` via **Apply now** (recreates container) | **Yes** — `modifiedFields` set, "N unsaved changes" chip, `Unsaved (N)` filter | `handleReset()` reverts to loaded snapshot (this is today's "undo"); staged server-side items listed in `UpdateBatch` with per-item `discardUpdateBatch` |
| Routing (`server-routing.svelte`) | single `hostname` vs `originalHostname` | `proxy.updateServerRouting` | **Partial** (hostname comparison exists; no dirty chip) | Immediate effect; conflicts surfaced inline |
| Tasks (`server-tasks.svelte`) | per-dialog task form | `task.updateTask` / `createTask` / `toggleTask` / `deleteTask` | Dialog-local | Not window-level config; leave out of v1 bar or treat as dialog-scoped only |
| Mods (`server-mods.svelte`), Datapacks (`server-datapacks.svelte`) | action-based (install/update/delete), `mod.updateMod` for per-mod fields | `mod.*`, `datapack.*` | None | Operation-style, not form-style; out of v1 bar scope |
| Backups (`server-backups.svelte`) | none | `backup.restoreBackup` etc. | — | State-changing but not a form; candidate for a later warning-only integration |
| World (`server-ui/ServerWorld.svelte`) | none | journaled world ops (swap/compat) | — | Out of bar scope |
| Modules (`server-ui/ServerModules.svelte`, `ModuleDialog.svelte`) | module forms | `module.updateModule`, `start/stopModule` | Dialog-local | Module state changes, not server config; out of v1 bar scope |
| Players / Console / Activity | commands and moderation actions | `server.sendCommand`, player ops | — | Out of scope (console can stop the server, but gating chat commands on config dirtiness would be wrong) |

The v1 bar therefore tracks **Server Settings**, **Server Configuration** and
**Routing** (the sections where a save action over a form exists).

### B2. Dirty-state design per section

- Settings: replace the live-store comparison with a **captured baseline
  snapshot** (`structuredClone`/JSON of `formData` at load and after each
  successful save) so background inventory polls don't interact with dirtiness.
- Configuration: keep `originalValues/currentValues`; expose
  `hasChanges`/`modifiedFields` upward instead of only rendering a chip.
- Routing: promote `hostname !== originalHostname` to the same contract.
- Shared contract: a Svelte-5 context/store registry
  (`dirtySections`), where each section registers
  `{ id, label, isDirty: () => boolean, save: () => Promise<void>, undo: () => void, saving: () => boolean }`.
  The server page renders one floating bar from the registry; sections keep
  full ownership of their form logic (matches the existing `onSave` prop
  pattern in `server-configuration.svelte`).
- Undo semantics per section = **revert to the loaded snapshot** (pure
  client-side; no RPC):
  - Settings: rebuild `formData` from the baseline snapshot.
  - Configuration: existing `handleReset()`.
  - Routing: `hostname = originalHostname`.
  - (Server-side staged records are a separate concept: they are already
    discardable via `UpdateBatch` → `config.discardUpdateBatch`; the bar must
    not conflate unsaved client edits with staged server records, but its
    toast copy should mention staged items exist when a batch is open.)

### B3. Why gating matters here: the start/state-change surface

State-change entry points on the server page:

- `ServerLifecycleActions` (Start / Stop / Restart / Wake / Resume) and the
  overflow menu **Force Recreate** + **Delete Server** →
  `handleServerAction` → `serverActions.perform(id, action)`
  (`lib/features/servers/actions.ts`) → `server.startServer/stopServer/
  restartServer/recreateServer`.
- Configuration's **Apply now** (a save that recreates the container — its own
  gate is "no unsaved changes" because it *is* the save).
- Backup restore, world swap (journaled ops), module start/stop.

The staged-config rules that make unsaved edits dangerous:

1. `StartServer`/`RestartServer` call `staging.CommitNextRestartFiles`
   (`internal/staging/apply.go:194`) — any **next-restart batch** (staged
   files and module specs) is copied onto the live tree at start, then
   `scheduler.ApplyOnRestartStagedConfigRecords` applies staged config values
   and `FinishNextRestartBatch` closes the batch. So Start is *not* a neutral
   action: it commits previously staged work while the user's current unsaved
   form edits remain purely client-side and would be lost on the next
   reload/navigation.
2. Time/idle windows are *not* consumed by an incidental restart
   (`CommitNextRestartFiles` leaves `AT_TIME`/`IDLE` batches staged) — so the
   danger window for "Start now vs. save first" is specifically the
   next-restart window plus whatever the user hasn't staged at all.
3. `UpdateServer` (Settings) persists immediately but only takes effect on
   the next (re)start — starting with unsaved Settings edits runs the
   container on the old values.

**Gate rule (v1):** Start / Restart / Force Recreate are refused client-side
when any registered section is dirty: the bar shakes, focus moves to it, and
a toast explains. The refusal is a UI contract — the backend keeps applying
staged state on start (documented; no server-side notion of "unsaved UI
edits" exists or is proposed). Stop and Delete are not gated (they neither
consume nor destroy unsaved edits); module/backup/world operations are
deferred.

### B4. Floating bar design

- **Position:** one global bar, fixed bottom-center of the content pane
  (same `z` band as `ScrollToTop`, which sits bottom-right — bar max-width
  keeps them from overlapping on desktop; on small screens the bar spans
  full width with 12 px margins and ScrollToTop hides while the bar shows).
  Precedent: `scroll-to-top.svelte` uses `fixed right-8 bottom-8 z-50` with a
  `fly` transition — mirror that entrance (`fly y:8`,
  `motionDuration(200)`).
- **Stacking (decision):** **one bar listing dirty sections**, not one bar per
  section. Tabs are kept mounted (`CarbonInactiveTabPanels`), so multiple
  sections can be dirty while only one is visible; a per-section bar would
  stack and thrash. Bar content: `N unsaved changes` + one chip per dirty
  section (clicking a chip selects that tab), primary **Save all** (runs each
  section's `save` sequentially, first failure stops and reports), secondary
  **Undo all** (runs each `undo`). A single dirty section renders as a plain
  `Save` / `Undo` pair labeled with the section name.
- **Shake spec:** new `@keyframes carbon-shake` in the design-system layer
  (`lib/design-system/tokens.css` is a pinned contract — follow
  `docs/design-system/README.md` before adding): translateX 0 → −4px → 4px →
  −2px → 2px → 0, duration `--motion-base` (200 ms) or `--motion-slow`
  (300 ms) for 2 cycles, triggered by a `data-shake` attribute toggle so
  repeats re-fire. `prefers-reduced-motion: reduce` is already globally
  zeroed in `app.css:752`, and the panel-level
  `data-carbon-motion='reduced'` preference (`lib/motion.ts`) must gate the
  JS-triggered class the same way (`motionDuration(0)`).
- **Toast wording (proposal):**
  - Refusal on Start/Restart/Recreate with unsaved edits:
    "Save or undo your changes before starting the server." (+ section names
    when more than one: "Unsaved changes in: Server Settings, Routing.")
  - Save-all success: "Saved N sections."
  - Save-all partial failure: "Saved Configuration. Server Settings failed:
    <reason>."
  - Undo-all: "Reverted unsaved changes in N sections."
- **Mobile:** bottom sheet-style bar, chips collapse to the count + section
  names in the toast; Save/Undo remain ≥ 40 px tall tap targets; safe-area
  inset respected.

### B5. Edge cases

| Case | Behavior |
|---|---|
| Save in flight, user edits more | Section keeps its own `saving` state; bar's Save-all skips/retries it, `isDirty` recomputes against the **post-save baseline only after success** (re-baseline in the section, not globally). No double-submit: buttons disable per in-flight section |
| Undo while save in flight | Blocked for that section (button disabled) until the request settles; undoing mid-flight would race the baseline |
| Section switch while dirty | Allowed — bar persists (sections stay mounted); chips deep-link to the tab |
| Two sections dirty + Start clicked | Refused once (no toast spam); shake + toast lists both; user can Save all / Undo all or gate per section |
| Permission loss mid-edit | Save fails (403 from `config.updateServer`/`updateServerConfig`/`updateServerRouting`): dirty state is kept, toast explains the failure, bar gains an error affordance ("Retry / Undo"); the section stays editable but cannot save — undo becomes the advertised exit |
| Server stops while dirty | No data loss (edits are client-side). Settings fields that were disabled while running become editable; the bar simply remains. Starting from stopped is the gated action |
| Restore/backup invalidates the session mid-edit | Configuration reload replaces the baseline; if values changed server-side, sections re-run their load and drop dirty state **only if** the user confirms (or: keep dirty and show a conflict chip) — v1: reload + toast "Configuration changed on the server; your edits were kept" |
| Navigation/reload away from the page | v1: out of scope (Jira asks for the bar + state-change gating); v2: `beforeunload` + SvelteKit `beforeNavigate` guard driven by the same registry |
| Duplicate save of the same section (bar + section button) | Registry serializes via the section's own `saving()` flag; second call is a no-op |

### B6. Suggested split for implementation

1. Dirty registry + per-section save/undo exposure + single-section floating
   bar (Settings first — it already has `isDirty`).
2. Start/Restart/Recreate gate + shake + toasts.
3. Configuration + Routing wired into the registry; multi-section bar with
   chips, Save all / Undo all.
4. Navigation guards and restore-conflict handling (v2).

---

## Open questions

1. Live TTL default (60 s vs 120 s) and whether the Live toggle defaults on
   while a server is running — needs a UX pass against real save cadences.
2. Should `dimensions` become a supported native capability in
   `nativeCapabilities()` in Stage 4, or stay renderer-internal until
   Nether/End ship?
3. Does the design-system contract allow adding `carbon-shake` to
   `tokens.css`, or should the keyframe live in `app.css` next to the other
   `carbon-*` keyframes?
4. Should the Start gate also cover backup restore and world swap in v1, or
   is lifecycle-only correct for the first cut?
