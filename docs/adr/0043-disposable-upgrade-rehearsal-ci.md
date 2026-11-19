# ADR 0043: Recurring disposable real-binary upgrade rehearsal in CI

- Status: Accepted
- Date: 2026-11-19
- Owners: oss-runtime
- Relates to: ADR 0010 (versioned release artifacts), ADR 0013 (bounded
  storage), ADR 0033 (release tag and publication policy), OSS-366 stable line,
  OSS-406 acceptance coverage matrix

## Context

The v1.0.40 → 2.0.0 migration policy documents additive SQLite schema growth
(35 → 44 tables) and a copy-back/restore rollback. A real-binary rehearsal
existed only as evidence on the OSS-366 PR head, and the checked-in
`scripts/rehearse_upgrade.py` is explicitly a synthetic MODEL. Nothing in CI
ran an upgrade rehearsal, so a schema regression could merge without a live
check. The rehearsal must not become a publication gate or touch the stable
publisher, live worlds, a host or a registry (OSS-406 implementation rules).

## Decision

`.github/workflows/upgrade-rehearsal.yml` is a **read-only, build-only** job on a
weekly `schedule` plus a path-gated `pull_request` and a manual
`workflow_dispatch`. It has only `contents: read` and `actions: read`; it never
dispatches another workflow, pushes an image, creates a tag or writes a release.

`scripts/upgrade_rehearsal_ci.py` performs the rehearsal entirely inside a
caller-owned disposable workspace (a GitHub `$RUNNER_TEMP` directory):

1. Download the verified `v1.0.40` archive, `checksums.txt` and
   `release-manifest.json` from the public release and require the archive SHA
   to match **both** records and the manifest version/commit to equal the
   pinned stable. A missing or mismatched fixture fails the job.
2. Build a real candidate `carbon-panel` binary from the checkout with the pure
   Go (`CGO_ENABLED=0`) release settings and require its `--version` to carry
   the requested version and commit.
3. Run the **real** stable binary against a seeded disposable config to create
   the 35-table DB, back it up with the real SQLite online backup API alongside
   hashed world copies, run the **real** candidate binary against a disposable
   copy to apply the additive migration, verify 35 → 44 tables, integrity, core
   row preservation and world-byte identity, and run forced-failure (missing
   snapshot) and real app-level (`-restore-panel-backup`) plus copy-back
   restores back to 35 tables.
4. Execute the checked-in model rehearsal and staged planner as extra
   deterministic checks, and emit a JSON evidence report containing exact
   artifact versions and SHA-256 digests for the stable archive, both binaries
   and the verification records.

Every check is aggregated; any failed check exits non-zero. There is no
`continue-on-error` and no skip path that reports green without running the
real binaries. `scripts/test_upgrade_rehearsal_ci.py` covers the fail-closed
fixture and mutation-detection helpers offline.

## Consequences

- Schema drift and a broken migration or restore surface on weekly scheduled
  runs and on PRs that touch the migration/config/release surfaces.
- The job proves real-binary schema/row/world preservation on disposable data.
  It does **not** prove gameplay, Docker daemon behavior, OIDC/PROXY migration
  steps or first-stable publication readiness; those stay separate gates.
- The stable publisher (OSS-366), tag line and registry aliases are untouched.
  Adding this job changes no release approval rule and grants no write scope.
