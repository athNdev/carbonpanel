---
type: Decision
title: "Architecture decisions — Decision records (part 2)"
description: "ADR index continuation from 0048 onward."
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: adrs.md
---
# Decision records (part 2)

| ADR | Decision | Governs |
| --- | --- | --- |
| [0048](../../docs/adr/0048-bluemap-asset-routing-and-csp.md) | Exact gzip asset routing and a pinned JIT BlueMap webapp keep the panel CSP unchanged | [World viewing](../architecture/world-viewing.md) |
| [0049](../../docs/adr/0049-world-swap-data-preserving-rollback.md) | Quarantine promoted worlds and prove bytes plus the binding row before a rollback is reported | [World Library](../architecture/world-library.md) |
| [0050](../../docs/adr/0050-maintained-operational-guides.md) | Release-independent operational guides derive an exact source revision at build time instead of pinning it in the manifest | [Website and documentation publication](website-docs.md) |
| [0051](../../docs/adr/0051-explicit-local-admin-oidc-linking.md) | Bind a verified provider identity to its initiating local admin session and retain role origin | [SSO identity](../invariants/sso-identity.md) |
| [0052](../../docs/adr/0052-channel-promotion-gates.md) | Exact-revision and dual-registry verification gates for docs and OCI channel moves | [Release](../workflows/release.md), [Website and documentation publication](website-docs.md), [Channel promotion](channel-promotion.md) |
| [0054](../../docs/adr/0054-player-last-seen-semantics.md) | Player "last seen" is a nullable observation only ingestion writes; registration never fabricates presence | [Player management identity](../architecture/player-identity.md) |
| [0055](../../docs/adr/0055-version-change-compatibility-gate.md) | Version changes refuse with a per-file list when installed content cannot satisfy the target | [Server lifecycle policy](../architecture/server-lifecycle-policy.md) |
| [0057](../../docs/adr/0057-disposable-server-archive-acceptance.md) | Exact-head native ZIP/tar.gz round trip on a disposable controller and second Docker engine | [Server portability archive acceptance rehearsal](../testing/server-archive-rehearsal.md) |
