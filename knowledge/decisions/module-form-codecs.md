---
type: Decision
title: Module form conversions
description: Module instance and template forms share pure row conversions while preserving separate request fields, staging policy, and explicit malformed-JSON reporting.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/features/modules/form-codecs.ts
  - web/carbon-panel/src/lib/features/modpacks/studio
  - web/carbon-panel/src/lib/features/modpacks/persisted-json.ts
  - web/carbon-panel/src/routes/modpacks/+page.svelte
  - scripts/manual-review/e2e/module-forms-check.mjs
  - web/carbon-panel/src/lib/components/server-ui/ModuleDialog.svelte
  - web/carbon-panel/src/lib/components/server-ui/ModuleTemplateCreateDialog.svelte
sources:
  - title: Module and Studio extraction
    resource: https://carbonpanel.atlassian.net/browse/OSS-341
---

# Choice

Environment keys, metadata keys and volume paths trim whitespace; blank keys
and incomplete volume rows are dropped. Environment values remain untrimmed,
and the last duplicate key wins. Volume JSON uses source/target and snake-case
read_only/create_dir. Instance row copies keep independent ports, dependencies
and event hooks; zero instance health settings retain 30/5/3 defaults.

Malformed persisted JSON retains the legacy empty-row fallback during mechanical
extraction; this is a lossy interpretation, not validation. Generated request
fields remain distinct: template defaults, instance overrides and staged scalar
updates must not be collapsed into one generic form payload. Scalar staging
omits topology and hook updates; immediate saves retain those fields.

Template editing decodes persisted source/target volume JSON into the same
hostPath/containerPath rows used by instance forms. Assigning the wire objects
directly leaves paths blank and breaks a later save. The real-dialog Chromium
fixture covers stored volume/env round trips, template-to-instance default/port
requests, scalar staging without topology, and immediate updates with topology
and numeric defaults. These tests use fixture RPCs and the real form controls.

Studio workflows are extracted one cohesive workflow at a time into
`features/modpacks/studio/`; route entries remain composition shells. Required
dependency reconciliation is owned by `studio/dependencies.ts`: it walks only
required dependencies, skips optional/duplicate/visited entries, is depth-bounded
at three levels and uses best-effort installs. Version migration is owned by
`studio/migration.ts`: it issues the same simulate/apply POST with the exact
`target_mc`/`target_loader`/`apply` body and `HTTP <status>` failure message, and
returns the compatibility report unchanged; the route keeps the form state,
toasts and post-apply reload. Both moves are mechanical and preserve outgoing
requests byte-for-byte; neither claims stricter validation of malformed persisted
JSON. The route keeps presentation and toasts.

The same mechanical rule governs four further extracted workflows:
`studio/loader-versions.ts` owns the lowercased loader URL, current-version
prepend and `['latest']` fallback (returning `null` for an empty loader means
"leave the list unchanged"); `studio/pack-resources.ts` owns override-file
fetch/save/delete, indexed datapack install and the raw `pack.toml` read, which
returns `null` on failure so the route keeps the last good text;
`studio/pack-maintenance.ts` owns the slug-keyed update map, the
`update_available` count and the refresh POST; `studio/mod-actions.ts` owns side
rotation, PATCH/DELETE/batch requests, online search and CurseForge
id-versus-slug version lookup. Search still parses JSON before checking
`res.ok`; the malformed-JSON-to-empty fallback remains lossy legacy behavior,
not validation. Outgoing requests stay byte-identical; the route keeps flags,
dialogs and toasts. Real-browser fixture QA drives these through the actual
Studio page at 1440 dark.

Malformed persisted JSON is now reported explicitly rather than silently
discarded. `form-codecs.ts` exposes `parseEnvVarsDetailed` and
`parseVolumesDetailed`, which return the same rows plus `malformed: true` when a
non-blank value cannot be parsed; the historical wrappers keep returning rows
only. Blank fields mean absent, not malformed. Both module dialogs show a
non-blocking warning on a malformed field, and the modpack library uses
`features/modpacks/persisted-json.ts` to flag unreadable persisted loader/version
metadata on the card. This is tolerant decoding with an honest report, not
validation, repair or rejection; valid values and all outgoing requests are
unchanged.

The last two Studio workflows follow the same mechanical rule.
`studio/pack-metadata.ts` owns the project GET, the whole-pack metadata/content
PUT, the `HTTP <status>` failures, the empty-baseline MC-version change rule,
content aggregation and the export-tab count breakdown.
`studio/export.ts` owns launcher formats/options, the exact export query and
URL, the CurseForge 422 dropped-item interpretation and the blob handoff; the
transport is injected so both Studio entry points are request-level testable
without a `$app` alias. Routes remain composition shells for form state, tabs,
dialogs and toasts. No generic form engine is introduced, and template defaults
and instance overrides stay distinct.
