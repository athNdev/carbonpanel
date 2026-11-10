---
type: Architecture
title: Runtime lifecycle and world-data seams
description: Locator, lifecycle handle, idle-power module, route PlanFor and worldstore are the five stable architecture seams.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/docker/locator.go
  - internal/docker/transition.go
  - internal/docker/idle.go
  - internal/proxy/plan.go
  - internal/worldstore
  - DESIGN.md
sources:
  - title: Design specification
    resource: ../../DESIGN.md
  - title: Node-to-daemon locator
    resource: ../../docs/adr/0004-docker-locator.md
  - title: Lifecycle transitions
    resource: ../../docs/adr/0005-lifecycle-transitions.md
  - title: Idle modes are data
    resource: ../../docs/adr/0006-idle-modes-are-data.md
  - title: Proxy route plans
    resource: ../../docs/adr/0004-proxy-route-plans.md
  - title: World-data seam
    resource: ../../docs/adr/0004-world-data-seam.md
---

# Shape

The controller concentrates five policies that used to be copied at call sites.
DESIGN.md §4.5 names the package path for each seam. A renamed or deleted
package fails knowledge-lint (`code_refs`) and `internal/docscheck`.

| Seam | Path | Rule |
| --- | --- | --- |
| Locator | `internal/docker/locator.go` | `Locator.For` / `ExecutorFor` map a node to a daemon. Failures are `ErrUnknownNode` or `ErrNoClient`, never `(nil, nil)`. |
| Lifecycle handle | `internal/docker/transition.go` | `TryBeginServerTransition` returns a `Transition`. Only that handle can release the claim. |
| Idle-power module | `internal/docker/idle.go` | Freeze and deep sleep share `idleTracker`, `idleMode` and `idleRunner`. |
| Route PlanFor | `internal/proxy/plan.go` | `PlanFor` is pure. The proxy manager applies the plan. |
| Worldstore | `internal/worldstore` | Snapshot, backup and restore of world data go through this package. Transport-only nodes stream backup and restore through `nodestorage.ForNode`. |

# Why

Without one module per rule, fail-closed Docker behaviour, lifecycle locking,
idle sweep and restore atomicity have to be patched in several adapters.

# Read next

- [Recorded architecture seams](../invariants/architecture-seams.md)
- [Node resolution](node-resolution.md)
- [TCP proxy](proxy.md)
- [Backup and restore](../operations/backup-restore.md)
