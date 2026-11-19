# Acceptance evidence contract (OSS)

`oss-acceptance-evidence.json` is the machine-readable record of work items
proposed for the Done category in OSS. It is **not** a substitute for the native
Jira guard: the team-managed workflow blocks direct/unreviewed/unevidenced Done
transitions at the source. This contract and validator are the repository-side
verifier and the scheduled drift detector for the exact merged artifact.

## Tiers

| Tier | Meaning |
| --- | --- |
| `CODE-MERGED` | Merged code only. Never sufficient for a public/live surface. |
| `CI-GREEN` | Merged code plus exact-commit green CI. Minimum for `internal`. |
| `BUILD-ONLY` | Build succeeded; no deploy/live proof. |
| `CAPTURE-ONLY` | A capture exists; live status not verified. |
| `DEPLOY-SKIPPED` | Deploy/publication job explicitly skipped. |
| `LIVE-VERIFIED` | Merge SHA + CI + non-skipped deploy + fresh live URL status. |

An `internal` surface requires at least `CI-GREEN`; `build` requires
`BUILD-ONLY`; `public` and `live` require `LIVE-VERIFIED`. A weaker tier is
permitted only with an explicit, well-formed `exception` (kind, reason,
`recorded_at`, reviewer). `DEPLOY-SKIPPED`, `CAPTURE-ONLY` and `BUILD-ONLY`
exceptions must use their matching kind. The verifier must not equal the merge
author.

## Adding a Done candidate

1. Add a candidate to `candidates[]` with the issue key, surface, tier, PR
   number, full merge SHA, CI run id, optional deploy/live references, verifier,
   and verification time.
2. Add `docs/acceptance/evidence/<ISSUE>.json` with the observed merge, CI,
   build, deploy and live facts. Keep the live screenshot/log in
   `docs/acceptance/evidence/` and reference it from the artifact.
3. Run `go run ./cmd/acceptance-evidence validate`. It fails closed on missing,
   stale (wrong SHA), skipped deploy, unrecorded exception, or a verifier who is
   also the author.

```sh
go run ./cmd/acceptance-evidence validate
go run ./cmd/acceptance-evidence verify-github   # scheduled drift audit; GITHUB_TOKEN from the runner
```

## Native guard and its limit

The live OSS team-managed workflow (project 10001) has a blocking `ALL` condition
group on the global Done edge: most-recent status must be In Review, and only the
Administrator project role may move to Done. A validator requires the description
to contain a `Completion Evidence:` section with a durable http(s) URL. Reopening
and all non-Done transitions are unchanged.

Limits that this repository layer documents but does not magically fix:

- OSS has a single human actor, so the role condition proves *who may close*, not
  author/reviewer independence. A distinct reviewer actor must be added before
  claiming separation.
- A dedicated custom field could not be created through the field API; the
  native evidence check is a structured Description section, not a purpose-built
  field, and cannot judge truthfulness or freshness of the URL.
- GitHub Actions cannot gate a Jira transition. The workflow in this repository
  gates the merge/label and runs a scheduled drift audit; it cannot undo a Done
  click made in Jira. The native workflow remains the blocking boundary.
