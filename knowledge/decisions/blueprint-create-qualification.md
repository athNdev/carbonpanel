---
type: Decision
title: Blueprint create qualification
description: Create-path blueprints are Java/tag seeds whose resolved image, env and protocol are tested; a Bedrock full-image seed is labeled unsupported, not a native edition.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/blueprint
  - internal/rpc/services/blueprint.go
  - internal/rpc/services/server.go
  - web/carbon-panel/src/lib/utils/server-blueprint.ts
  - web/carbon-panel/src/routes/servers/new/+page.svelte
sources:
  - title: Feature completeness matrix
    resource: ../../docs/plans/oss-feature-completeness-matrix.md
---

# Decision

- Selecting a catalog seed on create resolves a daemon image, container env
  (`TYPE`, `EULA`) and player protocol in `blueprint.Resolve`. Tests assert
  those fields; a helper or seed name is not evidence.
- Qualified seeds are Java edition on `itzg/minecraft-server` (empty image
  becomes the `java21` tag). The create wizard offers those for selection.
- The builtin Bedrock seed keeps its full `itzg/minecraft-bedrock-server`
  reference and is listed as an unsupported mode. It does not imply a native
  Bedrock edition path. Geyser stays a separate module.
- Applying a seed to create (`applyResolvedBlueprint`) refuses an
  unqualified seed before `CreateServer`. An operator who pastes a Bedrock
  image without going through that apply path is still storing a reference,
  not a qualified edition workflow.

# Rationale

O03 advertised a blueprint picker that the create route did not have, and a
Bedrock seed that only proved a catalog row. Hiding or labeling the
unqualified seed on the create path stops a false native-edition claim
without deleting the catalog.
