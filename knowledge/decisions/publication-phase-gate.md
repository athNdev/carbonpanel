---
type: Decision
title: Explicit phase-result gate for every publication write
description: Every release side-effect phase checks direct results and verification outputs, and a terminal job fails an explicit publish when a required phase was skipped.
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows/release.yml
  - scripts/release.py
  - scripts/test_release.py
sources:
  - title: Channel promotion decision
    resource: ../../docs/adr/0052-channel-promotion-gates.md
  - title: Release operations and versioning
    resource: ../../docs/releases.md
---

# Decision

- The staged publish path deliberately skips the whole build DAG
  (`frontend`/`binaries`/`image`/`rehearse-stable-docs`) and consumes the pinned
  staged artifacts. A default GitHub `success()` condition on a downstream job
  therefore skips every write phase, so a run can be green while publishing
  nothing. This happened in trusted runs 44738205399 and 44739581869.
- Every side-effect phase (`prepare-draft`, `stable-channel-transaction`,
  `promote-aliases`, `publish`, `publish-minor`) uses explicit `always()` plus
  `!cancelled()`, direct `needs.<phase>.result == 'success'` checks on its own
  prerequisites, and the verification outputs of the phases it consumes.
  Skipped-by-design ancestors are tolerated only where the staged identity
  (`artifact_run` == `staged_run`) or an actually successful image proves the
  inputs.
- A terminal `verify-publication-phases` job runs after every phase and fails
  an explicit publish if any required phase is skipped/failed/cancelled or a
  required output (immutable verification, private draft id, transaction
  action, alias verification) is missing. Rehearsals must have no successful
  write phase. The gate runs from the trusted workflow SHA, never from the
  pinned candidate commit, so the candidate cannot select its own trust logic.
- Operator intent is the trusted dispatch event plus the raw `publish` input;
  the Python plan's `publish`/`channel` outputs are only a decision. An explicit
  request requires plan result `success`, an affirmative decision and a
  recognized channel (`stable`/`prerelease`); a blank/false decision or unknown
  job result can never be reported as a green rehearsal. Stable transaction
  outcomes are allowlisted to `publish`/`noop`; `reconcile`, `rollback`,
  `refuse`, `stage` and empty are not positive publication results.
- No registry, Pages, tag or release write is added or bypassed by this fix; it
  only makes a silent no-op publication impossible to report as success.

# Read next

- [Channel promotion recovery](channel-promotion-recovery.md)
- [Release staging artifact contract](release-staging-artifact-contract.md)
- [Release](../workflows/release.md)
