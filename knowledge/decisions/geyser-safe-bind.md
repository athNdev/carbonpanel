---
type: Decision
title: Geyser safe bind default
description: A standalone Geyser entrypoint defaults to a loopback Bedrock bind; an explicit BEDROCK_ADDRESS is the opt-in to wider exposure.
status: stable
owners: [oss-runtime]
code_refs:
  - cmd/geyser
  - internal/module/builtin_templates.go
  - docs/deployment-modes.md
sources:
  - title: Deployment modes
    resource: ../../docs/deployment-modes.md
---

# Choice

`cmd/geyser` generates `bedrock.address: 127.0.0.1` when `BEDROCK_ADDRESS` is
unset and warns when an explicitly configured wildcard bind is used. The
shipped Geyser module keeps an explicit `BEDROCK_ADDRESS=0.0.0.0` because the
panel's UDP port proxy dials the container IP, so legitimate Bedrock traffic
must keep working.

# Alternatives

Changing the built-in template to loopback would break every published Geyser
port. A separate opt-in flag would break already-persisted module instances
that only carry the explicit address. Requiring a wildcard bind by default
exposes the UDP listener on every container interface for standalone runs.

# Consequence

Existing generated configs are not rewritten, so only fresh standalone runs
change behavior. Direct-exposure deployments must set `BEDROCK_ADDRESS`
themselves; the published UDP port still needs network-level restriction.
