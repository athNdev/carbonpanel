---
type: Runbook
title: Backup and restore
description: Where panel database snapshot, backup storage, restore and retention are specified.
status: stable
owners: [oss-runtime]
code_refs: [internal/s3, internal/snapshot, internal/scheduler, internal/worldstore, scripts/snapshot-panel-db.sh, scripts/compose-upgrade.sh, scripts/compose-upgrade-rollback.sh, docker-compose.yml]
sources:
  - title: Streaming limits and recovery
    resource: ../../docs/storage-streaming.md
  - title: Backup confirmation issue
    resource: ../../docs/issues/013-UI-5-backup-confirmation-dialogs.md
  - title: Snapshot integrity issue
    resource: ../../docs/issues/015-DEP-3-snapshot-integrity-concurrency-retention.md
---

# Parts

- [Backup and restore — Use this when + Rules + Known gap + Read next](backup-restore-use-this-when.md) — A backup, snapshot, restore or retention change is being made.

# Key facts

- A backup, snapshot, restore or retention change is being made.
- Scheduled backup archives, snapshot create/restore, and scheduled modpack
- Panel database snapshots and the offline restore command are specified in
- A backup is not complete until it can be restored. Test the restore path.
- `docs/runbook-panel-db-restore.md`.
- Before a controller upgrade, run the named operator script
- `scripts/snapshot-panel-db.sh` (or `make snapshot-panel-db`). It writes
- `carbon-panel-db-<timestamp>.sqlite` beside the data directory. Restore by
- copying that file onto `database.path` with the panel stopped; do not touch
- `docker compose up`/`down` do not take or restore that snapshot.
- `scripts/compose-upgrade.sh --apply` snapshots then recreates the panel
- container with `up -d --pull always`; a failed snapshot stops before `up`.
- verify, encrypt, upload, retain). The retired builtin-mc-backup sidecar is
- S3 credentials and any backup keys come from configuration, never from
- A restore must not overwrite a live world without an explicit confirmation.
- total limits, plus 500,000 entries and a 1 MiB manifest. Backup refuses a
- manifest restore would reject. Existing restore stage/old trees and failed
- [ADR 0007](../../docs/adr/0007-panel-sqlite-snapshots.md) and
- Host data directories stay in place. Restore with
- `scripts/compose-upgrade-rollback.sh` (copy-back, not a prune).
