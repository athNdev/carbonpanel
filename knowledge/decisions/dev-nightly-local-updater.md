---
type: Decision
title: Scoped local dev-nightly updater
description: Digest identity, scope and rollback rules for the local WSL Compose updater.
status: stable
owners: [oss-runtime]
code_refs:
  - scripts/Update-CarbonPanelDev.ps1
  - scripts/test-update-carbonpaneldev.ps1
sources:
  - title: Release operations and versioning
    resource: ../../docs/releases.md
  - title: Bounded trusted-main nightly publication
    resource: ../../docs/adr/0041-bounded-trusted-main-nightly.md
---

# Decision

The local updater is a Windows PowerShell tool for ONE existing install:
Compose project `carbon-panel-host`, service `carbon-panel`, loopback
`127.0.0.1:8095`, Docker relay `tcp://[::1]:2375`. Production scope accepts no
target overrides. Fixture scope is task-owned qualification only, requires a
fixture marker label and uses a local manifest instead of live services.

Three registry digest objects are never conflated: the multi-platform **index
digest**, the per-platform **child manifest digest**, and the **image config
digest**. The pull is pinned to the index; the local package is selected by the
daemon platform child; and `image inspect .Id` / `container inspect .Image`
(config IDs) are compared ONLY with the child's config digest. A config ID is
never compared with an index digest.

A latest-main update requires exact identity with current main (both GitHub
`compare` counts zero). A separate local forward-only check refuses a target
below the running commit. Missing CI, a single architecture, an unbound digest
or a downward target all refuse before any pull or write.

The default is dry-run. With `-Apply`, a durable lock is taken before the state
baseline, the SQLite controller database is snapshotted with the online backup
API, a bounded rollback image is preserved, and only the scoped service is
recreated with `up -d --no-deps --pull never --force-recreate`. Health,
mounts, config ID and data are re-verified. On failure, restore safety is decided from OBSERVED DATABASE STATE, never from
the wording of the error. A baseline state (user_version, table/row counts and
a database-plus-WAL fingerprint) is captured immediately after the verified
backup and compared with the state at failure time. If the database is
unchanged, only the old image is recreated. If `user_version` increased, a
schema migration committed after the snapshot: automatic restore is withheld,
no image recreation is attempted, and the operator gets an explicit fail-closed
message because overwriting that newer state could destroy legitimate data. Any
other post-snapshot change is ambiguous and also fails closed in Production;
only a fixture-injected, explicitly marked loss may authorize a real restore.
A permitted restore stops ONLY the scoped service, restores the verified
backup, then recreates the old image and verifies identity, mounts, health and
data. Mismatches and failed checks are errors, not reported success. Worlds,
other containers, game servers and WSL services are never touched.

Live use requires a trusted nightly publication and exact tool review. The
existing 06:30 Sydney Codex automation is guarded until the reviewed script is
installed and first-run verification exists; no duplicate scheduler is added.
