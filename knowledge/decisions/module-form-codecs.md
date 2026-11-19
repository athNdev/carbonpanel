---
type: Decision
title: Module form conversions
description: Module instance and template forms share pure row conversions while preserving separate request fields, staging policy, and explicit malformed-JSON reporting.
status: stable
owners: [oss-runtime]
code_refs: [web/carbon-panel/src/lib/features/modules/form-codecs.ts, web/carbon-panel/src/lib/features/modpacks/studio, web/carbon-panel/src/lib/features/modpacks/persisted-json.ts, web/carbon-panel/src/routes/modpacks/+page.svelte, scripts/manual-review/e2e/module-forms-check.mjs, web/carbon-panel/src/lib/components/server-ui/ModuleDialog.svelte, web/carbon-panel/src/lib/components/server-ui/ModuleTemplateCreateDialog.svelte]
sources:
  - title: Module and Studio extraction
    resource: https://carbonpanel.atlassian.net/browse/OSS-341
---

# Parts

- [Module form conversions — Choice](module-form-codecs-choice.md) — Environment keys, metadata keys and volume paths trim whitespace; blank keys
- [Module form conversions — Choice (part 2)](module-form-codecs-choice-2.md) — Malformed persisted JSON is now reported explicitly rather than silently
