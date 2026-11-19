---
type: Decision
title: "Recent architecture decisions — Decision records"
description: "The latest ADR rows appended to the bounded decision index."
status: stable
owners: [oss-runtime]
sources:
  - title: Full decision index
    resource: adrs-decision-records.md
---
# Recent decision records

The complete bounded index lives in [Decision records](adrs-decision-records.md);
these are the newest rows so that neither list exceeds the concept line cap.

| ADR | Decision | Governs |
| --- | --- | --- |
| [0049](../../docs/adr/0049-channel-promotion-gates.md) | Exact-revision and dual-registry verification gates for docs and OCI channel moves | [Release](../workflows/release.md), [Website and documentation publication](website-docs.md), [Channel promotion](channel-promotion.md) |
| [0050](../../docs/adr/0050-maintained-operational-guides.md) | Release-independent operational guides derive an exact source revision at build time instead of pinning it in the manifest | [Website and documentation publication](website-docs.md) |
