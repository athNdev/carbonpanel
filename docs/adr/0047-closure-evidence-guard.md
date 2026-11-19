# ADR 0047: Block OSS closure at the native Done edge, verify evidence in the repo

Status: Accepted

## Context

An audit of OSS closures found work items marked Done on the strength of a
merged PR and green CI alone, while the original acceptance required a public
or installed result. Jira OSS (project id 10001) is a team-managed project whose
workflow exposed a global, unconditional transition to Done (transition id 51);
any issue could be closed directly from To Do, with no screen, no required field,
no condition and no validator. A policy document or a repository check after the
fact could only describe that gap, not prevent it.

The native team-managed workflow editor does support blocking rules, exposed by
the documented workflow API (`/rest/api/3/workflows`, `/workflows/update/validation`,
`/workflows/update`, `/workflows/capabilities`). The site confirmed the built-in
rule keys for the previous-status condition, the transition-role restriction and
the field validator. GitHub Actions cannot gate a Jira transition at all.

## Decision

Apply native blocking rules to the global Done edge in OSS as the authoritative
boundary, and keep a fail-closed repository verifier plus scheduled drift audit
as a second layer.

The native condition group uses `operation: ALL` with:
- `system:previous-status-condition` — most recent status must be In Review
  (`10004`), `includeCurrentStatus=true`, `ignoreLoopTransitions=true`;
- `system:restrict-issue-transition` — only the Administrator project role
  (`10009`) may move an item to Done.

The validator is `system:validate-field-value` with
`ruleType=fieldMatchesRegularExpression` on `description`, requiring a
`Completion Evidence:` section containing a durable http(s) URL. A dedicated
custom field was attempted but the field API rejected creation, so the structured
Description section is the interim evidence reference. Reopening and every
non-Done transition are unchanged. Updates are version-checked and validated
before publication; only the project-scoped workflow is modified.

`docs/acceptance/oss-acceptance-evidence.json` is the machine-readable contract.
Each Done candidate names a PR and merge SHA, exact CI run id and, for public or
live acceptance, a deploy run and fresh live URL status. Evidence is graded into
six tiers; weaker-than-required tiers need an explicitly recorded exception.
`internal/acceptanceevidence` fails closed on missing, stale (wrong SHA), skipped
deploy, unrecorded exception, or a verifier who is also the author.
`.github/workflows/acceptance-evidence.yml` runs the validator on changes and on
a weekly schedule, and audits PR/CI/deploy references against GitHub.

## Consequences

Direct, unreviewed and unevidenced Done transitions are now blocked in Jira;
this was proved with REST transitions on a disposable QA issue (400 for each
negative case, 204 for a reviewed close with evidence, 204 for reopen, 400 for
a stale post-reopen close). The guarantee is **native and blocking**, but it is
not complete independence: OSS has one human actor, so the role condition shows
who may close but cannot prove author/reviewer separation. The Description regex
proves a durable URL was supplied, not that the linked evidence is truthful or
current; that is what the repository verifier and drift audit cover.

GitHub Actions still cannot undo a Done click made directly in Jira. The
workflow is a merge/label gate and a drift detector, not a Jira gate; claiming
otherwise would be false. Adding a distinct reviewer actor and a genuine
project-scoped evidence field are follow-ups that would remove both limitations.
Rollback is a versioned workflow update restoring transition 51 without the
condition group and validator.
