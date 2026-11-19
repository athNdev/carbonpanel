---
type: Invariant
title: "Data safety — How to check (part 2)"
description: "ExportWorld zips the server directory on the owning node's storage into"
status: stable
owners: [oss-runtime]
code_refs: [internal/snapshot, internal/s3, internal/db, internal/module/manager.go, internal/rpc/services/server.go, internal/worldlib, internal/rpc/services/world.go, Makefile, docker-compose.yml, scripts/snapshot-panel-db.sh, scripts/compose-upgrade.sh, scripts/compose-upgrade-rollback.sh, scripts/web-build.sh, scripts/dev.sh, scripts/review/devstack.sh, internal/docscheck/upgrade_safety_test.go, internal/docscheck/compose_upgrade_test.go]
verified: { by: agent:opencode-world-library, at: 2026-11-18 }
sources:
  - title: Parent concept
    resource: data-safety.md
---
- `ExportWorld` zips the server directory on the owning node's storage into
  a download and leaves the container, the world, and the panel row in
  place. Transport-only nodes zip through the injected storage resolver; a node
  with no worker is refused before a controller-local tree is created.
- World Library revisions are immutable and bound servers receive private
  clones. A bind or swap keeps the previous level family until verification
  succeeds, journals intent before mutating, and never auto-retries an
  uncertain outcome. Rejecting an import or a swap leaves the live world and
  the controller database unchanged. See
  [World Library](../architecture/world-library.md).
