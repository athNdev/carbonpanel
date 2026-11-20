---
type: Architecture
title: "Player management - Identity, provenance and capabilities"
description: "PlayerIdentity is a panel cache/index with an opaque pl_* key. Online UUID,"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/player.proto, internal/rpc/services/player.go, internal/rpc/services/player_wiring.go, internal/db/player.go, internal/playeradapters, internal/playerskin, internal/playerdata, internal/rbac/mapping.go]
sources:
  - title: Parent concept
    resource: player-management.md
---
# Identity and provenance

`PlayerIdentity` is a panel cache/index with an opaque `pl_*` key. Online UUID,
offline UUID, XUID and Floodgate UUID remain distinct; aliases are only names
this panel observed. Mojang no longer provides public name history, so no
third-party name lookup is used and the UI states this. Every non-authoritative
field carries a source, observation time, confidence and conflict note; unknown
is not zero.

# Sources and capabilities

On Java 1.21.9 and 1.21.10 the native v1 adapter uses bearer authentication
when its server ID and node match an operator-configured player endpoint.
Direct RCON uses the same binding. Later versions and prereleases use an
available fallback. Live 1.21.9 tests covered roster, join/leave, moderation
and kicks. A kick requires the server to acknowledge the target. RCON and
node-scoped exec use the allowlisted commands in `internal/playeradapters`.
Optional plugin bridges (vitals, inventory, groups, mute) and full Bedrock
identity parity are decision-gated: they are reported as decision-pending or
unsupported with an actionable explanation, never shipped as implied vanilla
capabilities.

# Skins, identity lookup and deep data

`internal/playerskin` fetches Mojang-hosted textures server-side, caches them
same-origin with bounded TTL/size and renders a 2D face. It accepts only
canonical UUIDs and pinned texture hashes; arbitrary URLs and cross-host
redirects are refused. The same bounded provider transport resolves an exact
UUID (session-server profile) or current username (Minecraft Services name
lookup). `LookupPlayer` is read-only: its ephemeral result never persists an
identity, session, access row, roster entry, presence claim or audit row.
See the [lookup profile](player-management-lookup-profile.md) for portraits and recorded joins.
Authoritative negatives are `NOT_FOUND`; outages and timeouts are `UNAVAILABLE`; unpinnable
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
