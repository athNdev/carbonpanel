# Native Java terrain renderer

The built-in terrain reader draws a top-down map of the saved Java terrain.
An original palette distinguishes common surface materials such as water,
sand, snow, rock and tree canopies. Water color varies with shallow depth and
land shading follows slope. Unknown or unavailable block states use elevation
colors. The map uses saved blocks and heights without game textures or biome
tints. Transparent areas represent empty columns, ungenerated chunks, or chunks the reader could not
qualify. A successful tile reports how many chunks were rendered, missing,
unsupported, or unreadable.

The renderer supports vanilla-height Overworld chunks from Java 1.18
through 26.3 (DataVersion 2860–5023), the Nether (storage span 0–255, rendered below the bedrock roof) and the End (0–255)
from the same versions. Fully generated chunks use the saved
`Heightmaps.WORLD_SURFACE` array to locate the visible block in each column's
section palette. Late generation stages can also contain
saved surface heights, either in that array or in `WORLD_SURFACE_WG`. The
interactive map fades those provisional surfaces until the chunk is complete.
Earlier stages
and chunks without a readable surface stay blank. Overworld files are selected
from the owning world's `region/` directory, the Nether's from
`DIM-1/region/` and the End's from `DIM1/region/`. The reader does not follow
external `.mcc` chunk payloads. Gzip, zlib, uncompressed, and LZ4 records
(the 1.20.5+ `region-file-compression=lz4` option) are supported. Older
formats, DataVersions above 26.3, nonstandard (modded) dimension heights,
modded dimension folders, Bukkit-style sibling worlds, and Bedrock are outside
this renderer's format scope. The Nether's saved `WORLD_SURFACE` is its full
bedrock roof (a complete sheet at y=127 with partial layers down to y=123),
so the Nether map looks under the roof: each column renders the first saved
terrain block below it, and a column that holds only roof bedrock and air
stays transparent.

Each request reads one 128 × 128 block tile at one pixel per block. X increases
right and Z increases down. It reads at most 64 selected chunks from a single
region through the owning server's confined file accessor. Compressed chunk
and tile limits are 512 KiB and 8 MiB. Decompressed limits are 2 MiB and
24 MiB. The reader rejects the result if its selected region header entries
change while it reads. Saved times are the oldest and newest nonzero
timestamps for the returned chunks. A tile represents disk observations and
may lag in-game changes until Minecraft saves.

## Carbon Panel map access

The World View workspace can open a built-in map for a supported Java save on
the controller's local Docker node. Explicit renderer configuration takes
precedence. The panel switches to a discovered BlueMap installation when it
can serve this world's map, and uses the built-in map while that installation
is stopped or waiting for its first render when a readable save is available. A native
map reads only saved terrain data. **Map span** sets the longer edge of the
viewport from 32 to 1,024 blocks. **Auto** opens at 256 blocks and expands to
512 when every tile in the initial view has complete coverage. The span sets how
much fits on screen. Pan or use **Jump** to reach any valid world coordinate.
When the world has saved Nether or End terrain, a **Dimension** selector lists
the offered dimensions; each dimension keeps its own terrain view and its own
region folder.
The map loads the visible area with up to two concurrent requests and rechecks
visible gaps every 30 seconds. **Live updates** (on by default) also
revalidates complete tiles: every 60 seconds the map asks whether the backing
region file changed and re-renders only when it did, so fresh saves appear
without manual refreshing. Turning **Live updates** off freezes fully rendered
tiles until the next manual refresh. **Refresh terrain** checks immediately in
either mode. Explore a new area in Minecraft to generate terrain.
New chunks can appear after Minecraft saves. Opening, panning or jumping on the
map has no effect on the world save.
For a compatible server with Chunky installed, use the server console to
pregenerate a selected area. [Chunky pregeneration guide](https://github.com/pop4959/Chunky/wiki/Pregeneration)
**Latest chunk save in this view** is the newest available Minecraft save time
among the chunks on screen, with its age while live updates are on. Map
markers are placed on the Overworld only. Moving players do not appear on the
built-in map.
The server overview preview combines nearby tiles around saved spawn and frames
the largest continuous patch when it covers most of the sample. Otherwise it
trims empty outer margins and leaves unsaved gaps transparent.
Check the tile's saved time and coverage
when terrain is still being generated or saved. Use **Copy map link** to
share the current coordinates with someone who can read the same world.
Ordinary save churn retries the affected tile in place. In a large server
list, Carbon Panel may check the save format when you open the world. An
unsupported save reports the format limit before opening the map.

The panel authorizes its native manifest and PNG requests through the existing
short-lived World View key and the operator's server-specific read permission.
The server's canonical level, current node generation, World Library revision
and last restore operation scope each key. Restoring a backup invalidates open
map keys even when the level folder name stays the same. The native renderer
accepts only canonical coordinate tile requests under the served dimension
prefixes, and permits two concurrent renders in the controller. A busy
request can be retried. The server supplies only a rendered tile, not a raw
region file. Viewing uses no runtime module, game assets or external services.

## Qualification

The default suite creates synthetic Anvil files with controllable versions,
compression, coordinates, and bad records. To exercise a disposable
game-generated save, set `CARBON_TERRAIN_FIXTURE_ROOT` to its server data root
and `CARBON_TERRAIN_FIXTURE_LEVEL` to its level directory, then use
`go test ./internal/worldterrain -run TestRenderOverworldTileGameSave -v`.
Optional `CARBON_TERRAIN_FIXTURE_PNG` must name a new artifact outside the
save. The fixture renders nine tiles around the origin and records the saved
chunk count and color diversity. It does not require a running server. Run it
once per release before trusting a raised DataVersion ceiling: the ceiling
itself is the fail-closed guard, and any chunk whose layout does not match a
known dimension span stays unsupported.

- [Mojang's Java 1.18 release notes](https://feedback.minecraft.net/hc/en-us/articles/4415128577293-Minecraft-Java-Edition-1-18)
- [PrismarineJS Anvil format implementation](https://github.com/PrismarineJS/prismarine-provider-anvil)
- [lz4-java block stream](https://github.com/lz4/lz4-java) (the framing Minecraft uses for LZ4 region chunks)
