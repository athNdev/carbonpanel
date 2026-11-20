---
type: Decision
title: Native map assets use scoped World View sessions
description: Native terrain uses the existing authenticated World View session with a server-validated world binding.
status: stable
owners: [oss-runtime]
code_refs: [internal/worldview/native_map.go, internal/worldview/native_end.go, internal/worldview/native_scope.go, internal/worldview/proxy.go, internal/worldview/registry.go]
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

End assets require the built-in Java 1.21.9 Vanilla generator and biome source.
Pin ordinary local directories to the active level and its DIM1/region tree.
Recheck the source identity, active world and renderer before publication.
The optional manifest dimensions describe only qualified saved sources.
The viewport offers only those dimensions and opens the Overworld by default.
The active native dimension also scopes panel-owned marker reads and copied
coordinate links. If optional End storage is unavailable, retain an Overworld
manifest only after checking the same world and renderer again.

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
