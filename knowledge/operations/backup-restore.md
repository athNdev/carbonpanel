---
type: Runbook
title: Backup and restore
description: Where backup storage, snapshots and restore are specified.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/s3
  - internal/snapshot
  - internal/scheduler
  - internal/worldstore
  - scripts/snapshot-panel-db.sh
  - scripts/compose-upgrade.sh
  - scripts/compose-upgrade-rollback.sh
  - docker-compose.yml
sources:
  - title: Backup confirmation issue
    resource: ../../docs/issues/013-UI-5-backup-confirmation-dialogs.md
  - title: Snapshot integrity issue
    resource: ../../docs/issues/015-DEP-3-snapshot-integrity-concurrency-retention.md
---

# Use this when

A backup, snapshot, restore or retention change is being made.

# Rules

- Backups are native: the Backups tab, scheduled tasks and the CreateBackup
  RPC all run the same journaled pipeline (quiesce, manifest archive,
  verify, encrypt, upload, retain). The retired builtin-mc-backup sidecar is
  no longer seeded; its template row is removed only while no module
  references it, so existing installs keep working until their owners
  remove them.
- A backup is not complete until it can be restored. Test the restore path.
- S3 credentials and any backup keys come from configuration, never from
  tracked source.
- Retention must not delete the only surviving copy of a world.
- A restore must not overwrite a live world without an explicit confirmation.

# Known gap

Scheduled backup archives, snapshot create/restore, and scheduled modpack
sync use the mapped world root. Mod installation and packwiz bake write the
mods folder on that same root. Deleting a server still removes the stored
data path.

Panel database snapshots and the offline restore command are specified in
[ADR 0007](../../docs/adr/0007-panel-sqlite-snapshots.md) and
`docs/runbook-panel-db-restore.md`.

Before a controller upgrade, run the named operator script
`scripts/snapshot-panel-db.sh` (or `make snapshot-panel-db`). It writes
`carbon-panel-db-<timestamp>.sqlite` beside the data directory. Restore by
copying that file onto `database.path` with the panel stopped; do not touch
worlds or bind mounts. `make test`, `scripts/web-build.sh`, `make clean` and
`docker compose up`/`down` do not take or restore that snapshot.
`scripts/compose-upgrade.sh --apply` snapshots then recreates the panel
container with `up -d --pull always`; a failed snapshot stops before `up`.
Host data directories stay in place. Restore with
`scripts/compose-upgrade-rollback.sh` (copy-back, not a prune).

# Read next

- [Data safety](../invariants/data-safety.md)
- [Known failure modes](../failures/known-gaps.md)
