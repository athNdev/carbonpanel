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
