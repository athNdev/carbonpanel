---
type: Decision
title: Shared lazy editor runtime
description: Both Monaco views share a cached lazy bootstrap while owning their editor models and pending-load lifetime.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/features/editor
  - web/carbon-panel/src/lib/components/ui/code-editor.svelte
  - web/carbon-panel/src/lib/components/files/file-editor-dialog.svelte
  - scripts/manual-review/e2e/editor-dialog-check.mjs
sources:
  - title: Frontend workflow
    resource: ../workflows/frontend-change.md
---

# Choice

The shared runtime lazily imports Monaco, configures the bundled workers and
registers the JSON-template language once. Concurrent loads share one promise;
rejections clear that promise cache. Browsers may retain a rejected dynamic
import in the document module map, so closing and reopening a view does not
guarantee recovery. Both error states instruct users to reload the page.
Views show loading/error state, invalidate a pending
load when disposed or replaced, and release their own editor models and resize
observers. File content and save callbacks retain their file-session identity.

# Verification

Pure loader tests cover concurrent initialization and promise-cache reset. The Chromium
fixture uses both real editor views in either first-open order and closes a
view while bootstrap is delayed. Production preview checks both consumers and
HTTP serving of all five emitted worker bundles. Both dev and production tests
abort the first real bootstrap fetch, verify that reopening stays safely in
error state, and verify page reload recovers both consumers. These established-view checks
allow Monaco's initial contribution startup to settle; they do not establish
error-free disposal immediately after view creation. No byte or latency savings
are asserted without measurement.
