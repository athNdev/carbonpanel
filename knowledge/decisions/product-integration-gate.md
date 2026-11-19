---
type: Decision
title: Product integration gate re-baseline (OSS-365)
description: The three product epics are accepted against current main, with merged-vs-qualified evidence kept distinct and unmeasurable renderer claims recorded as blockers rather than promotions.
status: stable
owners: [oss-product]
code_refs:
  - internal/worldview/proxy.go
  - internal/worldview/proxy_measurement_test.go
  - internal/bluemapdiscovery/world_preservation_test.go
  - docs/adr/0015-world-viewing-renderer-proxy.md
  - docs/adr/0017-player-management-identity-and-capability-gates.md
sources:
  - title: Cross-feature integration gate
    resource: https://carbonpanel.atlassian.net/browse/OSS-365
  - title: Integrated roadmap
    resource: ../../product-plans/integrated-roadmap.md
---

# Choice

OSS-365 accepts the three product epics against `origin/main@9e81679b`, not
the earlier planning pin `518b64b8`. ADR 0015 (renderer proxy), ADR 0016
(World Library) and ADR 0017 (player identity/capability gates) are accepted
records; ADR 0015 keeps the documented "BluePanel = BlueMap alongside a panel"
assumption explicit rather than promoting it to a verified vendor identity.
Cross-review findings H1 (W20 inventory) and H2 (W17/ADR 0013) are stale as
statements about current code, so the plans treat those capabilities as
landed. H3 stays binding in the qualified sense: a merge is not acceptance.
W15 (OSS-334), W16 (OSS-335) and W17 (OSS-336) have recorded qualification
evidence; W18 (OSS-337) remains open, so new generated RPC work follows the
current main convention instead of assuming a completed migration.

Renderer performance claims stay bounded by what was actually measured on an
owned fixture. The panel proxy streams through a fixed 32 KiB buffer with an
8 MiB hard cap; the measured loopback source-to-client p95 and the browser
source-to-UI p95 are recorded in the acceptance report with their fixture
limits. Renderer-owned process RAM and on-disk tile storage are NOT measured:
no live renderer, container or real tile dataset exists in this environment,
and the task prohibits live host/deployment work. Those are recorded as exact
blockers, not as passed gates.

# Verification

Real Chromium ran the production bundle built from the exact head at
1440/768/390/360 viewports, including the previously failing S02 mobile rerun
and the stale/conflict deep-field scenario. Go fixtures measure the proxy
streaming budget, source-to-client latency, world-data byte preservation across
adapter install/abort/remove, and the remote-node node-local executor boundary.
Knowledge lint regenerates the index and passes its fixed queries.

# Read next

- [World viewing](../architecture/world-viewing.md)
- [Player management](../architecture/player-management.md)
- [World Library](../architecture/world-library.md)
- [Fail-closed authorization](../invariants/authz-fail-closed.md)
