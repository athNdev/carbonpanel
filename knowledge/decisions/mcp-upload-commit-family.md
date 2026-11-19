---
type: Decision
title: MCP upload/commit family seam
description: Upload and commit operations are exposed as lane-owned typed tools and a composed bounded-stream path whose descriptors are handed to the integration owner, never wired directly into shared catalog files.
status: draft
owners: [oss-runtime]
code_refs:
  - internal/mcp/family_upload_tools.go
  - internal/mcp/family_upload_descriptors.json
  - internal/mcp/family_upload_tools_test.go
sources:
  - title: MCP controller-side stream transport plan
    resource: ../../docs/plans/mcp-controller-side-stream-transport.md
  - title: MCP full-coverage and publication gate
    resource: https://carbonpanel.atlassian.net/browse/OSS-375
---

# Choice

The upload/commit operations (`SaveUploadedFile`, `InitUpload`, `UploadChunk`
and the `rest_upload_stream` byte surface) are implemented in a self-contained
family file that exports descriptor, spec, registration and argument-table
seams. The single integration owner joins those seams into the generated catalog
and invocation harness. A tool reaches the controller only through typed
generated Connect clients behind the production auth interceptor; the composed
upload path additionally consumes controller-owned bounded stream frames and
holds a one-at-a-time commit lease. The final save re-checks
`files:create[server_id]` at commit with no direct destination helper, and an
ambiguous post-dispatch save is resolved by verifying the exact destination,
never by a blind re-upload.

# Alternatives rejected

- Editing `catalog.go`, `catalog_data.go`, `integration_catalog.go`,
  `integration_registration.go` or the pending inventory from this lane was
  rejected: those are single-owner files, and parallel lanes would overwrite
  each other's hand-offs. Exported seams keep each lane independently reviewable.
- A gateway-side filesystem or a direct `os.WriteFile` commit was rejected:
  the gateway must never own panel/node storage, and a direct helper would
  bypass the fresh server-scoped authorization enforced by the controller.
- Blind retry after a lost final-save reply was rejected: the destination may
  already be durable. Verification is the only honest resolution.

# Consequence

Both lanes' executable `tools/call` evidence is merged into the shared catalog
and production `registerCombinedTools`; the pending inventory is empty and the
STRICT full-coverage gate is green. This does not authorize MCP publication,
GHCR or registry write, which remain separate trusted-branch actions. A new
upload tool must keep its tool name aligned with the OSS-375 coverage matrix
and its descriptor artifact in sync with the live table; a drift test fails the
build otherwise.

# Verification

Real stdio `tools/call` fixtures assert exact typed-field mapping, handler
outcomes, resource-scope success and denial, a fresh commit-scope refusal with
zero writes, exclusive commit leases, ambiguous-save verification, and cap,
cancel, expiry, cross-scope and zero-byte refusals with no upload RPC on refusal.

# Read next

- [MCP server core](../architecture/mcp-server.md)
- [Fail-closed authorization](../invariants/authz-fail-closed.md)
