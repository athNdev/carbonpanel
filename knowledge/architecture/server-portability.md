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
- Preview is authorized by server-create, not node inventory. It may return only
  the enabled destination IDs and display names needed to choose a node; never
  require nodes/read for this path or expose addresses, credentials, capacity
  statistics or other private inventory. A supplied node still runs the
  exact-node eligibility and capacity check.
- The console offers ZIP and tar.gz export with stopped-server consistency only;
  it never stops a live server. It must show the third-party credential
  disclosure before enabling an authorized download and must not claim to detect
  secrets inside opaque plugin or mod files.
- Create-from-Archive inspects the upload first, then previews against a
  concrete destination node and renders typed findings and that node's capacity.
  Archive mode never uses generic automatic placement; preview and create bind
  the same node. Fence preview and upload responses by upload and destination.
  Create stays blocked until the current upload has a successful preview for the
  current node; a failed preview is retryable for the same upload and node, and
  a replacement upload blocks submission while it is in flight. Reuse one create
  request ID across retries and retain a completed upload after a refusal;
  release that identity only after a committed create.
- Archive create shows the verified runtime, submits its Minecraft version,
  loader and loader version, and keeps operator memory and max-player overrides.
  It disables standalone add-on setup because the archive carries its own
  content, and never carries a former blueprint image or modpack into the
  request.
