# PR #729 Nightly stabilization and split

**Status:** local stabilization against `origin/main` `020ebb7e0`; split not yet
executed. Published ancestry is preserved; this pass only adds merge and
follow-up commits.

## Published heads

| Lane | Head | Relationship |
| --- | --- | --- |
| PR #729 | `710491538` (`origin/feat/v2.1.1-improvements`) plus local reviewed fixes | Broad Nightly batch |
| #687 mod apply/scheduler | `10a8a285c` (`pr/687`) | Independent implementation on current main; owns reviewed mod application and scheduler policy |
| #715 World View | `403a9c3f4` (`pr/715`) | End-surface and qualified-End viewport work on the existing World View routes |

Already merged into main and not to be re-added: `#680` installed-mod update
preview, `#713`/`#720`/`#723` World View routes and server workspace, `#680`
and `#687`-adjacent staging/archive work.

## Overlap triage

- **#680 preview:** keep main's read-only `ListMods`/`InstalledModUpdate`
  contract. PR #729's equivalent preview is a superset only in comments; it must
  not introduce a second preview reason or UI.
- **#687 apply/scheduler:** PR #729's `a46a91da7` stack is an earlier competing
  implementation. #687 owns the RPC fields, apply service, policy, scheduler
  task, staging candidate, undo, and review UI. Do not extend the #729 copy.
- **#715 World View:** main's `/worlds` routes are current. Do not delete them or
  retarget server links to a dialog-only flow. PR #729's native live terrain is
  separable; End-specific admission belongs to #715.
- **Unique and safe in #729:** console floating save/undo, inventory and player
  presence/skins, plugin browse/install, freeze/deep-sleep, version-compatibility
  gate and the mods browser/dependency planner.

## Scoped transplant list for the #687 split

Drop from #729 (revert or transplant to #687; keep the commits in history):

- `a46a91da7` — `internal/rpc/services/mod_update_apply*.go`,
  `mod_update_fetcher_test.go`, `mod_update_policy.go`,
  `internal/staging/mod_candidate.go`, scheduler mod-update task and claim
  hunks, `internal/rpc/services/task.go` policy hunks, db staged-mod fields and
  migrations, `proto/carbonpanel/v1/mod.proto` `ModUpdateSelection`,
  `StagedModUpdate`, `UpdateModRequest` fields 6-9 and `UpdateModResponse`
  fields 2-3.
- `c8d8a476c` — `web/carbon-panel/src/lib/mod-update-ui*.ts`,
  `server-mods-update.test.ts`, and the review/stage card inside
  `server-mods.svelte`. Keep the main #680 preview row.
- Apply-specific hunks in `95627a7ad`, `582aa1c17` and `710491538`; retain their
  provider-diagnostic, compatibility and copy fixes where they are not tied to
  `ModUpdateSelection` or scheduled policy.
- `ee5825c80` is mixed: keep the standalone mods browser, filter vocabulary,
  dependency planner and loader-directory work; leave the server-mods update
  panel to #687.

Keep in #729: `internal/minecraft/modcompat.go`, the server version gate,
plugin browse/install, and the read-only preview contract already supplied by
main.

## World View route preservation

This pass restores `web/carbon-panel/src/routes/worlds/**` from current main and
restores the `page` presentation in `WorldViewWorkspace.svelte` (breadcrumbs,
back link, default presentation). The native live-terrain viewport stays, but
the dialog-only assumption is removed so both entry points compile and render.

## Verification

Focused source checks only; packaged/browser matrix is deferred while #686 runs.

- `go test ./internal/mcp/... ./internal/db/... ./internal/staging/...
  ./internal/scheduler/... ./internal/rpc/... ./internal/worldview/...
  ./internal/worldterrain/... ./internal/worldlib/... ./internal/docker/...`
- `cd web/carbon-panel && bun run check`
- `bun test --conditions=browser src/lib/features/worldview
  src/lib/components/worldview src/lib/components/server-mods-update.test.ts
  src/lib/mod-update-ui.test.ts src/routes/worlds`
