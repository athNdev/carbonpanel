---
type: Decision
title: Module form conversions
description: Module instance and template forms share pure row conversions while preserving separate request fields and staging policy.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/features/modules/form-codecs.ts
  - web/carbon-panel/src/lib/components/server/ModuleDialog.svelte
  - web/carbon-panel/src/lib/components/server/ModuleTemplateCreateDialog.svelte
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
