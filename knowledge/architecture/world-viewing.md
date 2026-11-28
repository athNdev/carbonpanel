---
type: Architecture
title: Map viewing
description: First-class maps are an observation-only surface behind an authenticated same-origin renderer proxy.
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/worldview.proto, internal/worldview]
sources:
  - title: ADR 0015 - first-class world viewing through an authenticated renderer proxy
    resource: ../../docs/adr/0015-world-viewing-renderer-proxy.md
  - title: ADR 0040 - installed BlueMap discovery, consent and in-context install
    resource: ../../docs/adr/0040-bluemap-installed-module-discovery.md
  - title: ADR 0048 - BlueMap asset routing and CSP compatibility
    resource: ../../docs/adr/0048-bluemap-asset-routing-and-csp.md
---

# Parts

- [Map viewing â€” Rule](world-viewing-rule.md) â€” WorldViewService observes worlds; it does not own them. A WorldRef
- [Map viewing â€” Invariants](world-viewing-invariants.md) â€” world_view/read never grants observe_players, overlay edits or admin;
- [Map viewing â€” Invariants (part 2)](world-viewing-invariants-2.md) â€” (no host allocation/publication), read-only /app/world bind of the canonical
- [Map viewing â€” Read next](world-viewing-read-next.md) â€” Fail-closed authorization
