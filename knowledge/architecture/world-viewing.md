---
type: Architecture
title: World viewing
description: First-class world maps are an observation-only surface behind an authenticated same-origin renderer proxy.
status: stable
owners: [oss-runtime]
code_refs:
  - proto/carbonpanel/v1/worldview.proto
  - internal/rpc
sources:
  - title: ADR 0015 - first-class world viewing through an authenticated renderer proxy
    resource: ../../docs/adr/0015-world-viewing-renderer-proxy.md
---

# Rule

`WorldViewService` observes worlds; it does not own them. A `WorldRef`
identifies a library binding, a server-local level or a static tile snapshot —
a folder name is never an identity. Map overlays and the opt-in player
visibility flag are the only mutations, and both are audited. Teleport and
world/region operations stay on the existing server, config and file RPCs with
their own authorization.

Renderer traffic is same-origin only. The browser receives an opaque,
short-lived session key, never a renderer URL or port; every proxied asset
request re-authenticates with the cookie or bearer token and enforces
`world_view/read` on the concrete server. Missing renderer endpoints, remote
nodes without a node-local renderer, unsupported capabilities and stale tiles
are reported honestly; there is no public map endpoint, no controller-local
fallback and no cross-origin iframe.

# Invariants

- `world_view/read` never grants `observe_players`, overlay edits or admin;
  anonymous and nil-enforcer requests are denied.
- Renderer capabilities are advertised by the adapter and gate visible
  controls; unsupported means unsupported, not a substitute control.
- Freshness is an `Observation`: timestamp, source and confidence. A missing
  renderer is onboarding, a stopped renderer is stale, not blank or "live".
- Container privileges do not change; renderer world mounts stay read-only.

# Read next

- [RPC contract](rpc-contract.md)
- [Fail-closed authorization](../invariants/authz-fail-closed.md)
