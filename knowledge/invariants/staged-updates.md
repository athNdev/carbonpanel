---
type: Invariant
title: Staged server updates
description: Staging a config, mod, datapack, file, modpack, or module edit records the open batch and does not restart the server or write the live tree.
status: stable
owners: [oss-runtime]
code_refs: [internal/db/staged_config.go, internal/db/staged_content.go, internal/rpc/services/stage_content.go, internal/staging, internal/rpc/services/update_batch.go, internal/scheduler/scheduler.go, web/carbon-panel/src/lib/components/server-configuration.svelte, web/carbon-panel/src/lib/components/server-tasks.svelte]
sources:
  - title: Staged update research
    resource: ../../docs/research/staged-update-application.md
verified: { by: agent:opencode, at: 2026-11-27 }
review_after: 2027-01-28
---

# Parts

- [Staged server updates — Rule](staged-updates-rule.md) — A server has one open update batch. Staging adds to that batch. It must not
- [Staged server updates — Publication](staged-updates-publication.md) — A due staged batch publishes through the journal; recovery and retry semantics preserve live data.
