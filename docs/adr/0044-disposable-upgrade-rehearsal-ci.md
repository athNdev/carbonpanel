# ADR 0044: Recurring disposable real-binary upgrade rehearsal in CI

- Status: Accepted
- Date: 2026-11-19
- Owners: oss-runtime
- Relates to: ADR 0010 (versioned release artifacts), ADR 0013 (bounded
  storage), ADR 0033 (release tag and publication policy), OSS-366 stable line,
  OSS-395 product version contract, OSS-406 acceptance coverage matrix

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

`scripts/upgrade_rehearsal_ci.py` performs TWO cases entirely inside a
caller-owned disposable workspace (a GitHub `$RUNNER_TEMP` directory):

1. **Legacy baseline** — the explicit `v1.0.40` migration path from the release
   contract constants. Download the verified archive, `checksums.txt` and
   `release-manifest.json` from the public release and require the archive SHA
   to match **both** records and the manifest version/commit to equal the pinned
   legacy baseline. Assert the documented 35 → 44 → 35 table shape.
2. **Current stable** — the highest published product stable derived from the
   authoritative GitHub release record through the OSS-395 product version
   contract (`published_stable_versions()` / `verified_previous_release_tag()`
   in `scripts/release.py`), never from a hardcoded literal. Its archive and
   records are verified the same way; its commit comes from the verified
   manifest. When a new stable is published the weekly case follows it. No
   table count is hardcoded here: the real binary defines its own baseline and
   the candidate schema must be an additive superset of it. If the derived
   stable is currently `v1.0.40`, both cases run independently on separate
   disposable paths and the report records that they share a baseline. A
   release state that cannot be established fails closed.
3. Build a real candidate `carbon-panel` binary from the checkout with the pure
   Go (`CGO_ENABLED=0`) release settings and require its `--version` to carry
   the requested version and commit.
4. In each case, seed the world/backup files BEFORE starting the real stable
   binary, back up the DB with the real SQLite online backup API alongside the
   hashed world copies, run the **real** candidate binary against a disposable
   copy, and assert integrity, additive table growth, core row preservation and
   world-byte identity after the upgrade, the app-level restore and the
   copy-back restore. A forced-failure (missing snapshot) restore must be
   refused without mutation. These checks are per case; none can be skipped.
5. Execute the checked-in model rehearsal and staged planner as extra
   deterministic checks, and emit a JSON evidence report containing exact
   artifact versions and SHA-256 digests for both stable archives, the candidate
   binary and the verification records. A failure always writes a failing
   evidence report rather than leaving a green-looking gap.

Every check is aggregated; any failed check exits non-zero. There is no
`continue-on-error` and no skip path that reports green without running the
real binaries. `scripts/test_upgrade_rehearsal_ci.py` covers the fail-closed
fixture and mutation-detection helpers offline.

## Consequences

- Schema drift and a broken migration or restore surface on weekly scheduled
  runs and on PRs that touch the migration/config/release surfaces.
- The job proves real-binary schema/row/world preservation on disposable data
  for both the fixed legacy baseline and the moving current stable. It does
  **not** prove gameplay, Docker daemon behavior, OIDC/PROXY migration steps or
  first-stable publication readiness; those stay separate gates.
- Build caches are the only writes outside the disposable workspace; no live
  world, database, host, registry, tag or release is written.
- The stable publisher (OSS-366), tag line and registry aliases are untouched.
  Adding this job changes no release approval rule and grants no write scope.
