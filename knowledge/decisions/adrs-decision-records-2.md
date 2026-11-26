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
| [0053](../../docs/adr/0053-reviewed-mod-update-application.md) | Reviewed mod updates reuse the staged batch and one planner for manual and scheduled runs | [Staged server updates](../invariants/staged-updates.md), [Provider adapters](provider-adapters.md) |
| [0056](../../docs/adr/0056-data-write-gate.md) | Ordinary panel writes share one per-server data lease while staged publication owns it exclusively through rollback | [Staged server updates - Publication](../invariants/staged-updates-publication.md) |
| [0057](../../docs/adr/0057-disposable-server-archive-acceptance.md) | Exact-head native ZIP/tar.gz round trip on a disposable controller and second Docker engine | [Server portability archive acceptance rehearsal](../testing/server-archive-rehearsal.md) |
| [0058](../../docs/adr/0058-staged-publication-phase-record.md) | One batch-bound publication record separates snapshot, dispatch intent, verified runtime outcome and cleanup | [Staged server updates - Publication](../invariants/staged-updates-publication.md) |
