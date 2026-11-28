---
type: Architecture
title: "Player management - Identity, provenance and capabilities"
description: "PlayerIdentity is a panel cache/index with an opaque pl_* key. Online UUID,"
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1/player.proto, internal/rpc/services/player.go, internal/rpc/services/player_wiring.go, internal/db/player.go, internal/playeradapters, internal/playerskin, internal/playerdata, internal/rbac/mapping.go, web/carbon-panel/src/lib/components/server-players.svelte, web/carbon-panel/src/lib/features/players/allowlist-dialog-state.ts]
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
Authoritative negatives are `NOT_FOUND`, outages are `UNAVAILABLE` and
unpinnable queries are `AMBIGUOUS`. See the [lookup profile](player-management-lookup-profile.md) for portraits and recorded joins. The server Players tab keeps roster filters
visible after a zero-match search and opens a scoped allowlist dialog.
Allowlist adds reuse typed moderation (`players:access:write` on `server_id`),
journal, audit and undo, and persist only the identity the mutation needs.
Java name/UUID entries use the negotiated Java adapter. Bedrock Gamertag/XUID
entries require an adapter proving Bedrock support. The dialog's polling lifetime is in [allowlist dialog lifetime](../decisions/allowlist-dialog-lifetime.md).
`internal/playerdata` reads `stats`, `advancements` and `playerdata` snapshots
through the injected node resolver with bounded reads; live NBT is never
edited. Resolution is in [snapshot resolution](player-management-snapshot-resolution.md).

# Roster filters

`ListPlayers` accepts the console's `all` sentinel for `state_filter` and
`access_filter` and treats it as no filter; other non-empty values remain exact
bands. Without this normalization the default roster hides every observed
identity, including seeded fixtures.
