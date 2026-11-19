# ADR 0041: Bounded trusted-main nightly publication

- Status: Accepted
- Date: 2026-11-19
- Owners: oss-runtime
- Relates to: ADR 0033 (release tag and publication policy), OSS-366 stable line,
  OSS-391 (dev-nightly lane)

## Context

The `nightly` development alias existed but required an explicit manual
`workflow_dispatch`, so a missed dispatch left it pointing at a stale commit.
Stable and prerelease publication stays with the tagged release workflow and
must not be disturbed. Native GitHub Actions scheduling is the only approved
cadence; no extra automation system or infrastructure setting is introduced.

## Decision

`cd.yml` gains one native `schedule` trigger (18:00 UTC daily, ahead of the
06:30 Australia/Sydney local updater). The schedule publishes ONLY the
`nightly` alias and ONLY from the exact current `main` tip:

1. Resolve the main tip read-only twice; a moving or unavailable tip pins
   nothing and the run is skipped.
2. Check out and assert that exact SHA, require a completed successful push CI
   run for that SHA, and re-read the tip plus re-verify CI again immediately
   before the buildx push and once more before the alias move. The alias move
   uses the FRESH pre-alias `verified` output and gates on it, so a CI result
   that turned non-green after the push cannot authorize the alias move.
3. Preflight the shared `sha-<commit>` image with strict, digest-anchored
   both-architecture evidence. A present-but-unverified or unknown reference is
   SKIPPED with zero writes; a verified reference is reused, never retargeted.
4. Derive the previous nightly binding from the live alias's own digest-
   addressed platform configs, then still require independent sha identity,
   ancestry and digest checks. Manual dispatch keeps its original explicit
   binding rule and refuses an absent binding.
5. Publication requires a separately reviewed, default-off repository variable
   `NIGHTLY_SCHEDULE_PUBLISH == 'true'`. Unset or false means verification
   only; this repository does not set or mutate the variable. Push, PR,
   workflow_run, tag and dispatch events never use the schedule gate.
6. Scheduled publishes join the same fixed, non-cancellable `cd-publish-edge`
   concurrency group as manual publishes, giving a single-writer proof on the
   shared sha identity and aliases. Verification builds stay per-SHA and
   cancellable.

No schedule trigger is added to the release, module, MCP distribution,
runtime-artifact or any other workflow. `latest`, stable minor tags, `edge`,
GitHub releases, release records, GHCR and MCP/runtime/module images are never
written by this path.

## Consequences

A daily verified main commit refreshes `nightly` without an operator dispatch,
while a moving tip, failed CI, unverified image or unset switch leaves every
registry untouched. The local updater can pin a fresh immutable digest once the
switch is deliberately enabled by an operator; enabling it is a separate,
reviewed action, not part of this change.
