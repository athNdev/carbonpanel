---
type: Decision
title: "Named egress classes — Consequence"
description: "New outbound families must construct a named policy rather than pass a bare"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: egress-class-policy.md
---
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
