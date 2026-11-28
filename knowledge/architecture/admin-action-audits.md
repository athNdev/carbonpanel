---
type: Architecture
title: "Admin action audits"
description: "Which administrative mutations write bounded activity rows, and where the source-backed coverage inventory lives."
status: stable
owners: [oss-runtime]
code_refs:
  - internal/rpc/services
  - internal/activity
  - internal/docscheck
sources:
  - title: OSS-358 inventory
    resource: ../../docs/plans/oss358-admin-action-audit-inventory.md
  - title: Security audit events
    resource: ../../docs/adr/0032-security-audit-events.md
---
# Shape

Every audited admin mutation in the covered classes writes at most one activity
row after the store or runtime mutation succeeds. The actor comes from the
request context, never the request payload. `writeAudit` in
`internal/rpc/services` bounds string properties and `SubjectID`, and logs a
failed insert without returning it, so auditing never changes the operation
result.

The inventory's **Deferred** rows are explicit gaps: those mutations do not
write an admin activity row in this slice and are not claimed as audited.

Added coverage in the OSS-358 bounded slice: node create/update/delete and TLS
credential clear; user create/update/delete; role create/update/delete,
permission replacement, assign and unassign; auth settings; invite create/delete;
API-token create/delete. Node TLS clearing records identity and endpoint
generation, never the certificate or key values.

Existing coverage includes server lifecycle, console command, mods, players,
world bind/unbind, world-view mutations, operation resolution and world export.
The inventory marks those as Covered rather than re-implementing them.

# Boundaries

Properties record identity and change shape only: names, ids, counts, booleans
and changed field names. Passwords, API-token plaintext, invite codes/PINs,
email addresses, TLS material and endpoint credentials never enter properties.

Read-only/list RPCs, auth bootstrap, node-enrollment bootstrap, upload transport
and per-user preferences do not get per-call rows. Deferred classes (config,
backups, tasks, modules, proxy, content catalog, world lifecycle, file writes and
node credential revocation) are explicit gaps; no event schema is guessed until
their property and secret policy is reviewed.

`internal/docscheck` checks the inventory: every cited entry point, audit
location and covering test must still exist. An **Added** row must name a
declared event, a write site that references it, and a test that asserts the
activity row.
