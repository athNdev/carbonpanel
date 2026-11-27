---
type: Architecture
title: "Map viewing — Rule (part 2)"
description: "Managed BlueMap install and private discovery share one node eligibility"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/worldview.proto, internal/worldview, internal/bluemapdiscovery, internal/rpc/services/module_bluemap.go, web/carbon-panel/src/routes/worlds, web/carbon-panel/src/lib/components/worldview]
sources:
  - title: Parent concept
    resource: world-viewing.md
  - title: Installed BlueMap discovery
    resource: ../../docs/adr/0040-bluemap-installed-module-discovery.md
---
Managed BlueMap install and private discovery share one node eligibility
check. It accepts legacy controller aliases and enabled registered nodes
marked local in persisted state. Discovery uses the controller Docker client,
checks the server's current node assignment and opens only its confined storage.
Managed startup holds the server transition through its bounded Docker call
and terminal state write. Manual Start, Restart, Recreate and managed updates
share that lease and recheck node eligibility. Async recreation keeps the lease.
Only the guarded installer sets the persisted managed marker. Module metadata
updates preserve that decision. A canceled post-create write records the known
container ID and stopped state so a later Start can reuse it.
