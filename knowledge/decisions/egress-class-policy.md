---
type: Decision
title: Named egress classes
description: Provider, webhook and archive outbound requests use class-specific policy objects; loopback is a test-only opt-in and remote saves are staged.
status: stable
owners: [oss-runtime]
code_refs:
  - pkg/utils/ssrf.go
  - pkg/utils/ssrf_egress_test.go
  - pkg/files/atomic.go
  - internal/rpc/services/file.go
  - internal/rpc/services/file_remote_test.go
  - internal/webhook/webhook.go
  - internal/minecraft/datapack.go
  - internal/rpc/handlers/packwiz_datapacks.go
  - internal/rpc/services/modpack.go
  - internal/rpc/services/modpack_egress_test.go
  - internal/config/config.go
  - config.example.yaml
sources:
  - title: ADR 0043
    resource: ../../docs/adr/0043-egress-policy-enforcement.md
  - title: Contributor guide
    resource: ../../AGENTS.md
---

# Choice

`pkg/utils` exposes three immutable `EgressPolicy` values, one per outbound
family. Provider egress carries a fixed host allowlist (known provider hosts
plus the trusted indexer origin) and a private-target decision derived from the
indexer's own policy; a public indexer still forces public-only downloads and a
private host outside the allowlist is refused. Webhook egress carries the exact
origin of the operator-configured receiver, so a redirect to any other origin
is refused. Archive egress carries both the
`security.allow_private_archive_egress` flag and the destination server's named
node: an empty node id denies RFC1918 targets even when the flag is set.
Loopback and link-local stay blocked for every class.

`ValidateURLForEgress` and `NewSafeHTTPClientForEgress` apply the same policy
object to the initial request (via a policy `RoundTripper`), dial time and every
redirect. Provider and webhook callers also validate the URL before building a
request. URLs with userinfo are rejected, and `RedactURL` strips credentials and
sensitive query values before any log line or client-facing error. `url.Error`
causes are unwrapped before delivery diagnostics, so a raw indexer or receiver
URL is never reflected. `url.Parse` errors are never chained into a diagnostic:
the policy returns `ErrInvalidURL` with only the redacted form.

The `DISCO_ALLOW_LOOPBACK_TEST` escape hatch is honoured only inside a Go test
binary (`testing.Testing()`) with the variable explicitly set to `1`. Production
builds can never enable loopback through the environment. Controller-local
remote-archive saves use `pkg/files.AtomicWriter`: the staged file stays `0600`
inside a private `0700` same-filesystem directory until the final rename; the
final mode is applied only at publish (umask-masked mode for a new destination,
previous mode and owner for an existing one), so no restrictive file is
broadened and no partial content is exposed. A failed copy aborts staging
instead of truncating the target.

# Alternatives

One shared `allowPrivate` boolean made a user-supplied archive URL inherit the
latitude needed by a homelab webhook receiver; raw URLs in diagnostics could
reflect embedded credentials. A build-tag test bypass alone could be omitted
from a release build, and in-place writes exposed a truncated target. The
policy object keeps each family's grant distinct and the staged publish keeps
the previous file intact until the whole copy succeeds.

# Consequence

New outbound families must construct a named policy rather than pass a bare
boolean. Self-hosted installs fetching archives from a trusted LAN host need
both the flag and a resolved node; the default is fail closed. Provider
adapters and webhook receivers keep their established behaviour, and loopback
`httptest` fixtures continue to opt in with `t.Setenv`.

Remote modpack import is a controller-scoped library fetch with no destination
node, so it binds to the archive class with an empty node name: RFC1918 targets
are refused regardless of the request's `allow_private_network` field, and the
client must fetch from a public URL or use an upload. Rejected URLs and
successful-import logs use `RedactURL`, so embedded credentials are never
reflected.
