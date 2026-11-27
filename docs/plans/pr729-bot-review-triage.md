# PR #729 CodeRabbit Review Triage

**PR:** feat: v2.1.1 improvements (#729, `feat/v2.1.1-improvements`)  
**Source artifacts:** the CodeRabbit review bodies for PR #729 (2 bodies, split by `===SPLIT===`) and the 7 extracted inline actionable comments, fetched from the PR review API into the agent's scratch directory (not tracked, so no workstation path is recorded here).  
**Triage date:** 2026-11-23  
**Scope note:** The first review body explicitly states "5 more major findings are in follow-up comments..." and "48 more minor findings are in follow-up comments..." that are not present in the provided review-body artifact. This triage covers the 7 extracted inline comments, the outside-diff comment, and actionable findings visible in the provided review bodies. It does **not** fabricate dispositions for follow-up comments absent from the artifacts.

> **PR #729 split note (2026-11-27):** Findings that cite the reviewed
> mod-update apply/selection stack describe code that PR #729 removed:
> `internal/rpc/services/mod_update_apply.go`, `mod_update_policy.go`,
> `web/carbon-panel/src/lib/mod-update-ui.ts`, the scheduler mod-update policy
> and claim, `ModUpdateSelection`/`StagedModUpdate`, and the per-row "Stage
> this update" control. They remain historical triage, not open work in this
> tree. A2-A11 below are affected (A2 `ExpectedLiveSha512`; A3/A4/A5 apply
> service; A6 policy UI; A7-A10 scheduler mod-update policy; A11 selection
> state). The retained surface is the read-only #680 installed-mod preview in
> `internal/rpc/services/mod_update_preview.go`, summarized by
> `summarizeModUpdateCheck()` in
> `web/carbon-panel/src/lib/utils/mod-update-summary.ts`, with no stage, apply
> or policy controls. #687 owns any future apply work; re-triage a finding
> against retained code before acting on it.

## VALID+OPEN backlog (top priority)

| # | Severity | File:line | Concern | Evidence / notes |
|---|---|---|---|---|
| A1 | Major | `internal/worldlib/chunk_surface.go:43` | `JavaNetherSpan.MaxY` is 127; vanilla Nether storage height is 256 (`min_y:0,height:256,logical_height:128`), so vanilla 9-bit heightmaps (37 longs) are rejected as unsupported. | Current code still has `JavaNetherSpan = JavaDimensionSpan{MinY: 0, MaxY: 127}`. Fix requires span/storage/layout audit: parser heightmap bit width/length, section count arrays, Nether roof scan assumptions, tests/fixtures. Not patched in this triage. |
| A2 | Major | `internal/db/models.go:635` / `internal/staging/file_undo.go` commit path | `ExpectedLiveSha512` is recorded but publish path does not recheck digest before replacing live file; edited-after-stage file can be overwritten. | Review body cites field comment and `commitStreamingFiles` not comparing digest. Code search confirms `ExpectedLiveSha512` exists on model and is written in stage paths (`internal/rpc/services/stage_content.go`), but no publish-time comparison was found during quick grep. **Needs focused verification/fix in staging/journal publish path.** |
| A3 | Major | `internal/rpc/services/mod_update_apply.go` (staging/download flow) | Temporary release lookup/download failures can be classified as permanent refusals; retryable staging errors after recording per-file outcomes may not propagate. | Review body lines 105-162 describe two related retryability problems. Not validated end-to-end here; treat as VALID+OPEN pending code inspection. |
| A4 | Major | `internal/rpc/services/mod_update_apply.go:92` | SSRF/egress: mod download client with proxy can bypass destination egress check because dialer control sees proxy address; redirect hops also need policy coverage. | Security-sensitive. Review cites `utils.NewSafeHTTPClient(2*time.Minute, false)` and proxy behavior. Requires maintainer policy decision (disable proxy for provider downloads or validate destination through proxy). **VALID+OPEN / security review required.** |
| A5 | Major | `internal/rpc/services/mod_update_apply.go` publish/version gate | Staged mod candidate compatibility not rechecked before publish; version change can commit while old-version mod batch open / staged candidates not covered. | Review body lines 205-236. High data-integrity risk; not patched here. |
| A6 | Major | `web/carbon-panel/src/lib/mod-update-ui.ts:105` | Existing policy `{"enabled":true}` without explicit `skip_when_running` is rendered/saved as checked, changing behavior on save. | Review body lines 246-275. Needs UI/config-reader fix with regression test. |
| A7 | Major | `internal/scheduler/scheduler.go:855` | Due-slot claim not fenced against stale policy snapshot; policy edit can race and excluded mods may reach planner. | Review body lines 283-311. Needs atomic claim/revision or claim returns current policy. |
| A8 | Major | `internal/scheduler/scheduler.go:422-430` | Transient next-run write failure can disable valid tasks after status write succeeds. | Review body lines 312-340. Disable only invalid schedule calculation; transient storage errors should release claim/leave due. |
| A9 | Major | `internal/scheduler/scheduler.go:433` | Invalid-timezone disable query can disable operator-corrected schedule; should match observed status/schedule/deadline. | Review body lines 342-370. |
| A10 | Major | `internal/scheduler/scheduler.go:1381` | Explicit `CRON_TZ=` / `TZ=` prefix overwritten by task timezone; bare expressions keep current behavior. | Review body lines 372-417. |
| A11 | Major | `web/carbon-panel/src/lib/components/server-mods.svelte:361` | Selection remains checked after reviewed candidate fingerprint changes; staging can submit new candidate under old selection. | Review body lines 421-454. |
| A12 | Major | `internal/rpc/handlers/mod_search.go:583` | Explicit Modrinth provider selection can be overridden by Hangar when slugs collide; automatic fallback should only run without explicit provider. | Review body lines 462-495. |
| A13 | Major | `web/carbon-panel/src/lib/mods/dependency-plan.ts:104` | Depth-limit omission can drop selected unknown dependency without unresolved entry. | Review body lines 502-531. |
| A14 | Major | `web/carbon-panel/src/lib/mods/dependency-plan.ts:133` | Dependency `version_id` pin not honored; planner always picks `versions[0]`. | Review body lines 534-565. |
| A15 | Major | `web/carbon-panel/src/lib/components/mod-browser-dialog.svelte:391` | Install context not validated after dependency planning; stale lookup can install into changed server/version context. | Review body lines 572-603. |
| A16 | Major | `internal/minecraft/modcompat.go:247` | Quilt heterogeneous dependency entries (string + object) fail whole descriptor decode; can allow incompatible downgrade. | Review body lines 612-641. |
| A17 | Major | `internal/minecraft/modcompat.go:260-261` | Bare Quilt version interpreted as exact instead of caret/range semantics; explicit Quilt `=` and bare Fabric exact must be preserved. | Review body lines 644-674. |
| A18 | Major | `internal/minecraft/modcompat.go:370-371` | Unknown alternative erased by later known non-match; violates fail-open/unknown-preserving behavior. | Review body lines 676-708. |
| A19 | Major | `internal/rpc/services/server.go:1674` | Version-change compatibility gate scans with old loader when request changes loader+mcVersion in same request. | **ADDRESSED in current tree**: `UpdateServer` builds an effective server with destination loader before `refuseVersionIncompatibleContent` when MC version changes (see `server.go` around 1722-1732). |
| A20 | Major | `internal/rpc/services/server_version_compat.go:75` | Datapack version gate scans `GetDatapacksPath(dataRoot, "")` (defaults to `world`) instead of active `ActualLevelName()`. | Confirmed current code still passes empty level name. **VALID+OPEN.** |
| A21 | Major | `internal/reconciler/reconciler.go:1167` | `AssertNoServerOperation` error conflates active operation and journal read failure; stop announcement may be suppressed permanently after read error. | Review body lines 795-823. Code currently returns `journal.AssertNoServerOperation(ctx, serverID) != nil` for crash path; needs distinction unknown vs active owner. **VALID+OPEN.** |
| A22 | Major (outside diff) | `scripts/manual-review/e2e/field-appearance.mjs:120-121` | Still navigates to deleted `/worlds` and waits for missing `Map status` control. | **ADDRESSED in current tree** (2026-11-23): `field-appearance.mjs` now exercises `/servers/ready`, `/servers`, and `/servers/new` (no `/worlds`, no `Map status`). Confirm with CI/editor-dialog browser regression. |
| A23 | Minor | `web/carbon-panel/src/lib/features/worldview/native-map.ts` / loader/live behavior | Any remaining minor live-revalidation/incomplete-tile/If-None-Match issues should be rechecked after fixes; see inline dispositions below. | All three specific inline minors were fixed in working tree; no additional concrete open minor extracted from bodies beyond follow-up note. |

## Inline comment dispositions (7/7)

| Inline | Severity | File:line | Concern | Disposition | Evidence |
|---:|---|---|---|---|---|
| 0 | Major | `internal/worldlib/chunk_surface.go:43` | Nether span should use storage height (MaxY 255), not logical 127; add vanilla 9-bit heightmap fixture. | **VALID+OPEN** | `JavaNetherSpan = JavaDimensionSpan{MinY: 0, MaxY: 127}` still present. Parser computes bits/packed length from span; 127 gives 8-bit/32-long expectation, incompatible with vanilla 9-bit/37-long heightmaps. |
| 1 | Major | `internal/worldterrain/dimension.go:29` | Bedrock roof should be transparent but terrain below roof must remain visible; don't blank columns. | **ALREADY-ADDRESSED** | `dimension.go` sets `Nether.SurfaceUnderRoof=true`. `chunk_surface.go` `readSurfaceBlocks` descends below roof bedrock (`surfaceUnderRoof && javaBedrockBlock` scan) and keeps roof-only columns empty. `dimension_test.go:TestRenderDimensionTileNetherRoofShowsTerrainBelow` asserts terrain below roof renders and roof-only column is empty. Review's cited names (`HideBedrockSurface`, `TestShadeGridNetherRoofStaysTransparent`) are stale but behavior is covered. |
| 2 | Major | `internal/worldterrain/lz4java.go:97` | XXH32 checksum must compare Java's masked low-28-bit value. | **FIXED (working tree)** | `lz4java.go:99` compares `xxh32(out[start:], lz4JavaDefaultSeed)&0x0FFFFFFF != check`. |
| 3 | Minor | `scripts/test-ignore-policies.py:117` | Docker required-file check should include an existing route like `servers/+page.svelte`. | **FIXED (working tree)** | `scripts/test-ignore-policies.py:118` includes `"web/carbon-panel/src/routes/servers/+page.svelte"`. |
| 4 | Minor | `web/carbon-panel/src/lib/components/worldview/NativeMapViewport.svelte:175` | Live mode read should not make loader-creation effect depend on `live`; preserve loader/tiles on live toggle. | **ALREADY-ADDRESSED** | Loader creation effect keys on path/generation/dimension and seeds live via `untrack(() => tileLoader.setLive(live))` (line ~179). Separate effect `loader?.setLive(live)` handles toggles (line ~191). |
| 5 | Minor | `web/carbon-panel/src/lib/features/worldview/native-map-loader.ts:34` | Incomplete/provisional saved tiles need finite live recheck deadline; requests unconditional. | **FIXED (working tree)** | `recheckAt` returns `Date.now() + liveRecheckDelayMs` for incomplete saved tiles when live; incomplete tiles do not get `If-None-Match` in `startNext` because conditional only set when complete and non-provisional. |
| 6 | Minor | `web/carbon-panel/src/lib/features/worldview/native-map.ts:261` | Send `If-None-Match` when conditional present on both attempts; keep manifest/preview unconditional. | **FIXED (working tree)** | `request()` accepts headers; `fetchTile()` passes `conditional ? { 'If-None-Match': conditional } : undefined`; retry calls `fetchTile()` again. Manifest uses `request(root+'native/manifest.json', signal)` with no header. |

## Review-body actionable findings (visible in provided artifacts)

| Severity | File:line (review) | Concern | Disposition | Evidence / notes |
|---|---|---|---|---|
| Major (outside diff) | `scripts/manual-review/e2e/server-inventory-check.mjs:195-220` / field-appearance | Browser checks navigate to deleted `/worlds` and assert missing control; breaks e2e. | **ADDRESSED in current tree** (2026-11-23) | `native-map-viewport-check.mjs` and `worldview-scoped-denial-check.mjs` use server-detail `?map=` flow; `field-appearance.mjs` no longer contains `/worlds` or `Map status` and now checks `/servers/ready`, `/servers`, `/servers/new`. Reconfirm in CI. |
| Major | `internal/db/models.go:635` | `ExpectedLiveSha512` not rechecked before publish. | **VALID+OPEN (needs verification)** | See A2. |
| Major | `internal/rpc/services/mod_update_apply.go:381-383` | Retryable staging errors not propagated after per-file outcomes recorded. | **NEEDS-VERIFICATION** | Not inspected in this pass. |
| Major | `internal/rpc/services/mod_update_apply.go:62-63` | Temporary release lookup failures / 429 treated as permanent refusal. | **NEEDS-VERIFICATION** | Not inspected. |
| Major | `internal/rpc/services/mod_update_apply.go:92` | SSRF/egress bypass when proxy configured. | **VALID+OPEN (security)** | See A4. |
| Major | `internal/rpc/services/mod_update_apply.go:353` | Staged mod compatibility not rechecked before publish; version-change gate misses staged candidates. | **NEEDS-VERIFICATION** | Not inspected. |
| Major | `web/carbon-panel/src/lib/mod-update-ui.ts:105` | Skip-while-running defaulting changes saved behavior. | **NEEDS-VERIFICATION** | Not inspected. |
| Major | `internal/scheduler/scheduler.go:855` | Policy snapshot fence missing on claim. | **NEEDS-VERIFICATION** | Not inspected. |
| Major | `internal/scheduler/scheduler.go:422-430` | Transient write failure disables valid task. | **NEEDS-VERIFICATION** | Not inspected. |
| Major | `internal/scheduler/scheduler.go:433` | Invalid-timezone disable can clobber operator-corrected schedule. | **NEEDS-VERIFICATION** | Not inspected. |
| Major | `internal/scheduler/scheduler.go:1381` | Explicit CRON_TZ/TZ prefix overwritten. | **NEEDS-VERIFICATION** | Not inspected. |
| Major | `web/carbon-panel/src/lib/components/server-mods.svelte:361` | Stale selection fingerprint can stage newer candidate. | **NEEDS-VERIFICATION** | Not inspected. |
| Major | `internal/rpc/handlers/mod_search.go:583` | Explicit provider selection not honored on slug collision. | **NEEDS-VERIFICATION** | Not inspected. |
| Major | `web/carbon-panel/src/lib/mods/dependency-plan.ts:104` | Depth limit drops unknown dependency silently. | **NEEDS-VERIFICATION** | Not inspected. |
| Major | `web/carbon-panel/src/lib/mods/dependency-plan.ts:133` | `version_id` pin ignored. | **NEEDS-VERIFICATION** | Not inspected. |
| Major | `web/carbon-panel/src/lib/components/mod-browser-dialog.svelte:391` | Stale dependency-planning continuation can install in wrong context. | **NEEDS-VERIFICATION** | Not inspected. |
| Major | `internal/minecraft/modcompat.go:247,260,370` | Quilt dependency parsing/range semantics/unknown alternative handling. | **NEEDS-VERIFICATION** | Not inspected; important correctness cluster. |
| Major | `internal/rpc/services/server.go:1674` | Loader change not considered in compatibility gate. | **NEEDS-VERIFICATION** | Not inspected. |
| Major | `internal/rpc/services/server_version_compat.go:75` | Datapack gate scans default `world` not active level. | **VALID+OPEN** | Current code passes `""` level name. |
| Major | `internal/reconciler/reconciler.go:1167` | Journal read error suppresses deferred SERVER_STOP permanently. | **VALID+OPEN** | Current crash path treats any `AssertNoServerOperation` error as active owner/suppression. |
| Minor/nitpick (body tail, visible) | test files listed in nitpick section | Several test-strengthening suggestions (mod update apply concurrency, server-mods-update selection test, task_test persistence, datapack-search Enter integration). | **LOW-VALUE / DEFER** | Valid quality improvements but not blocking correctness; keep as backlog if touching those tests. |

## Notes

- The second review body (after `===SPLIT===`) is a status/summary review repeating the same 7 inline actionable comments; no new actionable findings beyond them.
- The review body reports "21 Major comments" with 5 more in follow-up comments and "48 Minor comments" in follow-up comments; those follow-ups are not in the provided artifacts. A full PR triage should fetch them from GitHub/CodeRabbit before closing the bot-review gate.
- I did not run git commands or modify product code; this file is documentation only.

## 2026-11-23 working-tree disposition update

| Item | Status | Evidence / notes |
|---|---|---|
| A1 | **ACCEPTED (independent review)** | Nether heightmap/storage-logical split fixed in `internal/worldlib/chunk_surface.go`, `chunk_surface_test.go`, `internal/worldterrain/dimension.go`; knowledge updated. Reviewer `ses_df70b23e8ffe0CR8xiLX9uKTzg`. |
| A2/A3/A5 | **ACCEPTED (independent review)** | Publish-time digest precheck, retryability/transient staging propagation, publish-time mod candidate recheck + server version/loader gate. Reviewer `ses_df6ce6d6cffeJ2n9EqEqJ54LuA` applied a small fix: removed `StagedContentChange.BeforeCreate` lock-regression hook and added lock-tolerant best-effort server reload in `stage_content.go`; targeted db/staging/services checks reported passing. |
| A4 | **IN REVIEW** | SSRF/egress fix implemented by `ses_df6cd4069ffeUGERFD0L32t68e` (guarded egress client, redirect/initial validation, proxy bypass closed, regression tests). Independent review pending. |
| A6/A11/A13/A14/A15 | **ACCEPTED (independent review)** | Frontend mod-update/dependency/install-context fixes; svelte-check 0/0, targeted browser tests 40/40, prettier/eslint/knowledge-lint pass. Reviewer `ses_df6f236d6ffez93Wy0oc2cd248`. |
| A7/A8/A9/A10 | **ACCEPTED (independent review)** | Scheduler claim/persistence/timezone fixes; scheduler tests and knowledge-lint pass. Reviewer `ses_df753c3f7ffeZhmc4BgQh7CxoF`. |
| A12/A16/A17/A18 | **ACCEPTED (independent re-review)** | Mod compatibility fail-closed/normalization/pre-release/dependency fixes; `ImportUploadedMod` intentionally staging-only with regression test; MCP/web descriptions updated. Reviewer `ses_df7035f4bffeFLAkM0L7KNKOCK`. Proto `mod.pb.go`/`mod_pb.ts` regenerated. |
| A19 | **ADDRESSED in current tree** | `UpdateServer` scans with destination loader when loader+mcVersion change together (`internal/rpc/services/server.go`). |
| A20 | **ALREADY ADDRESSED in current tree** | `server_version_compat.go` resolves `cfg.ActualLevelName()` and passes it to `incompatibleInstalledContent`/`GetDatapacksPath`; regression `TestUpdateServerRefusesIncompatibleDatapackDowngradeInCustomLevel` present. Verified by `ses_df6cd4076ffdVR3n8UWvj85zgW` (targeted tests/vet pass). |
| A21 | **ALREADY ADDRESSED in current tree** | `suppressStopAnnouncement` returns false only on nil; defers on active-owner sentinel and on unknown/read errors via pending-stop flush; regression `TestJournalReadFailureDefersStopAnnouncement` present. Verified by `ses_df6cd4071ffeCJwMYG0GzHrXD9` (`go test ./internal/reconciler/...`, `go vet` pass). |
| A22 | **ADDRESSED in current tree** | `field-appearance.mjs` no longer references `/worlds`/`Map status`; confirm in CI. |
| CI a11y script regressions | **FIXED in working tree** | `scripts/manual-review/e2e/a11y-check.mjs`: SHELL_FOCUS updated for OSS-465 copy, assertTabPanels accepts OSS-464 persistent hidden tabpanels. |
| MCP doc drift | **FIXED in working tree** | `knowledge/architecture/mcp-server-rule-2.md` count claim 241→243; `TestMCPDocsCatalogueCountsMatchSourceOfTruth` passes. |