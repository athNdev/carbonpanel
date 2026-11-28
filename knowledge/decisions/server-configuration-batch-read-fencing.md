---
type: Decision
title: Server configuration batch-read fencing
description: The Configuration screen applies only the newest staging-window read, so a pre-save response cannot replace the window an operator just stored.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/components/server-configuration.svelte
  - web/carbon-panel/src/lib/components/server-configuration.dom.test.ts
sources:
  - title: Frontend resilience
    resource: https://carbonpanel.atlassian.net/browse/OSS-185
  - title: Staged updates
    resource: ../invariants/staged-updates.md
---

# Choice

The server Configuration screen reads the open update batch independently of
the configuration fields. Each read takes a generation and applies its batch
window, timestamp and idle minutes only while it is still the newest read for
the current server. Saving starts a new read, so a response that started before
the save is inert when it resolves later.

# Consequences

An operator's just-saved apply window survives a slower pre-save read. The
same fence covers server changes and repeated discard/save reads, without
changing what is staged or when the server restarts. The rendered-window
regression is covered with deferred responses in `server-configuration.dom.test.ts`.
