<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

# ADR 0054 — Player identity "last seen" is an observation, not a registration

Status: Accepted (2026-11-23).

## Context

`PlayerIdentity.LastSeenAt` doubles as "first recorded" and "last observed in
game". Any path that registers an identity — a manual allowlist add, an
operator lookup — stamped `last_seen_at = now`, so a player who never joined
appeared in the roster as recently seen, floated to the top of the last-seen
sort and was offered a presence timestamp that never came from the game.

## Decision

1. `last_seen_at` is nullable and means "the most recent game-side observation".
   Only observation sources (session/roster/metrics ingestion) write it.
   Operator paths (`manual_allowlist`, identity lookup) register the identity
   without touching it.
2. Presence is derived: an identity with no observation and no session rows
   reports offline with no timestamp. The UI renders "Never observed", never a
   fabricated or inherited time, and never-observed rows sort after observed
   ones in both sort directions.
3. A migration backfills legacy manual-add timestamps to NULL for identities
   without session rows; session evidence is what proves an observation was
   real.
4. Skin resolution treats offline-mode identities honestly: a panel-derived
   pseudo-UUID never queries Mojang; the server's own `usercache.json` is the
   name→UUID source of record, v4 UUIDs only.

## Consequences

- Roster and detail views can always distinguish "registered" from "observed".
- Adding an observation source means choosing whether it may write
  `last_seen_at`; the helper signature makes that choice explicit.
