# PR #729 Nightly stabilization and split

**Status:** split executed locally on `pr/729-stabilize` on top of
`5f8af05ed79c6a29fd6ff33d94443a179c8e5744`. History and published ancestry are
preserved; nothing was rewritten, rebased or pushed. The branch is a descendant
of the published PR head `710491538` and of current main `020ebb7e0`.

## Published heads

| Lane | Head | Relationship |
| --- | --- | --- |
| PR #729 | `710491538` (`origin/feat/v2.1.1-improvements`) plus local reviewed fixes | Broad Nightly batch |
| #687 mod apply/scheduler | `10a8a285c` (`pr/687`) | Independent implementation on current main; owns reviewed mod application and scheduler policy |
| #715 World View | `403a9c3f4` (`pr/715`) | End-surface and qualified-End viewport work on the existing World View routes |

Already merged into main and not re-added: `#680` installed-mod update preview,
`#713`/`#720`/`#723` World View routes and server workspace.

## Executed split

| Commit | Verb | Scope |
| --- | --- | --- |
| `8b8202b2d` | removed | The competing reviewed mod-update apply/scheduler stack: apply service, policy, fetcher test, staging candidate metadata and admission fields, scheduler mod-update task/claim, `TaskService` policy hunks, proto `ModUpdateSelection`/`StagedModUpdate`/`UpdateModRequest` fields 6-9/`UpdateModResponse` fields 2-3 and `TASK_TYPE_MOD_UPDATE`, regenerated Go/TS protobufs, and the review/stage UI (`mod-update-ui*.ts`, `server-mods-update.test.ts`, the server-mods update panel and automatic-policy accordion). |
| `66c946cd8` | removed | ADR 0053 and the `mod-update-apply` decision concept. Rewrote `mod-update-panel-safety` as `Mod browser dependency planning guards`, keeping only the dependency-plan and install-context checks that remain. Removed the stale scheduler "policy revision claim" rule. |
| `db1816878` | kept | Recorded this split plan and dropped the scheduler policy-claim rule. |
| `b84a3aaf3` | kept | Formatting-only follow-up: `gofmt` on the restored `TaskService`. |
| `da3fc578d` | kept | Removed the last policy reference from the installed-mod preview comment. |
| `7ba5c920b` | kept | Named the split follow-up commits in this plan. |
| `208ad57a1` | kept | Changelog now describes the retained installed-mod preview as read-only. |
| `376d635f0` | kept | Listed every split commit in this plan. |
| `9fcad83a` | kept | Replaced the deleted update-panel mapping on the published design-system page with the retained read-only preview; the page test now rejects the removed helpers. |
| `064b283f` | kept | Restored the three retained staged-batch tests from `5f8af05ed` and rewrote the cron admission race against the retained `AddStagedContent` path. |
| `271241b6` | kept | Marked the removed update-panel scope and stale symbols/copy in the planning docs. |
| `HEAD` (this bookkeeping commit) | kept | Recorded the follow-up commits and the remaining queue in this plan. |

No product behavior changes follow `da3fc578d`; later commits are documentation
and the restored retained tests.

Primary sources removed: `a46a91da7`, `c8d8a476c`, the apply-specific hunks in
`582aa1c17`, `95627a7ad` and `710491538`, and the server-mods update panel from
`ee5825c80`. Their histories remain reachable; this batch only adds revert
commits.

### Removed files

- `internal/rpc/services/mod_update_apply.go`, `mod_update_apply_test.go`,
  `mod_update_policy.go`, `mod_update_fetcher_test.go`
- `internal/staging/mod_candidate.go`
- `internal/scheduler/mod_update_task_test.go`
- `internal/rpc/handlers/packwiz_deploy_refusal_test.go`
- `web/carbon-panel/src/lib/mod-update-ui.ts`, `mod-update-ui.test.ts`
- `web/carbon-panel/src/lib/components/server-mods-update.test.ts`
- `docs/adr/0053-reviewed-mod-update-application.md`
- `knowledge/decisions/mod-update-apply.md`

### Removed surfaces

- Proto and RPC: `ModUpdateSelection`, `StagedModUpdate`,
  `UpdateModRequest.update_selections/update_window/update_apply_at_unix/
  update_idle_minutes`, `UpdateModResponse.staged_updates/update_batch_id`,
  `TASK_TYPE_MOD_UPDATE`. Regenerated with `bun scripts/proto-generate.mjs`.
- Database: `ExpectedLiveSha512`, `CandidateGameVersion`, `CandidateLoader`,
  `Server.BeforeUpdate`/`effectiveModLoader`, `TaskTypeModUpdate`,
  `ModUpdateTaskConfig`, `ErrModUpdateScheduleChanged`,
  `UpdateModUpdatePolicy`, `AdvanceModUpdateSchedule`,
  `ToggleModUpdateSchedule`, `DisableInvalidModUpdateSchedule`,
  `HasStagedReviewedModCandidates`.
