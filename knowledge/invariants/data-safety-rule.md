---
type: Invariant
title: "Data safety — Rule + Why"
description: "A build, development or cleanup command must preserve server worlds, the"
status: stable
owners: [oss-runtime]
code_refs: [internal/snapshot, internal/s3, internal/db, internal/module/manager.go, internal/rpc/services/server.go, internal/worldlib, internal/rpc/services/world.go, Makefile, docker-compose.yml, scripts/snapshot-panel-db.sh, scripts/compose-upgrade.sh, scripts/compose-upgrade-rollback.sh, scripts/web-build.sh, scripts/dev.sh, scripts/review/devstack.sh, internal/docscheck/upgrade_safety_test.go, internal/docscheck/compose_upgrade_test.go]
verified: { by: agent:opencode-world-library, at: 2026-11-18 }
sources:
  - title: Parent concept
    resource: data-safety.md
---
# Rule

A build, development or cleanup command must preserve server worlds, the
controller database, backups and snapshots. A destructive operation must be
explicitly named and separately invoked; it is never a side effect.

# Why

Worlds and databases are the user's data. Re-creating a container is routine;
losing a world is not recoverable by rebuilding the controller.
