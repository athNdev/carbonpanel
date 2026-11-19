---
type: Invariant
title: "Data safety — How to check"
description: "Look for remove, prune, rm, clean or volume deletion in the changed"
status: stable
owners: [oss-runtime]
code_refs: [internal/snapshot, internal/s3, internal/db, internal/module/manager.go, internal/rpc/services/server.go, internal/worldlib, internal/rpc/services/world.go, Makefile, docker-compose.yml, scripts/snapshot-panel-db.sh, scripts/compose-upgrade.sh, scripts/compose-upgrade-rollback.sh, scripts/web-build.sh, scripts/dev.sh, scripts/review/devstack.sh, internal/docscheck/upgrade_safety_test.go, internal/docscheck/compose_upgrade_test.go]
verified: { by: agent:opencode-world-library, at: 2026-11-18 }
sources:
  - title: Parent concept
    resource: data-safety.md
---
# How to check

- Look for `remove`, `prune`, `rm`, `clean` or volume deletion in the changed
  path. If it touches a world, backup or database path, stop.
- `internal/docscheck/upgrade_safety_test.go` must stay green: `make clean`,
  `make test`, `make build`, `scripts/web-build.sh`, `scripts/dev.sh` and
  `docker compose down` (without `--volumes`) do not delete worlds or the
  controller database. `scripts/review/devstack.sh clean` is the named
  destructive command for the disposable review stack only.
- Before a controller upgrade, `scripts/snapshot-panel-db.sh` (also
  `make snapshot-panel-db`) writes a timestamped SQLite copy beside the data
  directory. Rollback is `scripts/compose-upgrade-rollback.sh` (also
  `make compose-upgrade-rollback`): copy that file onto `carbon-panel.db` and
  leave worlds and bind mounts untouched. Ordinary test/build/compose paths
  must not invoke it.
- A Compose image upgrade uses `scripts/compose-upgrade.sh` (also
  `make compose-upgrade`). It recreates the panel with
  `docker compose up -d --pull always` and never a volumes-deleting compose
  down. A failed snapshot (disk-full or interrupted copy) stops before `up`.
  The shipped file bind-mounts data and backups, so worlds survive the
  container recreate. Helm and Terraform are not shipped.
- Named destructive cleanup for the disposable review stack is
  `make review-stack-clean` (`scripts/review/devstack.sh clean`). It is never
  a side effect of `make clean`, `make test` or `make build`.
- Backup and restore must be tested; a backup that cannot be restored is not a
  backup.
- Remote archive paths must not silently read the controller filesystem.
- Deleting a server removes its directory on the owning node's storage
  before the database row. A failed removal keeps the row so the delete
  can be retried. A remote node without shared storage does not have a
  controller-local directory removed in its place.
- Every delete writes a disposition row first and keeps it after the panel
  row is gone. The row records delete, keep_data, or detach, and it stays
  unsettled until that choice is recorded. A retry of a delete whose row is
  already gone only settles that record. It does not delete files again.
- Panel startup resumes an unsettled disposition. A pending delete continues
  the original choice. A disposition that already recorded the world outcome
  only removes the remaining panel row.
- `DeleteServer` with `keep_data` drops the panel record and container and
  leaves the world directory where it is. Sidecar module records are
  dropped without removing their containers, so module volumes are not
  purged. The audit row records that path.
- `DeleteServer` with `detach` leaves the container and the world directory
  and keeps that server's host port lease. The audit row records the
  container id and the path. `-operations release-port` frees that lease
  only after the panel row is gone, and it does not stop the container or
  delete the world.
