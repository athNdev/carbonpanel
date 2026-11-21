# Native Java terrain renderer

The built-in terrain reader draws a top-down map of the saved Java Overworld.
An original palette distinguishes common surface materials such as water,
sand, snow, rock and tree canopies. Water color varies with shallow depth and
land shading follows slope. Unknown or unavailable block states use elevation
colors. The map uses saved blocks and heights without game textures or biome
tints. Transparent areas represent empty columns, ungenerated chunks, or chunks the reader could not
qualify. A successful tile reports how many chunks were rendered, missing,
unsupported, or unreadable.

The first renderer supports standard-height Overworld chunks from Java 1.18
through 1.21.9 (DataVersion 2860–4554). Fully generated chunks use the saved
`Heightmaps.WORLD_SURFACE` array to locate the visible block in each column's
section palette. Late generation stages can also contain
saved surface heights, either in that array or in `WORLD_SURFACE_WG`. The
interactive map fades those provisional surfaces until the chunk is complete.
Earlier stages
and chunks without a readable surface stay blank. The file is selected
from the owning world's `region/` directory. It does not follow external
`.mcc` chunk payloads. Gzip, zlib, and uncompressed records are supported.
LZ4, older and newer formats, nonstandard world heights, Nether, End, and
Bedrock are outside this renderer's format scope.

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
map reads only saved Overworld data. **Map span** sets the longer edge of the
viewport from 32 to 1,024 blocks. **Auto** opens at 256 blocks and expands to
512 when every tile in the initial view has complete coverage. Panning loads
the visible area with two concurrent requests. To extend a new world's saved
terrain, explore it in Minecraft and use **Refresh terrain** after the game saves.
**Latest chunk save in this view** is the newest available Minecraft save time
among the chunks on screen. Moving players do not appear on the built-in map.
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
map keys even when the level folder name stays the same. The native renderer accepts only Overworld coordinate
tile requests, and permits two concurrent renders in the controller. A busy
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
chunk count and color diversity. It does not require a running server.

- [Mojang's Java 1.18 release notes](https://feedback.minecraft.net/hc/en-us/articles/4415128577293-Minecraft-Java-Edition-1-18)
- [PrismarineJS Anvil format implementation](https://github.com/PrismarineJS/prismarine-provider-anvil)
