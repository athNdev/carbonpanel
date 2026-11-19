---
type: Invariant
title: "Data safety — Review"
description: "Reviewed 2026-10-24 by agent:mimo-v2.6-pro against the code and attested by"
status: stable
owners: [oss-runtime]
code_refs: [internal/snapshot, internal/s3, internal/db, internal/module/manager.go, internal/rpc/services/server.go, internal/worldlib, internal/rpc/services/world.go, Makefile, docker-compose.yml, scripts/snapshot-panel-db.sh, scripts/compose-upgrade.sh, scripts/compose-upgrade-rollback.sh, scripts/web-build.sh, scripts/dev.sh, scripts/review/devstack.sh, internal/docscheck/upgrade_safety_test.go, internal/docscheck/compose_upgrade_test.go]
verified: { by: agent:opencode-world-library, at: 2026-11-18 }
sources:
  - title: Parent concept
    resource: data-safety.md
---
# Review

Reviewed `2026-10-24` by `agent:mimo-v2.6-pro` against the code and attested by
`human:athNdev`:

- `make clean` removes only the build binary and states that server data and
  Docker volumes are preserved; `proto-clean` removes generated proto output.
- `make help` describes clean as preserving worlds and databases.
- Compose bind-mounts host data; `docker compose down` without `--volumes`
  leaves those mounts in place (`docker-compose.yml`).
- Snapshot retention prunes only expired snapshots and protects the most recent
  copy (`internal/snapshot/integrity_retention_test.go`).
- Restore coverage exists in `internal/snapshot`.
- Upgrade snapshots and copy-back rollback are covered by
  `internal/docscheck/upgrade_safety_test.go`. Compose bind-mount upgrades,
  snapshot-failure abort-before-up, copy-back rollback, and the unshipped
  Helm/Terraform claim are covered by `internal/docscheck/compose_upgrade_test.go`.

The remote archive path gap is tracked in
[known gaps](../failures/known-gaps.md).
