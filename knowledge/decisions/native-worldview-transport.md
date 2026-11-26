---
type: Decision
title: Native map assets use scoped World View sessions
description: Native terrain uses the existing authenticated World View session with a server-validated world binding.
status: stable
owners: [oss-runtime]
code_refs: [internal/worldview/native_map.go, internal/worldview/native_scope.go, internal/worldview/proxy.go, internal/worldview/registry.go, web/carbon-panel/src/lib/features/worldview/native-map-loader.ts]
sources:
  - title: Native terrain format and access
    resource: ../../docs/world-terrain.md
  - title: Map observation and authorization
    resource: ../architecture/world-viewing.md
---

# Choice

Use the existing same-origin World View session for the native manifest and PNG
tile paths. Derive the active level from persisted server configuration. Keep
node endpoint generation, active revision and the last restore journal ID in the
key's private scope. Recheck server authorization and the bounded level format
before serving assets. Refuse assets while lifecycle operations hold the world.

# Alternatives

A separate public tile service would require its own authorization and storage
identity and could bypass per-server permission changes. Downloading region
files to the browser would disclose unrelated world data and require broader
file permissions. Reusing the scoped session keeps both checks at one entry point.

# Consequences

Configured and discoverable adapters retain their current precedence. Native
terrain covers supported controller-local saves. It uses bounded workers and
private no-store responses. Tiles show saved observations rather than live
game state. World changes invalidate the key and require a new session.
The viewport queues only visible tiles, nearest first, with two concurrent reads.
Map span covers 32–1,024 blocks along the longer viewport edge. Automatic span
widens from 256 to 512 blocks when the initial tiles have full coverage.
Panning and coordinate jumps load saved tiles across the Java Overworld bounds.
Empty, incomplete and transiently unavailable tiles recheck every 30 seconds
while visible. Missing tiles for a new view take priority over refreshes.
