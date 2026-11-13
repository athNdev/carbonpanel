---
type: Decision
title: Architecture decisions
description: Index of the accepted ADRs and the concept each one governs.
status: stable
owners: [oss-runtime]
code_refs:
  - docs/adr
sources:
  - title: ADR directory
    resource: ../../docs/adr
---

# Decision records

| ADR | Decision | Governs |
| --- | --- | --- |
| [0001](../../docs/adr/0001-moby-sdk-migration.md) | Use the Moby SDK instead of a shelled-out Docker CLI | [Runtime](../architecture/runtime.md), [Node resolution](../architecture/node-resolution.md) |
| [0002](../../docs/adr/0002-sveltekit-resolve-navigation.md) | Use SvelteKit resolve/navigation conventions in the console | [Frontend change](../workflows/frontend-change.md) |
| [0003](../../docs/adr/0003-sqlite-durability.md) | Superseded. Original continuous-replication proposal | [Persistence](../architecture/persistence.md) |
| [0004-docker-locator](../../docs/adr/0004-docker-locator.md) | One node-to-daemon Locator | [Runtime seams](../architecture/runtime-seams.md), [Node resolution](../architecture/node-resolution.md) |
| [0004-proxy-route-plans](../../docs/adr/0004-proxy-route-plans.md) | Proxy route policy is a pure PlanFor | [Runtime seams](../architecture/runtime-seams.md), [TCP proxy](../architecture/proxy.md) |
| [0004-world-data-seam](../../docs/adr/0004-world-data-seam.md) | World data flows through worldstore | [Runtime seams](../architecture/runtime-seams.md), [Backup and restore](../operations/backup-restore.md) |
| [0005](../../docs/adr/0005-lifecycle-transitions.md) | Lifecycle transitions are owned leases | [Runtime seams](../architecture/runtime-seams.md) |
| [0006](../../docs/adr/0006-idle-modes-are-data.md) | Idle modes are data, not strategies | [Runtime seams](../architecture/runtime-seams.md), [Deep sleep](../operations/deep-sleep.md) |
| [0007](../../docs/adr/0007-panel-sqlite-snapshots.md) | Daily SQLite snapshots and an explicit restore | [Persistence](../architecture/persistence.md) |
| [0008](../../docs/adr/0008-carbon-panel-brand-identity.md) | Use the approved lower-cut block and technical-capitals wordmark | [Brand assets](../../assets/branding/README.md) |
| [0009](../../docs/adr/0009-shared-design-system.md) | Proposed shared design-system source and Cloud snapshot | [Frontend change](../workflows/frontend-change.md) |
| [0010](../../docs/adr/0010-versioned-release-artifacts.md) | SemVer bundles, matching images and gated publication | [Release](../workflows/release.md) |
| [0011](../../docs/adr/0011-server-runtime-admission.md) | Share server start admission | [Server lifecycle admission](../architecture/server-lifecycle.md) |
| [0012](../../docs/adr/0012-product-transports.md) | Generated product control with tested HTTP compatibility | [RPC contract](../architecture/rpc-contract.md) |

# Rule

The ADR is authoritative for rationale. This index exists so an agent can find
the decision without reading every file. When an ADR is superseded, update this
table and mark the old concept `deprecated` rather than deleting it.