- Staging: candidate metadata, live-digest preconditions, reviewed-content
  batch window helpers, and the SHA-512 precondition in the undo journal.
- Scheduler/TaskService: `ModUpdatePlanner`, the mod-update budget, policy
  parse/validate helpers, `executeModUpdateTask`, due-slot policy claim and
  revalidation, and the `TaskService` policy create/update/toggle branches.
- Frontend: reviewed selection state, stage controls, automatic-policy
  accordion, outcome list, and the per-row "Stage this update" checkbox.
  Main's #680 read-only preview row and
  `$lib/utils/mod-update-summary` behavior are restored.

### Kept deliberately

- Main's #680 read-only installed-mod preview (`mod_update_preview.go`,
  `ListMods(check_updates)`, `InstalledModUpdate`) and its tests.
- The unconditional safe-stage path for compatibility-sensitive mod uploads in
  `ImportUploadedMod`, with its regression test.
- Generic staged-batch admission work that is not a duplicate feature surface:
  transactional `ensureOpenBatch`, `requireBatchAdmission`,
  `ErrUpdateBatchConflict`, config staging against the live row, discard
  journaling, and cron-row exclusion during rollback reconciliation.
- The unique #729 work: console floating save/undo, inventory and player
  presence/skins, plugin browse/install, freeze/deep-sleep,
  version-compatibility gate (`modcompat.go`, `server_version_compat.go`),
  standalone mods browser/dependency planner/loader directories, watcher
  crash-loop surfacing, and the unrelated docs/CI/dependency fixes.
- The native live-terrain viewport, including its Overworld, Nether and End
  dimension rendering. End support here is part of the separable native terrain
  view, not #715 admission/qualification work.

## #715 End slice

Verified absent: no End-specific admission or qualified-End route
implementation from #715 exists in this checkout. The only End references are
the native terrain dimension (`internal/worldterrain`, `native-map.ts`,
`WorldViewWorkspace.svelte` dimension selector), which stays with the
separable live-terrain viewport. No files were removed for this slice, and
main's `/worlds` routes were not touched.

## Remaining acceptance / transplant queue

- #687 owns the reviewed apply RPC fields, apply service, scheduler policy,
  staging candidate context and review UI. Do not re-add a second copy here.
- #715 owns End admission and qualification on the current `/worlds` routes.
- The PR body/description rewrite is orchestrator-owned. This checkout does not
  edit the GitHub PR body.
- Orphaned persisted `mod_update` scheduled-task rows from earlier Nightly runs
  remain in existing databases. They are inert but currently hit
  `unknown task type` in the scheduler; a cleanup or remap follow-up is needed.
- Deleted-proto-tag wire hygiene: the split removed `ModUpdateSelection`,
  `StagedModUpdate`, `UpdateModRequest` fields 6-9, `UpdateModResponse` fields
  2-3 and `TaskType` value 10 (`TASK_TYPE_MOD_UPDATE`). Reserve those numbers
  and names before any future reuse; do not renumber retained fields. This
  follow-up deliberately does not touch proto.
- Packaged/browser matrix and any Docker/live stack were not run this pass.
- Live acceptance is not claimed. The merge gate still needs an exact-head
  browser review of the affected routes plus the normal PR receipt.

## Verification

Focused source checks only; packaged/browser matrix is deferred.

- `git diff --check`
- `gofmt -l` on the Go files changed by this split
- `GOTOOLCHAIN=go1.26.11 go vet ./...`
- `GOTOOLCHAIN=go1.26.11 go test ./internal/rpc/... ./internal/db/...
  ./internal/scheduler/... ./internal/staging/... ./internal/minecraft/...
  ./internal/worldview/...`
- `cd web/carbon-panel && bun run check`
- focused `bun test` for the touched frontend files
- `golangci-lint v2.13.2 run --new-from-rev=020ebb7e ./...`

### Split follow-up checks (2026-11-27)

- `git diff --check` clean; `gofmt -l internal/db/staged_config_test.go` empty.
- `GOTOOLCHAIN=go1.26.11 go vet ./...` pass.
- `GOTOOLCHAIN=go1.26.11 go test ./internal/db/... ./internal/staging/...
  ./internal/rpc/...` pass.
- The four restored tests pass explicitly. Each was mutation-checked against
  the retained behavior (legacy batch retargeting, transactional rollback,
  idle-clock reset and cron admission) and failed when that behavior was
  temporarily broken.
- `python scripts/test_design_system_page.py` 12/12 pass.
- `go run ./cmd/knowledge-lint -root . -fix` then without `-fix`: 234
  concepts, indexes current, 40/40 queries; the pre-existing container/data
  invariant re-verify warnings remain.
- `bun scripts/proto-generate.mjs --check`: generated Go/TS outputs match.
- Local only: no push, GitHub or Jira action; live acceptance not claimed.
