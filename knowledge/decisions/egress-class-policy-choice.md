---
type: Decision
title: "Named egress classes — Choice + Alternatives"
description: "pkg/utils exposes three immutable EgressPolicy values, one per outbound"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: egress-class-policy.md
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
