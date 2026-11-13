---
type: Decision
title: WebSocket connection lifetime
description: Socket callbacks and timers belong to one connection generation, and subscriptions retain their requested tail on reconnect.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/stores/websocket.svelte.ts
  - web/carbon-panel/src/lib/stores/websocket.test.ts
  - scripts/manual-review/e2e/websocket-reconnect-check.mjs
sources:
  - title: Frontend change workflow
    resource: ../workflows/frontend-change.md
---

# Choice

The public singleton uses a factory with injectable socket, clock and token
access. Every callback and timer checks its connection generation. Replacement
or explicit disconnect invalidates the old generation before closing it. Pending
reconnect timers cannot reopen a disconnected client. Each active subscription
retains its requested tail when the current connection authenticates again.

# Consequences

The client preserves its existing backoff, log batching and subscriber API.
Fake sockets cover late events and timer races; a Chromium smoke checks an
actual loopback WebSocket disconnect and reconnection with a non-default tail.
