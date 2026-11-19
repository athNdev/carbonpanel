# ADR 0043: Enforce egress policy per class, not per boolean

Status: accepted (OSS-399, B-07 A1/A2/C1).

The three outbound families used to differ only by a label. `ssrf.go` returned
the same `allowPrivate` decision for provider, webhook and archive traffic, and
`ValidateURLForEgress` applied only that boolean. A user-supplied archive URL
could therefore inherit the private-network latitude that a homelab webhook
receiver legitimately needs. The named-node requirement was documentation only,
and `DISCO_ALLOW_LOOPBACK_TEST=1` was read in every build. Direct `os.Create`
on the remote-archive save path could leave a truncated target on a failed copy.

## Decision

`pkg/utils` now exposes one immutable `EgressPolicy` value per family, built by
`ProviderEgressPolicy`, `WebhookEgressPolicy` or `ArchiveEgressPolicy`:

- **Provider** carries a fixed host allowlist (known public provider hosts plus
  the trusted indexer origin) and a private-target decision derived from that
  indexer's policy. A public indexer can never point a download at a private
  host, and a private host outside the allowlist is refused.
- **Webhook** carries the explicit origin of the operator-configured receiver.
  A different origin — including a redirect to one — is refused even when both
  are loopback with different ports.
- **Archive** carries the `security.allow_private_archive_egress` flag together
  with the destination server's named node. An empty node id denies RFC1918
  targets even when the flag is set. Loopback and link-local remain blocked for
  every class.

`NewSafeHTTPClientForEgress` binds the same policy object to every outbound
request: a policy `RoundTripper` checks the initial URL, the dialer applies the
coarse private-network decision, and `CheckRedirect` revalidates each redirect.
Checking only redirects would let an indexer-supplied initial URL bypass the
provider allowlist entirely. Provider and webhook callers additionally validate
the URL before building a request so client-side errors never reflect raw indexer
or receiver URLs. The zero value and unknown classes fail closed.

The loopback test escape hatch is honoured only when `testing.Testing()` is
true **and** `DISCO_ALLOW_LOOPBACK_TEST=1`. Production binaries cannot be made
to allow loopback by setting the environment variable. Existing loopback
`httptest` fixtures keep opting in with `t.Setenv`; the pure
`loopbackTestAllowed(isTestBinary, envValue)` predicate makes the production
deny branch directly testable without a mutable production variable or a build
tag that a release build could omit.

Remote archive saves go through `pkg/files.AtomicWriter`: the download is
staged in a sibling temp file, the previous target's permission bits and owner
are carried to the replacement, and only a complete copy is published. A new
destination is created with mode `0666` so the operating system applies the
process umask exactly as the old `os.Create` path did; no publish-time chmod
widens a restrictive umask. A failed copy aborts the temp file, so the old
target stays intact. This is the same staged-publish contract as the bounded
storage stream (ADR 0013) applied to the controller-local save path.

## Alternatives

Keeping a single boolean with three named constants was smaller but left the
split nominal, which is the defect this ADR closes. A build-tagged loopback
bypass would require every test and release command to carry the tag; a test
binary check cannot be spoofed through process configuration. chown/chmod in
place would preserve the target metadata but would expose a truncated file
during the copy, so staging and atomic publish were preferred.

## Consequence

New outbound families must construct a named policy rather than pass a bare
boolean. Self-hosted archive downloads still need the explicit flag and a
resolved node. Provider and webhook receivers are audited as fixed lists, and
no diagnostic reflects a raw URL or its credentials. The A2 regression proves
a failed copy leaves the previous file byte-for-byte unchanged.
