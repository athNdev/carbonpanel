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
  a newly allocated destination and verify it against the inspected manifest.
  Keep upload ownership, create authorization and retry idempotency scoped to
  the submitting principal, and allocate the destination exclusively on the
  chosen node before committing the server row.
- Preview is authorized by server-create, not node inventory: return only enabled
  destination IDs and names, never addresses, credentials, capacity or private
  inventory, and never require nodes/read; a supplied node still checks eligibility and capacity.
- The console offers ZIP and tar.gz export with stopped-server consistency only;
  it never stops a live server. It must show the third-party credential
  disclosure before enabling an authorized download and must not claim to detect
  secrets inside opaque plugin or mod files.
- Create-from-Archive inspects the upload, previews a concrete destination and
  renders typed findings plus that node's capacity. Archive mode never uses
  generic automatic placement; preview and create bind the same node. The
  current upload's successful preflight outranks cached node inventory: newly
  enabled nodes appear, disabled ones disappear, a still-eligible selection is
  kept and a settled empty preflight renders an actionable empty state with a
  retry. Fence preview and upload responses by upload and destination. Create
  stays blocked with an on-screen reason until the current upload has a
  successful preview for the current node; a failed preview is retryable for
  the same upload and node, and a replacement upload blocks submission while in
  flight. Reuse one create request ID across retries and retain a completed
  upload after a refusal; release that identity only after a committed create.
- Archive create submits the verified runtime and keeps operator memory and
  max-player overrides, hiding standalone add-ons and the incompatible image,
  modpack, detached and Docker-override fields, and normalizing stale values
  out of the request. Leaving archive mode restores the prior placement choice;
  an in-flight create blocks navigation and binds the captured upload, node and preview.
