---
type: Architecture
title: "RPC contract — Rule"
description: "Every frontend or CLI call goes through a service defined in"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1, internal/rpc, buf.gen.yaml, web/carbon-panel/src/lib/proto, internal/nodescan]
sources:
  - title: Parent concept
    resource: rpc-contract.md
---
# Rule

Every frontend or CLI call goes through a service defined in
`proto/carbonpanel/v1`. Do not add an ad-hoc HTTP endpoint for product data.
Existing product HTTP consumers migrate incrementally through shared behavior
and tested compatibility adapters. [ADR 0012](../../docs/adr/0012-product-transports.md)
defines the minimum deprecation window and explicit content/integration HTTP
exceptions; [the inventory](../../docs/product-transports.md) identifies active
families. The rule describes the destination contract, not permission to remove
live REST consumers without a migration.
Node read responses are redacted: `dbNodeToProto` leaves `tls_ca_cert`,
`tls_cert` and `tls_key` empty, and `UpdateNode` treats an empty TLS string as
unchanged so an editor cannot clear stored credentials by echoing a redacted
response. Clearing stored credentials is a separate named, confirmed,
`nodes:update`-authorized action with an audit entry, not an empty-field edit;
ordinary edits never clear. The daemon pool reads stored material from the
database. See [ADR 0018](../../docs/adr/0018-node-response-redaction.md).

Server console commands are audited only after the server lookup, running-state
check and non-empty command validation, so an invalid lookup or argument writes
no row. One `server.command` activity row records the actor, the server id, the
silent flag and the send outcome, with an error string only when sending failed.
Because the feed is readable with the scoped `activity:read` permission and a
command can carry plugin credentials (`/login <password>`), properties keep
only the command's first whitespace-delimited verb, capped at 64 bytes on a
UTF-8 boundary, plus `command_length`, the original command's byte length, so
the row still correlates with the full console log; arguments are never
stored. Every string property, including a provider error, is capped at 512
bytes. An audit write failure is logged and never changes the RPC response or
the log stream.

Node discovery is the first compatibility migration: both transports use the
same twelve-second scan and best-effort known-node count. ScanNodes requires
nodes/read globally; the compatibility route preserves numeric JSON fields and
denies missing authentication or authorization dependencies. A node-scoped grant
cannot authorize a host-wide scan.
Provider credential validation is the second migration: generated
ConfigService.ValidateProviderKey and the legacy settings/validate-key adapter
share indexers.ValidateProviderKey. Provider outcomes stay soft (valid/message),
the RPC maps settings/read globally, and both transports use the shared
requireGlobalObject policy in internal/rpc/handlers, which authenticates even
in explicit no-auth mode and fails closed on missing policy dependencies.
The raw validator's HTTP client strips credentials on cross-origin redirects.

