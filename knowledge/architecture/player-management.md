---
type: Architecture
title: Player management
description: Player identity, provenance, moderation and capability gates for the PlayerService surface.
status: stable
owners: [oss-runtime]
code_refs:
  - proto/carbonpanel/v1/player.proto
  - internal/rpc/services/player.go
  - internal/rpc/services/player_wiring.go
  - internal/db/player.go
  - internal/playeradapters
  - internal/playerskin
  - internal/playerdata
  - internal/rbac/mapping.go
sources:
  - title: Player identity, provenance and capability gates
    resource: ../../docs/adr/0017-player-management-identity-and-capability-gates.md
  - title: Add an RPC
    resource: ../workflows/add-rpc.md
---

# Identity and provenance

`PlayerIdentity` is a panel cache/index with an opaque `pl_*` key. Online UUID,
offline UUID, XUID and Floodgate UUID remain distinct; aliases are only names
this panel observed. Mojang no longer provides public name history, so no
third-party name lookup is used and the UI states this. Every non-authoritative
field carries a source, observation time, confidence and conflict note; unknown
is not zero.

# Sources and capabilities

The native management protocol is negotiated first only when explicitly
configured on Java 1.21.9+. RCON and node-scoped exec are allowlisted fallbacks
defined in `internal/playeradapters`; neither uses controller-local state.
Optional plugin bridges (vitals, inventory, groups, mute) and full Bedrock
identity parity are decision-gated: they are reported as decision-pending or
unsupported with an actionable explanation, never shipped as implied vanilla
capabilities.

# Skins, identity lookup and deep data

`internal/playerskin` fetches signed textures server-side, caches them
same-origin with bounded TTL/size and renders a 2D face. It accepts only
canonical UUIDs and pinned texture hashes; arbitrary URLs and cross-host
redirects are refused. The same bounded provider transport resolves an exact
UUID (session-server profile) or current username (Minecraft Services name
lookup). `LookupPlayer` is read-only: its ephemeral result never persists an
identity, session, access row, roster entry or presence claim. Authoritative
negatives are `NOT_FOUND`; outages and timeouts are `UNAVAILABLE`; unpinnable
queries are `AMBIGUOUS`. No name-history service is queried. A manual
per-server allowlist add is a separate authorized mutation that reuses typed
moderation (`players:access:write` on `server_id`), journal, audit and undo;
it persists only the identity the mutation needs. Java name/UUID entries use
the negotiated Java adapter; Bedrock Gamertag/XUID entries need an adapter
proving Bedrock support and are otherwise refused with a capability error.
`internal/playerdata` reads `stats`, `advancements` and `playerdata` snapshots
through the injected node resolver with caller contexts and bounded reads;
live NBT is never edited.

# Roster filters

`ListPlayers` accepts the console's `all` sentinel for `state_filter` and
`access_filter` and treats it as no filter; other non-empty values remain exact
bands. Without this normalization the default roster hides every observed
identity, including seeded fixtures.

# Authorization and audit

All `PlayerService` procedures are mapped to `players:*` permissions and
server-scoped procedures use `server_id` so subuser grants apply. Moderation
uses a request id, a journal row and an `ActivityLog` entry with subject type
`player`. Undo exists only where a prior value was recorded; kick is explicitly
not undoable. The legacy `SendCommand` string path remains `servers:command`
and is not a moderation boundary.
