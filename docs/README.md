# Documentation map

| Need | Authoritative source |
| --- | --- |
| Install, configure or use the panel | [User guides](carbon-panel/src/content/docs/index.mdx), authored in the Starlight content tree |
| Operate or recover an installation | [Deployment modes](deployment-modes.md), [server operations](runbook-server-operations.md), [panel database recovery](runbook-panel-db-restore.md), and other `runbook-*.md` files |
| Develop and verify a change | [Contributor guide](../AGENTS.md), [development commands](development.md), and [verification](../knowledge/testing/verification.md) |
| Understand accepted architecture | [ADR register](../knowledge/decisions/adrs.md) and the records in `adr/` |
| Find subsystem constraints and code ownership | [Knowledge index](../knowledge/index.md) and [repository map](../knowledge/repo-map.md) |
| Read a dated proposal or investigation | `plans/`, `research/` and `issues/`; check each document's scope, revision and evidence limits |
| Find current work, acceptance and status | [Carbon Panel OSS Jira board](https://carbonpanel.atlassian.net/jira/software/projects/OSS/boards/2) |

Plans, reviews and historical tracker identifiers preserve the context of their
recorded revision. Current implementation status lives in Jira; historical
handoffs and checkout ledgers do not establish current branch or runtime state.

## Published documentation

The public user guides, examples and API schema describe the application commit
selected in [website-source.json](website-source.json). Website presentation
changes have a separate lifecycle. See [website publishing](website-publishing.md)
for the source pin, snapshot isolation and publication checks.

## Cloud product policy

Carbon Cloud owns managed-hosting terminology and commercial decisions in its
[glossary](https://github.com/athNdev/carbon-cloud/blob/main/CONTEXT.md),
[decision register](https://github.com/athNdev/carbon-cloud/blob/main/docs/plans/hosted-minecraft-instance-catalog.md#11-decision-register)
and [commercial-policy ADR](https://github.com/athNdev/carbon-cloud/blob/main/docs/adr/0010-managed-hosting-commercial-policy.md).
Use those sources for Cloud work. The OSS [deployment matrix](deployment-modes.md)
defines the installation modes shipped by this repository.
