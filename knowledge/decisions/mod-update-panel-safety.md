---
type: Decision
title: Mod update safety guards
description: Policy defaults, reviewed candidate selections, and dependency plans preserve operator intent across saves and asynchronous context changes.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/mod-update-ui.ts
  - web/carbon-panel/src/lib/components/server-mods.svelte
  - web/carbon-panel/src/lib/mods/dependency-plan.ts
  - web/carbon-panel/src/lib/components/mod-browser-dialog.svelte
sources:
  - title: ADR 0053
    resource: ../../docs/adr/0053-reviewed-mod-update-application.md
---

<!-- SPDX-License-Identifier: MPL-2.0 -->

# Choice

- An omitted `skip_when_running` policy setting keeps its absent meaning. The
  panel renders its effective unchecked state and omits the field on save
  unless the operator makes an explicit choice.
- A reviewed update selection is bound to the candidate version, digest, and
  filename fingerprint that the operator selected. A changed candidate is no
  longer checked and cannot be staged under the earlier review.
- Dependency planning keeps requirements beyond the traversal depth visible as
  unresolved outcomes and honors a dependency's explicit `version_id` pin.
- The browser compares the server, Minecraft version, and loader context after
  dependency planning and before the install request. A changed context refuses
  the install instead of using a stale lookup.

# Consequences

Policy saves do not silently change an absent boolean into its opposite
default. Changed candidates require a fresh operator review. Dependency plans
cannot silently omit a requirement, substitute a different pinned version, or
install into a context other than the one that was planned.

# Read next

- [Reviewed mod update application](mod-update-apply.md)
