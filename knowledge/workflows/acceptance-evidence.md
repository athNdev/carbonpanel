---
type: Workflow
title: Acceptance evidence and closure
description: How an OSS work item is blocked from Done and what evidence must be recorded before closure is claimed.
status: stable
owners: [oss-runtime]
code_refs:
  - docs/acceptance
  - cmd/acceptance-evidence
  - internal/acceptanceevidence
  - .github/workflows/acceptance-evidence.yml
  - scripts/acceptance-evidence-contract.test.mjs
sources:
  - title: Acceptance evidence contract
    resource: ../../docs/acceptance/README.md
  - title: Closure evidence guard decision
    resource: ../../docs/adr/0047-closure-evidence-guard.md
---

# Steps

1. The native team-managed workflow in OSS is the blocking boundary. Do not
   bypass the Done condition group (most recent status In Review, Administrator
   role) or the Completion Evidence validator. Reopening and non-Done
   transitions stay unrestricted.
2. Before proposing a Done candidate, add it to the acceptance-evidence contract
   with PR, exact merge SHA, CI run id and public/live evidence as its surface
   requires, and add the matching evidence artifact.
3. Run `go run ./cmd/acceptance-evidence validate`. Missing, stale, skipped or
   unrecorded-exception evidence fails closed.
4. Let the scheduled drift job re-check PR merge SHA, CI run and deploy jobs
   against GitHub. It uses only the built-in token.
5. Record a weaker tier only with an explicit exception and the linked remaining
   work. Never describe a merged PR or a build capture as live verification.

# Rules

- GitHub Actions cannot gate, read or undo a Jira transition. The workflow is a
  `record-auditor` over opt-in contract candidates and a real reference run, not
  a Jira gate; the native workflow is authoritative. An empty candidate set is a
  failure, never a successful no-op.
- OSS has one human actor, so the native role condition does not prove
  author/reviewer independence. Do not claim separation until a distinct reviewer
  actor exists.
- The evidence validator checks that a `Completion Evidence:` URL was supplied,
  not that it is truthful or fresh. The repository verifier and drift audit cover
  SHA, CI, deploy and live linkage.
- Public documentation delivery (OSS-409 deploy run 44522728666) is already
  independently verified; do not re-litigate it or gate it again.
- Do not transition canonical tracker issues from this lane; the QA proof uses a
  disposable, labelled issue only.
