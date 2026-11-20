---
type: Decision
title: "Architecture decisions — Recent decision records"
description: "Recent ADR records split from the main index to keep each concept bounded."
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: adrs.md
---
# Recent decision records

| ADR | Decision | Governs |
| --- | --- | --- |
| [0043](../../docs/adr/0043-egress-policy-enforcement.md) | Enforce egress policy per class, not per boolean | [Named egress classes](../decisions/egress-class-policy.md) |
| [0044](../../docs/adr/0044-disposable-upgrade-rehearsal-ci.md) | Recurring read-only real-binary upgrade rehearsal on disposable fixtures | [Release](../workflows/release.md) |
| [0045](../../docs/adr/0045-version-contract-and-multi-version-docs.md) | One fail-closed version contract and immutable per-version docs routes | [Website and documentation publication](website-docs.md) |
| [0046](../../docs/adr/0046-image-digest-provenance.md) | Record resolved image digests; opt-in strict pinned-digest preflight | [Image digest provenance](../decisions/image-digest-provenance.md), [Container privileges](../invariants/container-privileges.md) |
| [0047](../../docs/adr/0047-closure-evidence-guard.md) | Block OSS Done natively and verify acceptance evidence in the repo | [Acceptance evidence](../../docs/acceptance/README.md) |
| [0048](../../docs/adr/0048-bluemap-asset-routing-and-csp.md) | Exact gzip asset routing and a pinned JIT BlueMap webapp keep the panel CSP unchanged | [World viewing](../architecture/world-viewing.md) |
| [0050](../../docs/adr/0050-maintained-operational-guides.md) | Release-independent operational guides derive an exact source revision at build time instead of pinning it in the manifest | [Website and documentation publication](website-docs.md) |
| [0051](../../docs/adr/0051-explicit-local-admin-oidc-linking.md) | Explicit local-admin intent binds verified issuer+subject; local role ownership is retained | [SSO identity](../invariants/sso-identity.md) |
