---
type: Overview
title: Repository map
description: Top-level directories and the subsystem each one owns.
status: stable
owners: [oss-runtime]
code_refs: [cmd, internal, pkg, proto, web, docs, docker, scripts, oidc]
sources:
  - title: Design specification
    resource: ../../DESIGN.md
  - title: Documentation ownership
    resource: ../docs/README.md
---

# Layout

| Path | Owns |
| --- | --- |
| `cmd/carbon-panel` | Daemon entrypoint, lifecycle, embedded frontend |
| `cmd/carbon-fault-harness` | Fault-injection harness for tests |
| `cmd/carbon-storage` | Worldstore JSON worker and nodestorage accessor mode for remote transport |
| `cmd/geyser`, `cmd/status` | Auxiliary binaries |
| `internal` | Controllers and subsystems (see the architecture concepts) |
| `pkg` | Public shared packages (`mcstatus`, `routing`) also used by Cloud |
| `proto/carbonpanel/v1` | Connect-RPC contracts |
| `web/carbon-panel` | SvelteKit 2 / Svelte 5 console |
| `docs` | ADRs, plans, research, runbooks and issue write-ups |
| `docker`, `scripts`, `oidc` | Deployment assets, developer scripts, OIDC examples |

# Rule

Do not add a top-level directory without a concept that names its owner and
boundary. If a directory exists only for one subsystem, document that subsystem
under `architecture/` instead.

Internal packages must have a runtime caller or an explicitly supported tool
or test seam. Keep unimplemented feature requirements in Jira; an uncalled
placeholder that returns synthetic credentials is not a provisioned capability.

Documentation ownership is mapped in [docs/README.md](../docs/README.md):
Starlight holds user guides, runbooks hold operations, ADRs hold accepted design,
and knowledge holds concise governing concepts. Jira owns current work and
status. Dated plans and research retain their recorded scope; handoff prompts,
checkout ledgers and workstation configuration are not durable project context.

# Read next

- [Runtime model](architecture/runtime.md)
- [RPC contract](architecture/rpc-contract.md)
- [Testing and verification](testing/verification.md)
