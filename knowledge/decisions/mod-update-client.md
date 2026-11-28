---
type: Decision
title: Reviewed mod update client and scheduled policy
description: The Mods tab and policy editor bind requests to the captured server, use the scheduler's effective default budget, and fail closed on malformed policy.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/components/server-mods.svelte
  - web/carbon-panel/src/lib/mod-update-ui.ts
  - internal/rpc/services/mod_update_policy.go
sources:
  - title: Parent concept
    resource: mod-update-apply.md
  - title: ADR 0053
    resource: ../../docs/adr/0053-reviewed-mod-update-application.md
---
# Choice

The policy editor mirrors `scheduler.DefaultModUpdatesPerRun` (10) for an
absent or zero `max_updates_per_run`, so the displayed and re-saved budget is
the one the scheduler applies. Malformed persisted JSON loads disabled even
when the task status says enabled; an empty legacy config remains a valid unset
policy.

The Mods tab captures the selected server id and the request generation before
staging. A response that lands after navigation or a newer request writes no
status or selection and starts no refresh for the newly selected server.

A scheduled policy reads the installed-mod inventory without opening the server
data root, so a server whose directory does not exist yet reports an empty
inventory instead of a planner failure.
