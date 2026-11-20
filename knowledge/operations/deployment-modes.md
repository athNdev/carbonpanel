---
type: Runbook
title: Deployment modes
description: Which install paths are shipped (Compose and native binary), which are not (Helm, Terraform), and how Compose upgrade and copy-back rollback keep host data.
status: stable
owners: [oss-runtime]
code_refs:
  - docker-compose.yml
  - .github/workflows/compose-release-qualification.yml
  - scripts/qualify-compose-artifacts.sh
  - scripts/qualify-compose-release.sh
  - scripts/qualify-compose-game.mjs
  - scripts/compose-upgrade.sh
  - scripts/compose-upgrade-rollback.sh
  - scripts/snapshot-panel-db.sh
  - Makefile
  - internal/docscheck/upgrade_safety_test.go
sources:
  - title: Deployment modes matrix
    resource: ../../docs/deployment-modes.md
  - title: Panel database restore
    resource: ../../docs/runbook-panel-db-restore.md
---

# Use this when

Changing compose, the panel image build, an upgrade script, or any document
that names Helm, Terraform or a deployment mode.

# Rules

- The shipped OSS install is Docker Compose (`docker-compose.yml`) or a native
  binary. Host directories listed in the compose file hold the panel database
  and server files across container recreates.
- Upgrade a Compose install with `scripts/compose-upgrade.sh`: snapshot the
  controller database, then `docker compose up -d --pull always`. If the
  snapshot fails (disk-full or interrupted copy), stop before compose up.
  Do not pass a volumes-delete flag. That is how an upgrade avoids deleting worlds.
- Roll back the controller database with
  `scripts/compose-upgrade-rollback.sh`: copy a snapshot-panel-db artifact onto
  `carbon-panel.db`. That is copy-back, not a prune. World bind mounts stay.
- Helm, Terraform and Kubernetes manifests are **not shipped**. Do not add an
  upgrade procedure for them until the artifact exists in the tree.
- Use the hosted Compose qualification workflow for host and bridge game
  joins, isolated snapshot faults, upgrade and copy-back. Identify candidate
  runs by exact staged product SHA, Actions archive hashes and tested image
  digest. Rerun against the final product SHA for release qualification.

# Read next

- [Data safety](../invariants/data-safety.md)
- [Backup and restore](backup-restore.md)
