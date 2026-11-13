---
type: Decision
title: Provider HTTP credentials
description: Provider credentials stay with the configured origin and never accompany keyless fallback or cross-origin redirects.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/indexers/httpclient.go
  - internal/indexers/fuego
  - internal/rpc/handlers/mod_search.go
sources:
  - title: Provider policy consolidation
    resource: https://carbonpanel.atlassian.net/browse/OSS-338
---

# Choice

The CurseForge adapter owns its official/keyless fallback policy. A keyless
request never uses the keyed client. Canceled operations do not start a
fallback request. Provider clients accept an instance-owned HTTP client for
local fixtures and caller-specific transport or timeout policy.
The known keyless provider is recognized by its parsed hostname, so alternate
paths, ports, schemes, trailing slashes and hostname case cannot enable a key.

The shared redirect policy clones the supplied client, preserves its redirect
limit or callback, and removes API keys, authorization and cookies when scheme,
host or port changes. The original client and supplied header map are not mutated.

# Consequences

The community proxy receives no official API key, including slug lookup.
Filtering, file ordering, checksum selection and existing fallback error results
are preserved. Fixture-only tests cover redirects, authentication/status and
malformed responses, cancellation, and credential-free fallback. Remaining
browse/Packwiz request consolidation is tracked in the source issue.
