---
type: Decision
title: Console log buffer ownership
description: A delayed log listing or WebSocket snapshot can only write the viewed buffer while it still owns the displayed server and the latest request.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/components/server-console.svelte
  - web/carbon-panel/src/lib/utils/request-sequence.ts
  - web/carbon-panel/src/lib/components/server-console.test.ts
  - web/carbon-panel/e2e/oss-245-diagnostics-reconnect.spec.ts
  - scripts/review/oss-245-diagnostics-reconnect-check.mjs
sources:
  - title: Frontend resilience
    resource: https://carbonpanel.atlassian.net/browse/OSS-185
  - title: Diagnostics and recovery
    resource: https://carbonpanel.atlassian.net/browse/OSS-245
---

# Choice

The console owns its viewed log buffer with two independent request sequences:
one for RPC log listings and one for live subscription generations. Every
producer captures the owner and a sequence before it starts and must prove both
are still current after it resolves. Navigation and container recreation rotate
the owner and re-register the WebSocket handlers, so a delayed full snapshot or
listing for a previous server cannot render or persist into a newer view. A
same-server round trip is also fenced: identity alone is insufficient because
the earlier and later requests may carry the same server ID.

Fail-closed authentication is part of the same boundary. A terminal
`signed-out` websocket state stops fallback log polling, hides the reconnect
spinner and manual reconnect affordance, and exposes a distinct signed-out
label rather than presenting a rejected token as a transport outage.

# Verification

A deferred-response Chromium fixture drives the real console component: a
stale A listing cannot paint B, a superseded same-server listing cannot
overwrite newer lines, `AUTH_FAIL` shows signed-out without retry, a failed
non-idempotent clear is dispatched once, and the log subscription keeps
delivering after container recreation. The fake-socket suite covers the
websocket generation and terminal auth transition.
