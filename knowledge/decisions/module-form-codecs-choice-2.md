---
type: Decision
title: "Module form conversions — Choice (part 2)"
description: "Malformed persisted JSON is now reported explicitly rather than silently"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: module-form-codecs.md
---
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
