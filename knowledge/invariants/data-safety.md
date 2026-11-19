---
type: Invariant
title: Data safety
description: Worlds, databases, backups and snapshots are persistent user data and must survive build, development and cleanup commands.
status: stable
owners: [oss-runtime]
code_refs: [internal/snapshot, internal/s3, internal/db, internal/module/manager.go, internal/rpc/services/server.go, internal/worldlib, internal/rpc/services/world.go, Makefile, docker-compose.yml, scripts/snapshot-panel-db.sh, scripts/compose-upgrade.sh, scripts/compose-upgrade-rollback.sh, scripts/web-build.sh, scripts/dev.sh, scripts/review/devstack.sh, internal/docscheck/upgrade_safety_test.go, internal/docscheck/compose_upgrade_test.go]
sources:
  - title: Contributor guide
    resource: ../../AGENTS.md
  - title: Backup confirmation issue
    resource: ../../docs/issues/013-UI-5-backup-confirmation-dialogs.md
  - title: Panel database restore runbook
    resource: ../../docs/runbook-panel-db-restore.md
  - title: Deployment modes
    resource: ../../docs/deployment-modes.md
verified: { by: agent:opencode-world-library, at: 2026-11-18 }
review_after: 2027-01-23
---

# Parts

- [Data safety — Rule + Why](data-safety-rule.md) — A build, development or cleanup command must preserve server worlds, the
- [Data safety — How to check](data-safety-how-to-check.md) — Look for remove, prune, rm, clean or volume deletion in the changed
- [Data safety — How to check (part 2)](data-safety-how-to-check-2.md) — ExportWorld zips the server directory on the owning node's storage into
- [Data safety — Review](data-safety-review.md) — Reviewed 2026-10-24 by agent:mimo-v2.6-pro against the code and attested by

# Key facts

- The shipped file bind-mounts data and backups, so worlds survive the
- A build, development or cleanup command must preserve server worlds, the
- Worlds and databases are the user's data. Re-creating a container is routine;
- `docker compose down` (without `--volumes`) do not delete worlds or the
- before the database row. A failed removal keeps the row so the delete
- Every delete writes a disposition row first and keeps it after the panel
- row is gone. The row records delete, keep_data, or detach, and it stays
- `DeleteServer` with `keep_data` drops the panel record and container and
- leaves the world directory where it is. Sidecar module records are
- `DeleteServer` with `detach` leaves the container and the world directory
- delete the world.
- controller database, backups and snapshots. A destructive operation must be
- losing a world is not recoverable by rebuilding the controller.
- path. If it touches a world, backup or database path, stop.
- `make snapshot-panel-db`) writes a timestamped SQLite copy beside the data
- leave worlds and bind mounts untouched. Ordinary test/build/compose paths
- Named destructive cleanup for the disposable review stack is
- unsettled until that choice is recorded. A retry of a delete whose row is
- already gone only settles that record. It does not delete files again.
- Panel startup resumes an unsettled disposition. A pending delete continues
- the original choice. A disposition that already recorded the world outcome
- dropped without removing their containers, so module volumes are not
- and keeps that server's host port lease. The audit row records the
- a download and leaves the container, the world, and the panel row in
- World Library revisions are immutable and bound servers receive private
- clones. A bind or swap keeps the previous level family until verification
- uncertain outcome. Rejecting an import or a swap leaves the live world and
- [World Library](../architecture/world-library.md).
- `make clean` removes only the build binary and states that server data and
- `make help` describes clean as preserving worlds and databases.
