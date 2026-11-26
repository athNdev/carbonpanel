---
type: Architecture
title: Server portability archive
description: Native ZIP and tar.gz containers share a versioned server manifest and per-file SHA-256 validation.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/serverarchive
  - internal/rpc/services
  - internal/nodestorage/allocation.go
  - proto/carbonpanel/v1
  - web/carbon-panel/src/lib/features/servers/archive-presentation.ts
  - web/carbon-panel/src/lib/features/servers/archive-import-session.ts
  - web/carbon-panel/src/lib/features/servers/ServerArchiveExport.svelte
  - web/carbon-panel/src/lib/features/servers/ArchiveExportPanel.svelte
  - web/carbon-panel/src/lib/features/servers/ArchivePreviewPanel.svelte
  - web/carbon-panel/src/routes/servers/new/+page.svelte
  - web/carbon-panel/src/routes/servers
sources:
  - title: Server portability archive format
    resource: ../../docs/server-portability.md
  - title: Server portability
    resource: https://carbonpanel.atlassian.net/browse/OSS-422
---

# Rules

- Stream server data through the owning node's storage accessor. Export callers
  must hold lifecycle admission and verify the server is stopped.
- Serialize only explicitly portable runtime fields. Keep deployment identity,
  host paths, overrides and credentials outside the manifest.
- Validate the complete archive before allocating a destination. Copy only into
  a newly allocated destination and verify its input against the inspected
  manifest before keeping the destination.
- Keep upload ownership, server-create authorization and retry idempotency scoped
  to the submitting principal. Allocate the destination exclusively on the
  chosen node before committing the new server row.
- The console offers ZIP and tar.gz export with stopped-server consistency only;
  it never stops a live server. It must show the third-party credential
  disclosure before enabling an authorized download and must not claim to detect
  secrets inside opaque plugin or mod files.
- Create-from-Archive uploads once, previews after upload and again when the
  destination changes, and renders typed findings and node capacity. Fence
  preview responses by upload and destination. Reuse one create request ID
  across retries and retain a completed upload after a refusal; release that
  identity only after a committed create.
