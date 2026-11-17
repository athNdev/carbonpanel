# ADR 0014: Player identity, provenance and capability gates

Status: Proposed

## Context

OSS-345 replaces the thin Players tab with a cross-server player product. The
available sources disagree in important ways: the native Minecraft Server
Management Protocol is structured but only on Java 1.21.9+, RCON is text with
ambiguous replies, on-disk `playerdata`/`stats`/`advancements` are game-owned
files, and plugin bridges are loader-specific with different licences. Mojang
removed the public username-history API in 2022 and no official replacement
returns a name list. Several proposed capabilities (mute, groups, live
inventory, Bedrock parity) have no verified vanilla source.

## Decision

1. A panel-scoped `PlayerIdentity` uses an opaque `pl_*` key. Online UUID,
   offline UUID, XUID and Floodgate UUID stay distinct and are never merged by
   name alone. Only *observed* aliases are recorded; no name-history service is
   queried and the UI says so explicitly.
2. Every non-authoritative field carries `Observation {source, observed_at,
   confidence, applicability, conflict_note}`. Unknown and stale values are
   rendered as such; an empty field is not treated as a zero.
3. The structured management protocol is negotiated first when explicitly
   configured on Java 1.21.9+; RCON and node-scoped `exec` are allowlisted
   fallbacks. No adapter falls back to controller-local state, and no live
   Minecraft server is assumed in tests.
4. Skins and capes are fetched server-side from the signed Mojang texture
   chain, cached in a bounded same-origin cache and rendered as a 2D face in
   the panel. There is no client-side third-party request and no tracker by
   default. The cache accepts only canonical UUIDs and pinned texture hashes;
   arbitrary URLs and cross-host redirects are rejected structurally.
5. Deep character/progress data is read-only through the injected node
   resolver with caller contexts and W17 bounded reads. Live NBT is never
   edited; snapshots are labelled with their age and consistency.
6. Moderation is typed, permissioned with `players:*` scopes on `server_id`
   (so subuser grants apply), journaled with a request id, audited, and
   reversible only where a prior value was recorded. The legacy `SendCommand`
   string path is not a moderation boundary.
7. Optional Paper/Fabric/NeoForge bridges and full Bedrock parity are a
   **decision gate, not an implementation promise**. No bridge artifact is
   bundled and no unverified capability is advertised. The UI and capability
   RPC report each such capability as `DECISION_PENDING` or `UNSUPPORTED` with
   an actionable explanation and a licence/verification requirement.

## Consequences

- Roster and detail work on vanilla data alone; plugin-only fields degrade to
  explicit "unsupported / requires verified bridge" states rather than blanks
  that look like missing data.
- Adding a bridge later is a separate ADR with legal review of GPL-3.0 (Paper
  server), Apache-2.0 (Fabric API) and MIT (NeoForge docs), plus a fixture and
  one real-server qualification run for the management protocol.
- Licence summaries are orientation, not legal advice. Fixture protocol tests
  do not prove interoperability with a real 1.21.9+ server.

## Evidence

`product-plans/player-management.md` §§5,7,8,9,15 and `product-plans/
integrated-roadmap.md` §§2,4,6, with primary sources cited there and accessed
2026-11-18. No live Minecraft server, RCON endpoint, skin service or container
was contacted by the implementation work.
