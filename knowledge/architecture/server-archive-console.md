---
type: Architecture
title: Server archive console and preflight
description: Export and create-from-archive controls bind preview and create to the current upload and a concrete enabled destination.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/features/servers/archive-presentation.ts
  - web/carbon-panel/src/lib/features/servers/archive-import-session.ts
  - web/carbon-panel/src/lib/features/servers/ServerArchiveExport.svelte
  - web/carbon-panel/src/lib/features/servers/ArchiveExportPanel.svelte
  - web/carbon-panel/src/lib/features/servers/ArchivePreviewPanel.svelte
  - web/carbon-panel/src/routes/servers/new/+page.svelte
  - web/carbon-panel/src/routes/servers
sources:
  - title: Server portability
    resource: ../../docs/server-portability.md
  - title: Archive destination preflight decision
    resource: ../decisions/archive-destination-preflight.md
---

# Rules

- The server detail console offers ZIP and tar.gz export with stopped-server
  consistency only and never stops a live server. Show the third-party
  credential disclosure before enabling an authorized download and do not claim
  to detect secrets inside opaque plugin or mod files.
- Keep the export panel import-free so it renders in isolation, and reproduce
  CarbonButton's `--carbon-control-md` (40 px) geometry for its actions instead
  of hand-rolled padding; the control list is pinned by
  `ArchiveExportPanel.test.ts`.
- Fence an export preparation to the current open modal attempt and server.
  Closing and reopening for another format, a server identity or status change,
  and teardown release the former attempt, so a late success or rejection cannot
  replace the active session, filename or phase. The download action requires
  the current ready session. Export preparation is a silent RPC call: the shared
  interceptor must not toast for a stale attempt, and only the fenced current
  attempt reports its failure.
- Create-from-Archive binds preview and create to the same concrete node and
  renders typed findings plus that node's capacity. The current upload's
  successful preflight outranks cached inventory, including newly enabled,
  disabled and settled-empty destinations with retry. Fence responses by upload
  and destination; block create with an on-screen reason until the current
  upload has a successful preview for that node. Retry a failed preview for the
  same node, block submission during replacement uploads, reuse one create
  request ID, and retain a completed upload after refusal until commit.
- Archive create submits the verified runtime, keeps operator memory and
  max-player overrides, hides standalone add-ons and incompatible image,
  modpack, detached and Docker-override fields, and clears stale values. Leaving
  archive mode restores placement; an in-flight create blocks navigation and
  binds the captured upload, node and preview.
- Keep selected archive-card descriptions on the accent foreground token and
  keep the archive-only same-node explanation readable. The unavailable
  Auto-Placement choice stays focusable through `aria-disabled` rather than
  dimming its explanation with native disabled opacity.
