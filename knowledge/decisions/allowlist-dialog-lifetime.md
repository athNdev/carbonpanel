---
type: Decision
title: Allowlist dialog lifetime across telemetry polling
description: The scoped allowlist dialog closes only on a real server switch or tab deactivation, not on every telemetry republish.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/features/players/allowlist-dialog-state.ts
  - web/carbon-panel/src/lib/components/server-players.svelte
  - scripts/review/plan-showcase.json
sources:
  - title: Player management identity and provenance
    resource: ../architecture/player-management-identity-and-provenance.md
---

# Choice

The server detail page republishes a fresh `Server` object every telemetry poll.
An effect that merely reads `server.id` therefore re-runs on every poll. The
allowlist dialog must not close on those refreshes: it closes only when the
server identity changes or the Players tab becomes inactive.
`allowlistDialogShouldClose` owns that rule and is unit-tested across repeated
same-identity polls.

# Consequences

An open allowlist dialog survives a telemetry refresh while an operator is
using it, and the README showcase capture can clip the dialog after its
settle/timing window. Switching servers or leaving the tab still closes it, so
it can never apply to a different server. No authorization or audit behavior
changes; the dialog still requires `players:access:write` on the target server.
