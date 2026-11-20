---
type: Decision
title: WebSocket connection lifetime
description: Socket callbacks and timers belong to one connection generation, subscriptions retain their requested tail on reconnect, and AUTH_FAIL ends in a signed-out state rather than a retry loop.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/stores/websocket.svelte.ts
  - web/carbon-panel/src/lib/stores/websocket.test.ts
  - scripts/manual-review/e2e/websocket-reconnect-check.mjs
  - web/carbon-panel/src/lib/components/server-console.svelte
  - web/carbon-panel/e2e/oss-245-diagnostics-reconnect.spec.ts
  - scripts/review/oss-245-diagnostics-reconnect-check.mjs
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
An `AUTH_FAIL` frame is a terminal credential rejection: the client drops the
socket and its generation, clears timers/subscriptions/pending log batches and
enters `signed-out` while clearing the local session like an RPC 401. It never
schedules a reconnect, and views must not render a reconnect spinner in that
state.

# Consequences

The client preserves its existing backoff, log batching and subscriber API.
Fake sockets cover late events and timer races. Chromium journeys cover an
actual loopback disconnect/reconnect with a non-default tail and the seeded
console fixture proves transport drops reconnect, re-auth, resubscribe and
continue live delivery without signing out.
