---
type: Decision
title: Shared provider request adapters
description: Modrinth request construction is shared while browse and Packwiz retain their presentation and version-selection policies.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/indexers/modrinth
  - internal/packwiz/content.go
  - internal/packwiz/packwiz.go
  - internal/rpc/handlers/mod_search.go
  - internal/rpc/handlers/packwiz.go
sources:
  - title: Provider consolidation
    resource: https://carbonpanel.atlassian.net/browse/OSS-338
---

# Choice

The Modrinth adapter owns search and version request construction, caller
credentials, instance-owned transport and structured provider failures. Browse
retains its sort/filter choices and HTTP response mapping. Packwiz update checks
retain provider ordering, game/loader compatibility, primary-file choice and
SHA-512 preference over SHA-1. An operation context reaches update-check requests.

# Consequences

Browse and update checks use the same transport and redirect policy as the
indexer. Compatibility wrappers retain existing callers. Packwiz still reports
an unavailable update as unchanged when the provider fails; cancellation returns
an error rather than a successful empty check. Dependency lookup, compatibility
upgrade and remaining CurseForge browse paths require separate migrations.
