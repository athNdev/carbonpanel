---
type: Decision
title: Mod browser dependency planning guards
description: Dependency plans and browser context checks preserve the operator's install intent and refuse stale targets.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/mods/dependency-plan.ts
  - web/carbon-panel/src/lib/components/mod-browser-dialog.svelte
---
<!-- SPDX-License-Identifier: MPL-2.0 -->

# Choice

- Dependency planning keeps requirements beyond the traversal depth visible as
  unresolved outcomes and honors a dependency's explicit `version_id` pin.
  Dedupe tracks the requested pin per project: a second request for the same
  project with a different pin becomes an `unresolved` conflict entry instead
  of silently reusing or dropping a pin. Repeated unpinned requests stay a
  normal dedupe.
- The browser compares the server, Minecraft version, and loader context after
  dependency planning and before the install request. A changed context refuses
  the install instead of using a stale lookup.

# Consequences

Dependency plans cannot silently omit a requirement, substitute a different
pinned version, or install into a context other than the one that was planned.

# Read next

- [Mods browser target context](mods-browser-target-context.md)
