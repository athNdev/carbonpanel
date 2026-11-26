---
type: Decision
title: Library adoption fencing
description: Compatibility approval is tuple-bound, binding outcomes keep their reported label, and an unmanaged target is never guessed from a folder name.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/features/server-world
  - web/carbon-panel/src/lib/components/server-ui/ServerWorld.svelte
  - web/carbon-panel/src/lib/features/world-library/presentation.ts
  - scripts/manual-review/e2e/server-world-tab-check.mjs
sources:
  - title: Server detail World tab
    resource: https://carbonpanel.atlassian.net/browse/OSS-382
---

# Choice

A library binding is the only authority for a managed level. Otherwise an
observed server-local level is `UNMANAGED`, a configured level without an
observation is `UNBOUND`, and neither yields a guessed name. The World tab
groups the active level, server environment and configuration. It shows binding
and revision details when a binding exists and preserves a differing configured
level as a separate fact. The tab opens the scoped map dialog on the current
server route and links to the World Library through `resolve()`.

Adoption reuses `CheckCompatibility`, `BindWorld` and `ResolveWorldOperation`
only. Permission, verdict and unknown-finding gates match the library route;
compatible with no unknown findings is the only unlock. A read-only report is
approved for the exact tuple it was checked against (server, revision, target
version and level). Selection or server navigation invalidates it, a delayed
response is discarded on arrival, and confirmation re-requires the same tuple.
Every reported outcome keeps its own label and explanation; queued, running,
skipped, cancelled and failed never become an applied success, and uncertainty
is resolved by an operator without automatic retry.

# Consequences

No new free-form command path or direct level-data write is introduced. The
existing stop/quiesce, private-clone, journal rollback and never-overwrite
guarantees remain in force. Stale approval cannot unlock a different revision
or server, and navigation cannot restore a previous server's report.
