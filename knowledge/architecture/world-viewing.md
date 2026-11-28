---
type: Architecture
title: Map viewing
description: First-class maps are an observation-only surface behind an authenticated same-origin renderer proxy.
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/worldview.proto, internal/worldview, web/carbon-panel/src/routes/worlds]
sources:
  - title: ADR 0015 - first-class world viewing through an authenticated renderer proxy
    resource: ../../docs/adr/0015-world-viewing-renderer-proxy.md
  - title: ADR 0040 - installed BlueMap discovery, consent and in-context install
    resource: ../../docs/adr/0040-bluemap-installed-module-discovery.md
  - title: ADR 0048 - BlueMap asset routing and CSP compatibility
    resource: ../../docs/adr/0048-bluemap-asset-routing-and-csp.md
---

# Parts

- [Map viewing — Rule](world-viewing-rule.md) — WorldViewService observes worlds; it does not own them. A WorldRef
- [Map viewing — Rule (part 2)](world-viewing-rule-2.md) — Managed BlueMap install and private discovery share one node eligibility
- [Map viewing — Invariants](world-viewing-invariants.md) — world_view/read never grants observe_players, overlay edits or admin;
- [Map viewing — Invariants (part 2)](world-viewing-invariants-2.md) — (no host allocation/publication), read-only /app/world bind of the canonical
- [Map viewing — Read next](world-viewing-read-next.md) — Fail-closed authorization
