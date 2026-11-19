---
type: Decision
title: Named egress classes
description: Provider, webhook and archive outbound requests use separate egress decisions; URLs are redacted before being logged or returned.
status: stable
owners: [oss-runtime]
code_refs:
  - pkg/utils/ssrf.go
  - internal/rpc/services/file.go
  - internal/webhook/webhook.go
  - internal/minecraft/datapack.go
  - internal/rpc/handlers/packwiz_datapacks.go
  - internal/config/config.go
  - config.example.yaml
sources:
  - title: Contributor guide
    resource: ../../AGENTS.md
---

# Choice

`pkg/utils` exposes three `EgressClass` values. The provider class carries the
fixed decision computed by the provider adapter from its trusted indexer/host
policy (`minecraft.DownloadAllowsPrivate`), not a caller-supplied flag; a public
indexer still forces public-only downloads. Webhook egress permits private
receivers because the receiver itself is operator configured. Archive egress
permits RFC1918 targets only behind the explicit
`security.allow_private_archive_egress` flag; loopback and link-local stay
blocked for every class. `ValidateURLForEgress` and
`NewSafeHTTPClientForEgress` bind the same class policy to dial-time and
redirect-time checks, so a redirect cannot inherit a looser policy. URLs with
userinfo are rejected, and `RedactURL` strips credentials and sensitive query
values before any log line or client-facing error. `url.Parse` errors are never
chained into a diagnostic: they embed the raw URL, so the policy returns
`ErrInvalidURL` with only the redacted form.

# Alternatives

One shared `allowPrivate` boolean made a user-supplied archive URL inherit the
latitude needed by a homelab webhook receiver, and raw URLs in diagnostics could
reflect embedded credentials. A build-tag test bypass alone would not separate
the product decisions.

# Consequence

New outbound families must name a class rather than passing a bare boolean.
Self-hosted installs fetching archives from a trusted LAN host must set the new
flag; the default is fail closed. Existing provider adapters and webhook
receivers keep their established behaviour, and the loopback-only test override
remains a test fixture, not a production egress path.
