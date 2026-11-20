# Acceptance evidence contract (OSS)

`oss-acceptance-evidence.json` is a machine-readable **opt-in record** of work
items whose acceptance has already been verified. Its `gate.scope` is
`record-auditor`: the validator and the scheduled workflow check the recorded
PR/CI/deploy/live chain for these candidates only. They **do not read Jira,
compare Done transitions, or prevent a Done click**. The native team-managed
workflow is the only blocking boundary.

An empty `candidates` array is a configuration failure, not a successful no-op:
validation fails closed and the drift job exits non-zero when nothing was
audited. A real reference run is also required so the scheduled audit proves it
can reach GitHub even when few candidates exist.

Each candidate declares `closure_state`:

- `proposed` (the default when omitted) — the live check must be fresh within
  `max_evidence_age_days`. This is the path for a closure being requested now.
- `recorded` — a historical record whose live observable was verified when it
  closed. Its recorded `checked_at` stays valid indefinitely; the scheduled
  audit rechecks immutable GitHub facts and the reference run, never the age of
  a past URL check. The recorded observable must remain byte-identical to the
  evidence artifact; refreshing it means updating both deliberately.

The weekly cron therefore does not turn red merely because a long-closed item's
URL check is older than seven days, and it needs no edits to closed history.

## Tiers

| Tier | Meaning |
| --- | --- |
| `CODE-MERGED` | Merged code only. Never sufficient for a public/live surface. |
| `CI-GREEN` | Merged code plus exact-commit green CI. Minimum for `internal`. |
| `BUILD-ONLY` | Build succeeded; no deploy/live proof. |
| `CAPTURE-ONLY` | A capture exists; live status not verified. |
| `DEPLOY-SKIPPED` | Deploy/publication job explicitly skipped. |
| `LIVE-VERIFIED` | Merge SHA + CI + the **named** deploy job at the exact qualified revision + fresh live URL status. |

An `internal` surface requires at least `CI-GREEN`; `build` requires
`BUILD-ONLY`; `public` and `live` require `LIVE-VERIFIED`. A weaker tier is
permitted only with an explicit, well-formed `exception` (kind, reason,
`recorded_at`, reviewer) whose kind matches the tier. The verifier must not
equal the merge author.

`LIVE-VERIFIED` is strict: `deploy.job` must name the publishing job, that job
must be present with `conclusion == success`, the deploy run SHA must equal the
recorded `revision_sha`, and the revision must be either the PR merge SHA or a
separately recorded `trusted_deploy_sha`. The GitHub audit verifies the run's
workflow name against `deploy.workflow` and uses GitHub's commit comparison to
confirm the trusted SHA descends from the PR merge. A skipped, failed or missing
named job and a build-only run all fail closed. Optional jobs may be skipped;
no job may fail, be cancelled or time out.

## Adding a Done candidate

1. Add a candidate to `candidates[]` with the issue key, surface, tier, PR
   number, full merge SHA, CI run id, the named deploy job and revision, live
   reference, verifier and verification time.
2. Add `docs/acceptance/evidence/<ISSUE>.json` with the observed merge, CI,
   build, deploy and live facts. Keep the live capture in
   `docs/acceptance/evidence/` and reference it from the artifact.
3. Run `go run ./cmd/acceptance-evidence validate`. It fails closed on missing,
   stale (wrong SHA), skipped/failed/missing named deploy, unrecorded exception,
   or a verifier who is also the author.

```sh
go run ./cmd/acceptance-evidence validate
go run ./cmd/acceptance-evidence verify-github   # scheduled record audit; GITHUB_TOKEN from the runner
```

## Qualified record in this repository

OSS-409 is a `recorded` historical public-docs rollout: PR #575 merged at
`dbbd22e25ee55f8bc0624eeb45801765819fa6db`; exact-commit CI run `44517658883`
and Pages deploy run `44522728666` both at the trusted descendant
`69ddb46d596aea8ed117a3dba6a1f63521572664`; job `deploy` succeeded and the
optional `notify-indexnow` job was skipped; the authoritative public index at
`https://carbonpanel.dev/version-index.json` returned 200. Its evidence is in
`docs/acceptance/evidence/OSS-409.*`. This record is audited, not re-litigated:
the audit proves the verifier path works against real GitHub runs.

## Native guard and its limit

The live OSS team-managed workflow (project 10001) has a blocking `ALL` condition
group on the global Done edge: most-recent status must be In Review, and only the
Administrator project role may move to Done. A validator requires the description
to contain a `Completion Evidence:` section with a durable http(s) URL. Reopening
and all non-Done transitions are unchanged.

Limits stated plainly:

- OSS has a single human actor, so the role condition proves *who may close*, not
  author/reviewer independence. A distinct reviewer actor must be added before
  claiming separation.
- A dedicated custom field could not be created through the field API; the
  native evidence check is a structured Description section, not a purpose-built
  field, and cannot judge truthfulness or freshness of the URL.
- GitHub Actions cannot gate a Jira transition. This workflow audits an opt-in
  record and reports drift; it does not read Jira, compare Done closures, or
  undo a transition. A fabricated but well-formed URL can pass the native check;
  only the separately recorded evidence makes that detectable.
